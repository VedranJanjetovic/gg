package verification

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const defaultOutputLimit = 128 * 1024

// RunnerOptions configures synchronous direct process execution.
type RunnerOptions struct {
	Executor       Executor
	Environment    map[string]string
	LogDirectory   string
	MaxOutputBytes int
}

// Runner executes planned steps and stores bounded evidence in .gg logs.
type Runner struct {
	executor       Executor
	environment    map[string]string
	logDirectory   string
	maxOutputBytes int
}

// NewRunner creates a runner. An empty log directory is resolved per Run to
// <worktree>/.gg/verification-logs.
func NewRunner(options RunnerOptions) *Runner {
	limit := options.MaxOutputBytes
	if limit <= 0 {
		limit = defaultOutputLimit
	}
	environment := make(map[string]string, len(options.Environment))
	for key, value := range options.Environment {
		environment[key] = value
	}
	executor := options.Executor
	if executor == nil {
		executor = directExecutor{maxOutputBytes: limit}
	}
	return &Runner{executor: executor, environment: environment, logDirectory: options.LogDirectory, maxOutputBytes: limit}
}

// Run executes every step once. A process exit is represented by its result;
// returned errors are reserved for invalid input, unavailable execution, I/O,
// and cancellation so callers can distinguish pause-worthy infrastructure.
func (r *Runner) Run(ctx context.Context, worktree string, steps []Step) (Report, error) {
	if r == nil || r.executor == nil {
		return Report{}, errors.New("verification runner is required")
	}
	if strings.TrimSpace(worktree) == "" {
		return Report{}, errors.New("verification worktree is required")
	}
	if len(steps) == 0 {
		return Report{}, errors.New("verification requires at least one step")
	}
	for _, step := range steps {
		if err := step.Validate(); err != nil {
			return Report{}, err
		}
	}
	logDirectory := r.logDirectory
	if strings.TrimSpace(logDirectory) == "" {
		logDirectory = filepath.Join(worktree, ".gg", "verification-logs")
	}
	if err := os.MkdirAll(logDirectory, 0o755); err != nil {
		return Report{}, fmt.Errorf("create verification log directory: %w", err)
	}
	report := Report{Results: make([]CommandResult, 0, len(steps))}
	var unavailable []error
	for index, step := range steps {
		result, err := r.runStep(ctx, worktree, logDirectory, index, 0, step)
		report.Results = append(report.Results, result)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return report, err
			}
			unavailable = append(unavailable, err)
		}
	}
	return report, errors.Join(unavailable...)
}

// Verify executes the planned set and immediately retries each failed,
// classifiable step exactly once. A retry that passes is retained as a flaky
// warning; a second failure remains a real failure.
func (r *Runner) Verify(ctx context.Context, worktree string, steps []Step) (Report, error) {
	report, err := r.Run(ctx, worktree, steps)
	if err != nil {
		return report, err
	}
	for i, result := range report.Results {
		if result.Status != CommandFailed || len(result.Failures) == 0 {
			continue
		}
		step := steps[i]
		verified, warnings, attempts, retryErr := r.confirmFailure(ctx, worktree, i, step, result)
		if retryErr != nil {
			return report, retryErr
		}
		verified.RetryCount = attempts
		report.Warnings = append(report.Warnings, warnings...)
		report.Results[i] = verified
	}
	return report, nil
}

func (r *Runner) confirmFailure(ctx context.Context, worktree string, index int, step Step, original CommandResult) (CommandResult, []Warning, int, error) {
	logDirectory := filepath.Dir(original.LogPath)
	// Go test exposes an executable test identity, so confirm every failed
	// test independently. Other adapters only expose a command-level retry;
	// rerunning them once is the smallest stable target available.
	if step.Adapter != AdapterGoTest || !allTestIdentities(original.Failures) {
		retry, err := r.runStep(ctx, worktree, logDirectory, index, 1, retryStep(step, original.Failures[0]))
		if err != nil {
			return original, nil, 0, err
		}
		if retry.Status == CommandPassed {
			return retry, flakyWarnings(original.StepName, original.Failures, retry.LogPath), 1, nil
		}
		return retry, nil, 1, nil
	}

	remaining := make([]IndividualFailure, 0, len(original.Failures))
	var warnings []Warning
	var lastResult CommandResult
	for attempt, failure := range original.Failures {
		retry, err := r.runStep(ctx, worktree, logDirectory, index, attempt+1, retryStep(step, failure))
		if err != nil {
			return original, warnings, attempt, err
		}
		lastResult = retry
		if retry.Status == CommandPassed {
			warnings = append(warnings, flakyWarnings(original.StepName, []IndividualFailure{failure}, retry.LogPath)...)
			continue
		}
		matched := matchFailure(failure, retry.Failures)
		if len(matched) == 0 {
			// A narrowed retry that cannot identify the original test must
			// remain strict-fallback evidence rather than being treated as a
			// repaired test.
			lastResult.Status = CommandUnclassifiable
			lastResult.Failures = nil
			return lastResult, warnings, attempt + 1, nil
		}
		remaining = append(remaining, matched...)
	}
	if len(remaining) == 0 {
		lastResult.Status = CommandPassed
		lastResult.Failures = nil
	} else {
		lastResult.Status = CommandFailed
		lastResult.Failures = sortFailures(remaining)
	}
	return lastResult, warnings, len(original.Failures), nil
}

func allTestIdentities(failures []IndividualFailure) bool {
	if len(failures) == 0 {
		return false
	}
	for _, failure := range failures {
		name := testName(failure.Identity)
		if !strings.HasPrefix(name, "Test") && !strings.HasPrefix(name, "Benchmark") && !strings.HasPrefix(name, "Fuzz") && !strings.HasPrefix(name, "Example") {
			return false
		}
	}
	return true
}

func testName(identity string) string {
	if colon := strings.LastIndex(identity, ":"); colon >= 0 {
		return identity[colon+1:]
	}
	return identity
}

func matchFailure(original IndividualFailure, candidates []IndividualFailure) []IndividualFailure {
	name := testName(original.Identity)
	for _, candidate := range candidates {
		if candidate.Identity == original.Identity || testName(candidate.Identity) == name {
			candidate.Identity = original.Identity
			candidate.Reason = NormalizeReason(candidate.Reason)
			return []IndividualFailure{candidate}
		}
	}
	return nil
}

func flakyWarnings(checkName string, failures []IndividualFailure, logPath string) []Warning {
	warnings := make([]Warning, 0, len(failures))
	for _, failure := range failures {
		warnings = append(warnings, Warning{Key: FailureKey{CheckName: checkName, Identity: failure.Identity}, Reason: NormalizeReason(failure.Reason), Classification: ClassificationFlaky, LogPath: logPath})
	}
	return warnings
}

func (r *Runner) runStep(ctx context.Context, worktree, logDirectory string, index, attempt int, step Step) (CommandResult, error) {
	if err := ctx.Err(); err != nil {
		return CommandResult{}, err
	}
	environment := mergeEnvironment(r.environment, step.Env)
	stdout, stderr, exitCode, err := r.executor.Execute(ctx, worktree, step.Command, append([]string(nil), step.Args...), environment)
	output := boundedOutput(stdout, stderr, r.maxOutputBytes)
	logName := fmt.Sprintf("%02d-%s.log", index, safeName(step.Name))
	if attempt > 0 {
		logName = fmt.Sprintf("%02d-%s-retry-%d.log", index, safeName(step.Name), attempt)
	}
	logPath := filepath.Join(logDirectory, logName)
	if writeErr := os.WriteFile(logPath, []byte(output), 0o644); writeErr != nil {
		return CommandResult{}, fmt.Errorf("write verification log %q: %w", logPath, writeErr)
	}
	result := CommandResult{StepName: step.Name, Command: step.Command, Args: append([]string(nil), step.Args...), ExitCode: exitCode, LogPath: logPath, Output: output, Status: CommandPassed}
	failures, classifiable, parseErr := ParseOutput(step.Adapter, stdout, stderr, exitCode)
	if parseErr != nil {
		return CommandResult{}, parseErr
	}
	result.Failures = sortFailures(failures)
	if exitCode != 0 || len(result.Failures) > 0 {
		result.Status = CommandFailed
		if !classifiable || (exitCode != 0 && len(result.Failures) == 0) {
			result.Status = CommandUnclassifiable
		}
	}
	if err != nil {
		result.Status = CommandUnavailable
		result.UnavailableErr = err.Error()
		return result, &UnavailableError{Command: step.Command, Err: err}
	}
	return result, nil
}

func mergeEnvironment(base, overlay map[string]string) []string {
	values := make(map[string]string, len(base)+len(overlay))
	for _, entry := range os.Environ() {
		if key, value, ok := strings.Cut(entry, "="); ok {
			values[key] = value
		}
	}
	for key, value := range base {
		values[key] = value
	}
	for key, value := range overlay {
		values[key] = value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}

func boundedOutput(stdout, stderr string, limit int) string {
	var buffer bytes.Buffer
	write := func(label, value string) {
		if value == "" || buffer.Len() >= limit {
			return
		}
		remaining := limit - buffer.Len()
		prefix := label + "\n"
		if len(prefix) >= remaining {
			_, _ = buffer.WriteString(prefix[:remaining])
			return
		}
		_, _ = io.WriteString(&buffer, prefix)
		remaining = limit - buffer.Len()
		if len(value) > remaining {
			value = value[:remaining]
		}
		_, _ = buffer.WriteString(value)
		if buffer.Len() < limit {
			_, _ = buffer.WriteString("\n")
		}
	}
	write("[stdout]", stdout)
	write("[stderr]", stderr)
	return buffer.String()
}

func safeName(value string) string {
	var b strings.Builder
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "step"
	}
	return b.String()
}

type directExecutor struct{ maxOutputBytes int }

func (e directExecutor) Execute(ctx context.Context, worktree, command string, args []string, environment []string) (string, string, int, error) {
	resolved, err := lookPathWithEnvironment(command, environment)
	if err != nil {
		return "", "", -1, err
	}
	process := exec.CommandContext(ctx, resolved, args...)
	process.Dir = worktree
	process.Env = environment
	stdout := &boundedBuffer{limit: e.maxOutputBytes}
	stderr := &boundedBuffer{limit: e.maxOutputBytes}
	process.Stdout = stdout
	process.Stderr = stderr
	err = process.Run()
	if err == nil {
		return stdout.String(), stderr.String(), 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return stdout.String(), stderr.String(), exitErr.ExitCode(), nil
	}
	return stdout.String(), stderr.String(), -1, err
}

func lookPathWithEnvironment(command string, environment []string) (string, error) {
	pathValue := ""
	hasPath := false
	for _, entry := range environment {
		key, value, ok := strings.Cut(entry, "=")
		if ok && key == "PATH" {
			hasPath = true
			pathValue = value
			break
		}
	}
	if !hasPath || strings.ContainsAny(command, `/\\`) {
		return exec.LookPath(command)
	}
	for _, directory := range filepath.SplitList(pathValue) {
		if directory == "" {
			directory = "."
		}
		candidate := filepath.Join(directory, command)
		if resolved, err := exec.LookPath(candidate); err == nil {
			return resolved, nil
		}
	}
	return "", &os.PathError{Op: "lookpath", Path: command, Err: exec.ErrNotFound}
}

// retryStep narrows Go test confirmation to the individual test when the
// adapter exposed one. Other adapters rerun their smallest stable executable
// unit: the named command itself.
func retryStep(step Step, failure IndividualFailure) Step {
	if step.Adapter != AdapterGoTest || strings.TrimSpace(failure.Identity) == "" {
		return step
	}
	testName := testName(failure.Identity)
	if !strings.HasPrefix(testName, "Test") && !strings.HasPrefix(testName, "Benchmark") && !strings.HasPrefix(testName, "Fuzz") && !strings.HasPrefix(testName, "Example") {
		return step
	}
	args := append([]string(nil), step.Args...)
	pattern := "^" + regexp.QuoteMeta(testName) + "$"
	for index := 1; index < len(args); index++ {
		if args[index] == "-run" {
			if index+1 < len(args) {
				args[index+1] = pattern
				step.Args = args
			}
			return step
		}
		if strings.HasPrefix(args[index], "-run=") {
			args[index] = "-run=" + pattern
			step.Args = args
			return step
		}
	}
	insertAt := goTestPackageIndex(args)
	args = append(args[:insertAt], append([]string{"-run", pattern}, args[insertAt:]...)...)
	step.Args = args
	return step
}

func goTestPackageIndex(args []string) int {
	for index := 1; index < len(args); index++ {
		arg := args[index]
		if strings.HasPrefix(arg, "-") {
			if goTestFlagTakesValue(arg) && index+1 < len(args) && !strings.Contains(arg, "=") {
				index++
			}
			continue
		}
		return index
	}
	return len(args)
}

func goTestFlagTakesValue(arg string) bool {
	name := arg
	if equals := strings.IndexByte(name, '='); equals >= 0 {
		name = name[:equals]
	}
	switch name {
	case "-bench", "-benchtime", "-cpu", "-count", "-list", "-run", "-timeout", "-vet":
		return true
	default:
		return false
	}
}

type boundedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(data []byte) (int, error) {
	if b.limit <= 0 {
		return len(data), nil
	}
	remaining := b.limit - b.Len()
	if remaining > 0 {
		_, _ = b.Buffer.Write(data[:minInt(len(data), remaining)])
	}
	return len(data), nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
