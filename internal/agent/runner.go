package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/VedranJanjetovic/gg/internal/config"
	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/proof"
	"github.com/VedranJanjetovic/gg/internal/state"
)

type OutcomeRecorder interface {
	RecordExecution(context.Context, string, string, string, state.ExecutionOutcome, []string) (state.ProjectState, error)
}

type StopRegistry struct {
	mu     sync.Mutex
	active map[string]Process
}

func NewStopRegistry() *StopRegistry { return &StopRegistry{active: make(map[string]Process)} }
func (r *StopRegistry) Register(id string, p Process) error {
	if r == nil || strings.TrimSpace(id) == "" || p == nil {
		return errors.New("run ID and process are required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.active[id]; ok {
		return fmt.Errorf("run %q is already active", id)
	}
	r.active[id] = p
	return nil
}
func (r *StopRegistry) Unregister(id string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	delete(r.active, id)
	r.mu.Unlock()
}
func (r *StopRegistry) Stop(id string) error {
	if r == nil {
		return errors.New("stop registry is nil")
	}
	r.mu.Lock()
	p, ok := r.active[id]
	r.mu.Unlock()
	if !ok {
		return fmt.Errorf("run %q is not active", id)
	}
	return p.Cancel()
}
func (r *StopRegistry) Active(id string) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.active[id]
	return ok
}

type MultiSink struct{ sinks []EventSink }

func NewMultiSink(sinks ...EventSink) *MultiSink {
	out := make([]EventSink, 0, len(sinks))
	for _, s := range sinks {
		if s != nil {
			out = append(out, s)
		}
	}
	return &MultiSink{sinks: out}
}
func (s *MultiSink) Publish(ctx context.Context, e Event) error {
	var errs []error
	for _, sink := range s.sinks {
		c := e
		c.Payload = append([]byte(nil), e.Payload...)
		if err := sink.Publish(ctx, c); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

type FilePhaseLog struct {
	mu         sync.Mutex
	file       *os.File
	path       string
	stdoutTail []byte
	stderrTail []byte
}

func NewFilePhaseLog(root, phase, subphase string) (*FilePhaseLog, error) {
	return newFilePhaseLog(root, "", phase, subphase)
}

// NewProjectPhaseLog stores logs in the durable per-project directory.
func NewProjectPhaseLog(root, slug, phase, subphase string) (*FilePhaseLog, error) {
	return newFilePhaseLog(root, slug, phase, subphase)
}

func newFilePhaseLog(root, slug, phase, subphase string) (*FilePhaseLog, error) {
	if strings.TrimSpace(root) == "" || strings.TrimSpace(phase) == "" {
		return nil, errors.New("project root and phase are required")
	}
	name := safeLogName(phase)
	if subphase != "" {
		name += "-" + safeLogName(subphase)
	}
	dir := filepath.Join(root, ".gg", "projects")
	if slug != "" {
		dir = filepath.Join(dir, safeLogName(slug), "logs")
	} else {
		dir = filepath.Join(dir, "logs")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, name+".log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, err
	}
	return &FilePhaseLog{file: f, path: path}, nil
}
func (l *FilePhaseLog) Write(_ context.Context, stream string, payload []byte) error {
	if l == nil || l.file == nil {
		return errors.New("phase log is closed")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.appendTail(stream, payload)
	if _, err := l.file.Write(append([]byte(fmt.Sprintf("[%s] ", stream)), payload...)); err != nil {
		return err
	}
	return l.file.Sync()
}

// tailCap bounds the in-memory per-stream tails kept for post-run parsing
// (token usage); agents report usage at the end of their output.
const tailCap = 128 * 1024

func (l *FilePhaseLog) appendTail(stream string, payload []byte) {
	buffer := &l.stdoutTail
	if stream == "stderr" {
		buffer = &l.stderrTail
	}
	*buffer = append(*buffer, payload...)
	if overflow := len(*buffer) - tailCap; overflow > 0 {
		*buffer = append((*buffer)[:0], (*buffer)[overflow:]...)
	}
}

// StreamTails returns the retained ends of the process stdout and stderr.
func (l *FilePhaseLog) StreamTails() (stdout, stderr string) {
	if l == nil {
		return "", ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return string(l.stdoutTail), string(l.stderrTail)
}
func (l *FilePhaseLog) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	err := l.file.Close()
	l.file = nil
	return err
}
func (l *FilePhaseLog) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}
func safeLogName(v string) string {
	var b strings.Builder
	for _, r := range v {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

type AgentRunner struct {
	factory  ProcessFactory
	prompt   PromptBuilder
	lookup   LookPath
	registry *StopRegistry
	events   EventSink
	results  ResultStore
	recorder OutcomeRecorder
	logRoot  string
	proof    *proof.ArtifactService
}
type AgentRunnerOptions struct {
	Factory  ProcessFactory
	Prompt   PromptBuilder
	Lookup   LookPath
	Registry *StopRegistry
	Events   EventSink
	Results  ResultStore
	Recorder OutcomeRecorder
	// LogRoot is the durable state root. Production callers must keep it
	// separate from phase worktrees so observability files never dirty Git.
	LogRoot string
	Proof   *proof.ArtifactService
}

func NewAgentRunner(o AgentRunnerOptions) *AgentRunner {
	p := o.Prompt
	if p == nil {
		p = StandalonePromptBuilder{}
	}
	return &AgentRunner{factory: o.Factory, prompt: p, lookup: o.Lookup, registry: o.Registry, events: o.Events, results: o.Results, recorder: o.Recorder, logRoot: o.LogRoot, proof: o.Proof}
}

func (r *AgentRunner) Run(ctx context.Context, req RunRequest) (RunResult, error) {
	if r == nil || r.factory == nil {
		return RunResult{}, errors.New("agent runner process factory is required")
	}
	if err := ctx.Err(); err != nil {
		return RunResult{}, err
	}
	if strings.TrimSpace(req.WorkingDirectory) == "" {
		req.WorkingDirectory = req.Project.WorktreePath
	}
	workingDirectory, err := cleanExistingDirectory(req.WorkingDirectory)
	if err != nil {
		return RunResult{}, fmt.Errorf("validate agent worktree: %w", err)
	}
	req.WorkingDirectory = workingDirectory
	if req.Prompt == "" {
		return RunResult{}, errors.New("agent prompt is required")
	}
	if strings.TrimSpace(req.RunID) == "" {
		return RunResult{}, errors.New("agent run ID is required")
	}
	provider, err := NewProvider(req.Settings, r.lookup)
	if err != nil {
		return RunResult{}, err
	}
	spec, err := provider.BuildSpec(req)
	if err != nil {
		return RunResult{}, err
	}
	logRoot := r.logRoot
	if strings.TrimSpace(logRoot) == "" {
		// Retain a deterministic compatibility default for direct test/library
		// callers. The production composition root always supplies LogRoot.
		logRoot = req.WorkingDirectory
	}
	var proofBaseline proof.ArtifactBaseline
	if req.Phase == pipeline.PhaseQA && r.proof != nil {
		proofBaseline, err = r.proof.Capture(ctx, req.WorkingDirectory)
		if err != nil {
			return RunResult{}, err
		}
	}
	if err := ensureArtifactWorkspace(req.WorkingDirectory); err != nil {
		return RunResult{}, fmt.Errorf("prepare artifact workspace: %w", err)
	}
	log, err := NewProjectPhaseLog(logRoot, req.Project.Slug, string(req.Phase), req.Subphase)
	if err != nil {
		return RunResult{}, err
	}
	defer log.Close()
	spec.Logs = log
	var outputGate *startedEventGate
	if r.events != nil {
		outputGate = newStartedEventGate(ctx, &runEventSink{sink: r.events, request: req})
		spec.Events = outputGate
	}
	process, err := r.factory.Start(ctx, spec)
	if err != nil {
		if outputGate != nil {
			outputGate.release()
		}
		return RunResult{}, err
	}
	id := req.RunID
	if id == "" {
		id = req.Project.Slug + "/" + string(req.Phase) + "/" + req.Subphase
	}
	if r.registry != nil {
		if err := r.registry.Register(id, process); err != nil {
			_ = process.Cancel()
			if outputGate != nil {
				outputGate.release()
			}
			return RunResult{}, err
		}
		defer r.registry.Unregister(id)
	}
	started := time.Now().UTC()
	var eventErr error
	if r.events != nil {
		eventErr = r.events.Publish(ctx, Event{ProjectSlug: req.Project.Slug, Phase: req.Phase, Subphase: req.Subphase, Type: EventStarted, At: started})
		if eventErr != nil {
			_ = process.Cancel()
		}
	}
	if outputGate != nil {
		outputGate.release()
	}
	pr, waitErr := process.Wait()
	waitErr = errors.Join(waitErr, eventErr)
	status, et := state.StatusFinished, EventCompleted
	if ctx.Err() != nil {
		status, et = state.StatusStopped, EventCanceled
	} else if waitErr != nil || pr.ExitCode != 0 {
		status, et = state.StatusFailed, EventFailed
	}
	result := RunResult{ProjectSlug: req.Project.Slug, Phase: req.Phase, Subphase: req.Subphase, Status: status, ExitCode: pr.ExitCode, StartedAt: pr.StartedAt, FinishedAt: pr.FinishedAt, Duration: pr.Duration, ArtifactPaths: discoverArtifacts(req.WorkingDirectory, req.Phase, req.ArtifactPaths)}
	if req.Phase == pipeline.PhaseQA && r.proof != nil && status != state.StatusStopped {
		artifact, proofErr := r.proof.DiscoverAndCopy(ctx, req.WorkingDirectory, req.Project.Slug, proofBaseline, req.RunID, req.Project.GitDisabled)
		if proofErr != nil {
			// A missing or malformed proof is a protocol failure, not actionable
			// semantic QA feedback. Keep it out of the retryable feedback loop.
			result.Disposition = DispositionFailed
			result.Status = state.StatusFailed
			status, et = state.StatusFailed, EventFailed
			waitErr = errors.Join(waitErr, fmt.Errorf("validate QA proof: %w", proofErr))
		} else {
			result.DeferredChecks = append([]proof.DeferredCheck(nil), artifact.DeferredChecks...)
			result.ArtifactPaths = appendUniquePath(result.ArtifactPaths, artifact.Path)
			switch artifact.Classification {
			case proof.ClassificationPass:
				result.Disposition = DispositionPassed
			case proof.ClassificationFeedback:
				result.Disposition = DispositionFeedback
			default:
				result.Disposition = DispositionFailed
			}
			if result.Disposition != DispositionPassed {
				result.Status = state.StatusFailed
				status, et = state.StatusFailed, EventFailed
				waitErr = errors.Join(waitErr, &SemanticFailureError{Phase: req.Phase, Disposition: result.Disposition})
			}
		}
	} else if status == state.StatusFinished {
		disposition, protocolErr := readCanonicalDisposition(req.WorkingDirectory, req.Phase, req.RunID)
		result.Disposition = disposition
		if protocolErr != nil {
			result.Status = state.StatusFailed
			status, et = state.StatusFailed, EventFailed
			waitErr = errors.Join(waitErr, protocolErr)
		} else if disposition != DispositionPassed {
			result.Status = state.StatusFailed
			status, et = state.StatusFailed, EventFailed
			waitErr = errors.Join(waitErr, &SemanticFailureError{Phase: req.Phase, Disposition: disposition})
		}
	}
	stdoutData, stderrData := log.StreamTails()
	if pr.ExitCode != 0 {
		// Surface the reason instead of a bare exit status. claude reports
		// API errors (unknown model, auth) as a JSON envelope on stdout with
		// an empty stderr; a broken agent installation dies with stderr only.
		if message := parseClaudeErrorResult(stdoutData); message != "" {
			waitErr = errors.Join(waitErr, fmt.Errorf("agent error: %s", message))
		}
		if tail := stderrTail(log.Path()); tail != "" {
			waitErr = errors.Join(waitErr, fmt.Errorf("agent stderr: %s", tail))
		}
	}
	if result.StartedAt.IsZero() {
		result.StartedAt = started
	}
	if result.FinishedAt.IsZero() {
		result.FinishedAt = time.Now().UTC()
	}
	if result.Duration == 0 {
		result.Duration = result.FinishedAt.Sub(result.StartedAt)
	}
	result.TokensUsed = parseTokenUsage(req.Settings.Agent, stdoutData, stderrData)
	if req.Settings.Agent == config.AgentClaude {
		result.CostUSD = parseClaudeCost(stdoutData)
	}
	metadataPath := log.Path() + ".json"
	out := state.ExecutionOutcome{Runtime: string(req.Settings.Agent), Model: req.Settings.Model, Effort: string(req.Settings.Effort), ExitCode: result.ExitCode, StartedAt: result.StartedAt, FinishedAt: result.FinishedAt, Duration: result.Duration, LogPath: log.Path(), MetadataPath: metadataPath, Canceled: et == EventCanceled, TokensUsed: result.TokensUsed, CostUSD: result.CostUSD}
	out.DeferredChecks = append([]proof.DeferredCheck(nil), result.DeferredChecks...)
	if status == state.StatusFailed && waitErr != nil {
		// Persist the failure reason so attached screens can answer "why did
		// this phase fail" without digging through log files. The result
		// carries it too, because in the production composition the
		// orchestrator (not this runner) records the durable outcome.
		out.Error = waitErr.Error()
		result.Error = out.Error
	}
	if metadata, e := json.MarshalIndent(out, "", "  "); e == nil {
		if e = os.WriteFile(metadataPath, append(metadata, '\n'), 0644); e != nil {
			waitErr = errors.Join(waitErr, e)
		}
	} else {
		waitErr = errors.Join(waitErr, e)
	}
	result.LogPaths = []string{log.Path(), metadataPath}
	if r.recorder != nil {
		_, e := r.recorder.RecordExecution(context.Background(), req.Project.Slug, string(req.Phase), req.Subphase, out, result.ArtifactPaths)
		waitErr = errors.Join(waitErr, e)
	}
	if r.results != nil {
		waitErr = errors.Join(waitErr, r.results.Save(context.Background(), result))
	}
	if r.events != nil {
		c := result
		waitErr = errors.Join(waitErr, r.events.Publish(context.WithoutCancel(ctx), Event{ProjectSlug: req.Project.Slug, Phase: req.Phase, Subphase: req.Subphase, Type: et, At: result.FinishedAt, Result: &c}))
	}
	return result, waitErr
}

// ReadPlanFrontmatter extracts the optional plan-tracking arrays from a
// phase's canonical artifact frontmatter: `gg_plan_phases` (the plan's
// implementation phases, written by Planning) and `gg_plan_completed` (the
// phases Development reports as done). Both are single-line JSON string
// arrays. Parsing is deliberately tolerant — plan visibility is display data
// and must never fail a phase — so any problem yields empty results.
func ReadPlanFrontmatter(root string, phase pipeline.PhaseID) (phases, completed []string) {
	name, ok := pipeline.CanonicalArtifactName(phase)
	if !ok {
		return nil, nil
	}
	data, err := os.ReadFile(filepath.Join(filepath.Clean(root), name))
	if err != nil {
		return nil, nil
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if len(lines) < 2 || lines[0] != "---" {
		return nil, nil
	}
	for _, line := range lines[1:] {
		if line == "---" {
			break
		}
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		switch strings.TrimSpace(key) {
		case "gg_plan_phases":
			phases = parseStringArray(value)
		case "gg_plan_completed":
			completed = parseStringArray(value)
		}
	}
	return phases, completed
}

// ReadVerificationContract reads the declaring phase's executable
// verification declaration. Planning owns it whenever it runs; Acceptance
// criteria owns it when Planning is disabled. Unlike the display-only plan
// progress parser above, this parser is strict because its result controls
// whether Development may start.
func ReadVerificationContract(root string, phase pipeline.PhaseID) (state.VerificationContract, error) {
	name, ok := pipeline.CanonicalArtifactName(phase)
	if !ok {
		return state.VerificationContract{}, fmt.Errorf("phase %q has no canonical artifact", phase)
	}
	data, err := os.ReadFile(filepath.Join(filepath.Clean(root), name))
	if err != nil {
		return state.VerificationContract{}, fmt.Errorf("read verification artifact: %w", err)
	}
	return ParseVerificationContract(data)
}

// ParseVerificationContract validates the single-line JSON frontmatter fields
// required by Planning. The body and unrelated frontmatter remain opaque.
func ParseVerificationContract(data []byte) (state.VerificationContract, error) {
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if len(lines) < 2 || lines[0] != "---" {
		return state.VerificationContract{}, errors.New("verification artifact must begin with frontmatter")
	}
	var stepsValue, repairValue string
	foundSteps, foundRepair := false, false
	closed := false
	for _, line := range lines[1:] {
		if line == "---" {
			closed = true
			break
		}
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		switch strings.TrimSpace(key) {
		case "gg_verification_steps":
			if foundSteps {
				return state.VerificationContract{}, errors.New("verification artifact repeats gg_verification_steps")
			}
			foundSteps, stepsValue = true, strings.TrimSpace(value)
		case "gg_repair_mode":
			if foundRepair {
				return state.VerificationContract{}, errors.New("verification artifact repeats gg_repair_mode")
			}
			foundRepair, repairValue = true, strings.TrimSpace(value)
		}
	}
	if !closed {
		return state.VerificationContract{}, errors.New("verification artifact has unterminated frontmatter")
	}
	if !foundSteps || stepsValue == "" {
		return state.VerificationContract{}, errors.New("verification artifact requires non-empty gg_verification_steps")
	}
	if !foundRepair || repairValue == "" {
		return state.VerificationContract{}, errors.New("verification artifact requires explicit gg_repair_mode")
	}
	var rawSteps []json.RawMessage
	decoder := json.NewDecoder(strings.NewReader(stepsValue))
	if err := decoder.Decode(&rawSteps); err != nil || len(rawSteps) == 0 {
		if err == nil {
			err = errors.New("array is empty")
		}
		return state.VerificationContract{}, fmt.Errorf("invalid gg_verification_steps: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return state.VerificationContract{}, errors.New("gg_verification_steps contains trailing JSON")
	}
	steps := make([]state.VerificationStep, 0, len(rawSteps))
	for index, raw := range rawSteps {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return state.VerificationContract{}, fmt.Errorf("verification step %d is not an object: %w", index, err)
		}
		for field := range fields {
			switch field {
			case "name", "command", "args", "env", "adapter":
			default:
				return state.VerificationContract{}, fmt.Errorf("verification step %d has unknown field %q", index, field)
			}
		}
		if _, ok := fields["args"]; !ok {
			return state.VerificationContract{}, fmt.Errorf("verification step %d requires an args array", index)
		}
		if trimmed := strings.TrimSpace(string(fields["args"])); len(trimmed) == 0 || trimmed[0] != '[' {
			return state.VerificationContract{}, fmt.Errorf("verification step %d args must be a JSON array", index)
		}
		if env, ok := fields["env"]; ok {
			trimmed := strings.TrimSpace(string(env))
			if trimmed == "" || trimmed[0] != '{' {
				return state.VerificationContract{}, fmt.Errorf("verification step %d env must be a JSON object", index)
			}
		}
		var step state.VerificationStep
		stepDecoder := json.NewDecoder(strings.NewReader(string(raw)))
		stepDecoder.DisallowUnknownFields()
		if err := stepDecoder.Decode(&step); err != nil {
			return state.VerificationContract{}, fmt.Errorf("invalid verification step %d: %w", index, err)
		}
		step.Name, step.Command = strings.TrimSpace(step.Name), strings.TrimSpace(step.Command)
		steps = append(steps, step)
	}
	if repairValue != "true" && repairValue != "false" {
		return state.VerificationContract{}, errors.New("gg_repair_mode must be an explicit boolean")
	}
	repairMode := repairValue == "true"
	contract := state.VerificationContract{Steps: steps, RepairMode: repairMode}
	if err := contract.Validate(); err != nil {
		return state.VerificationContract{}, err
	}
	return contract, nil
}

// ReadOpenQuestions extracts the optional `gg_open_questions` frontmatter
// array from a phase's canonical artifact: the precise questions a blocked
// phase needs answered. Parsing is tolerant — any problem yields nil.
func ReadOpenQuestions(root string, phase pipeline.PhaseID) []string {
	name, ok := pipeline.CanonicalArtifactName(phase)
	if !ok {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(filepath.Clean(root), name))
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if len(lines) < 2 || lines[0] != "---" {
		return nil
	}
	for _, line := range lines[1:] {
		if line == "---" {
			break
		}
		key, value, found := strings.Cut(line, ":")
		if found && strings.TrimSpace(key) == "gg_open_questions" {
			return parseStringArray(value)
		}
	}
	return nil
}

func parseStringArray(value string) []string {
	var raw []string
	if err := json.Unmarshal([]byte(strings.TrimSpace(value)), &raw); err != nil {
		return nil
	}
	entries := make([]string, 0, len(raw))
	for _, entry := range raw {
		if entry = strings.TrimSpace(entry); entry != "" {
			entries = append(entries, entry)
		}
	}
	return entries
}

// parseTokenUsage extracts the run's total token count from agent output.
// codex prints a "tokens used" marker followed by the count on stderr; claude
// with --output-format json reports a usage object on stdout. Usage display
// is best-effort: unknown formats yield zero.
func parseTokenUsage(agentName config.Agent, stdout, stderr string) int64 {
	switch agentName {
	case config.AgentCodex:
		return parseCodexTokens(stderr)
	case config.AgentClaude:
		return parseClaudeTokens(stdout)
	default:
		return 0
	}
}

func parseCodexTokens(stderr string) int64 {
	lines := strings.Split(stderr, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		rest, ok := strings.CutPrefix(strings.TrimSpace(lines[i]), "tokens used")
		if !ok {
			continue
		}
		candidates := append([]string{rest}, lines[i+1:]...)
		for _, candidate := range candidates {
			digits := strings.ReplaceAll(strings.Trim(strings.TrimSpace(candidate), ":"), ",", "")
			if digits == "" {
				continue
			}
			if value, err := strconv.ParseInt(strings.TrimSpace(digits), 10, 64); err == nil && value > 0 {
				return value
			}
			break
		}
	}
	return 0
}

// parseClaudeCost extracts the run's agent-reported USD cost from claude's
// JSON result envelope; zero when absent or unparsable.
func parseClaudeCost(stdout string) float64 {
	start := strings.Index(stdout, "{")
	end := strings.LastIndex(stdout, "}")
	if start < 0 || end <= start {
		return 0
	}
	var payload struct {
		TotalCostUSD float64 `json:"total_cost_usd"`
	}
	if err := json.Unmarshal([]byte(stdout[start:end+1]), &payload); err != nil || payload.TotalCostUSD < 0 {
		return 0
	}
	return payload.TotalCostUSD
}

func parseClaudeTokens(stdout string) int64 {
	start := strings.Index(stdout, "{")
	end := strings.LastIndex(stdout, "}")
	if start < 0 || end <= start {
		return 0
	}
	var payload struct {
		Usage struct {
			InputTokens              int64 `json:"input_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(stdout[start:end+1]), &payload); err != nil {
		return 0
	}
	usage := payload.Usage
	return usage.InputTokens + usage.CacheCreationInputTokens + usage.CacheReadInputTokens + usage.OutputTokens
}

// parseClaudeErrorResult extracts the human-readable message from claude's
// JSON result envelope when it reports a failed run on stdout (for example an
// unknown model or an authentication problem, where stderr stays empty).
// Output that is not such an envelope yields "".
func parseClaudeErrorResult(stdout string) string {
	start := strings.Index(stdout, "{")
	end := strings.LastIndex(stdout, "}")
	if start < 0 || end <= start {
		return ""
	}
	var payload struct {
		IsError bool   `json:"is_error"`
		Result  string `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout[start:end+1]), &payload); err != nil || !payload.IsError {
		return ""
	}
	return strings.TrimSpace(payload.Result)
}

// stderrTail returns the last non-empty stderr line from a phase log, reading
// only the final chunk so large logs stay cheap.
func stderrTail(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	const tailBytes = 16 * 1024
	info, err := file.Stat()
	if err != nil {
		return ""
	}
	offset := info.Size() - tailBytes
	if offset < 0 {
		offset = 0
	}
	buffer := make([]byte, info.Size()-offset)
	if _, err := file.ReadAt(buffer, offset); err != nil && len(buffer) > 0 {
		return ""
	}
	lines := strings.Split(string(buffer), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if rest, ok := strings.CutPrefix(lines[i], "[stderr] "); ok && strings.TrimSpace(rest) != "" {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

func readCanonicalDisposition(root string, phase pipeline.PhaseID, runID string) (Disposition, error) {
	name, ok := pipeline.CanonicalArtifactName(phase)
	if !ok {
		return "", fmt.Errorf("phase %q has no canonical artifact", phase)
	}
	root, err := cleanExistingDirectory(root)
	if err != nil {
		return "", fmt.Errorf("validate canonical artifact root: %w", err)
	}
	path := filepath.Join(root, name)
	if !isRegularArtifact(root, path) {
		return "", fmt.Errorf("canonical artifact %q is missing or is not a regular non-symlink file", name)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read canonical artifact %q: %w", name, err)
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if len(lines) < 4 || lines[0] != "---" {
		return "", fmt.Errorf("canonical artifact %q must begin with result frontmatter", name)
	}
	values := make(map[string]string, 2)
	closed := false
	for index := 1; index < len(lines); index++ {
		line := lines[index]
		if line == "---" {
			closed = true
			break
		}
		key, value, found := strings.Cut(line, ":")
		if !found {
			return "", fmt.Errorf("canonical artifact %q has malformed result frontmatter line %d", name, index+1)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key != "gg_run_id" && key != "gg_disposition" {
			continue
		}
		if _, duplicate := values[key]; duplicate {
			return "", fmt.Errorf("canonical artifact %q repeats frontmatter field %q", name, key)
		}
		if unquoted, unquoteErr := strconv.Unquote(value); unquoteErr == nil {
			value = unquoted
		}
		values[key] = value
	}
	if !closed {
		return "", fmt.Errorf("canonical artifact %q has unterminated result frontmatter", name)
	}
	if values["gg_run_id"] != runID {
		return "", fmt.Errorf("canonical artifact %q run ID %q does not match invocation %q", name, values["gg_run_id"], runID)
	}
	disposition := Disposition(values["gg_disposition"])
	if !disposition.IsValid() {
		return "", fmt.Errorf("canonical artifact %q has invalid disposition %q", name, disposition)
	}
	return disposition, nil
}

// ensureArtifactWorkspace prepares the worktree's ignored artifact directory:
// phase artifacts live under it so they never clutter the repository, commits,
// or pull requests. Legacy root-level artifacts are migrated in, which leaves
// deletions the next development auto-commit cleans out of the branch.
func ensureArtifactWorkspace(worktree string) error {
	worktree = strings.TrimSpace(worktree)
	if worktree == "" {
		return errors.New("worktree is required")
	}
	dir := filepath.Join(worktree, pipeline.ArtifactDirectory)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	ignore := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(ignore); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(ignore, []byte("*\n"), 0o644); err != nil {
			return err
		}
	}
	for _, name := range append(pipeline.CanonicalArtifactBaseNames(), "PROOF.md") {
		legacy := filepath.Join(worktree, name)
		target := filepath.Join(dir, name)
		if _, err := os.Stat(target); err == nil {
			continue
		}
		if info, err := os.Lstat(legacy); err != nil || !info.Mode().IsRegular() {
			continue
		}
		_ = os.Rename(legacy, target)
	}
	return nil
}

func discoverArtifacts(root string, phase pipeline.PhaseID, paths []string) []string {
	candidates := append([]string(nil), paths...)
	if canonical, ok := pipeline.CanonicalArtifactName(phase); ok {
		candidates = append(candidates, canonical)
	}
	if phase == pipeline.PhaseQA {
		candidates = append(candidates, proof.ArtifactName)
	}
	root, err := cleanExistingDirectory(root)
	if err != nil {
		return nil
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, p := range candidates {
		if strings.TrimSpace(p) == "" {
			continue
		}
		candidate := p
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(root, candidate)
		}
		candidate, err = filepath.Abs(candidate)
		if err != nil || !pathWithin(root, candidate) {
			continue
		}
		if !isRegularArtifact(root, candidate) {
			continue
		}
		resolved, err := filepath.EvalSymlinks(candidate)
		if err != nil || !pathWithin(resolvedRoot, resolved) {
			continue
		}
		relative, err := filepath.Rel(root, candidate)
		if err != nil {
			continue
		}
		relative = filepath.ToSlash(relative)
		if _, exists := seen[relative]; exists {
			continue
		}
		seen[relative] = struct{}{}
		out = append(out, relative)
	}
	return out
}

func isRegularArtifact(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false
	}
	current := root
	parts := strings.Split(relative, string(filepath.Separator))
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return false
		}
		if i < len(parts)-1 && !info.IsDir() {
			return false
		}
		if i == len(parts)-1 {
			return info.Mode().IsRegular()
		}
	}
	return false
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

type runEventSink struct {
	sink    EventSink
	request RunRequest
}

func (s *runEventSink) Publish(ctx context.Context, e Event) error {
	e.ProjectSlug, e.Phase, e.Subphase = s.request.Project.Slug, s.request.Phase, s.request.Subphase
	return s.sink.Publish(ctx, e)
}

// startedEventGate prevents a fast process from publishing output before the
// runner has announced that the process started. The context fallback avoids
// leaking a factory's output goroutine if startup is canceled or fails.
type startedEventGate struct {
	ctx     context.Context
	sink    EventSink
	started chan struct{}
	once    sync.Once
}

func newStartedEventGate(ctx context.Context, sink EventSink) *startedEventGate {
	return &startedEventGate{ctx: ctx, sink: sink, started: make(chan struct{})}
}

func (g *startedEventGate) Publish(ctx context.Context, e Event) error {
	if e.Type == EventOutput {
		select {
		case <-g.started:
		case <-g.ctx.Done():
			return g.ctx.Err()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return g.sink.Publish(ctx, e)
}

func (g *startedEventGate) release() {
	g.once.Do(func() { close(g.started) })
}

type PipelineRunRequest struct {
	Project         state.ProjectState
	Pipeline        pipeline.ExecutablePipeline
	DefaultSettings config.AgentSettings
	PhaseContracts  map[pipeline.PhaseID]string
	ArtifactPaths   []string
	Subphases       pipeline.DevelopmentSubphaseGeneration
	RunID           string
}

func (r *AgentRunner) RunPipeline(ctx context.Context, req PipelineRunRequest) ([]RunResult, error) {
	var results []RunResult
	artifacts := append([]string(nil), req.ArtifactPaths...)
	for _, ep := range req.Pipeline.Phases() {
		phase := ep.Phase().ID()
		settings, ok := ep.Settings()
		if !ok {
			settings = req.DefaultSettings
		}
		subs := []string{""}
		if phase == pipeline.PhaseDevelopment {
			generated, err := pipeline.GenerateDevelopmentSubphases(req.Subphases)
			if err != nil {
				return results, err
			}
			subs = subs[:0]
			for _, sub := range generated {
				subs = append(subs, string(sub.ID()))
			}
		}
		for _, sub := range subs {
			invocationID := req.RunID + "/" + string(phase) + "/" + sub
			prompt, err := r.prompt.BuildPrompt(PromptInput{Project: req.Project, Phase: phase, Subphase: sub, PhaseContract: req.PhaseContracts[phase], ArtifactPaths: artifacts, WorkingDirectory: req.Project.WorktreePath, RunID: invocationID, Development: phase == pipeline.PhaseDevelopment})
			if err != nil {
				return results, err
			}
			one := RunRequest{Project: req.Project, Phase: phase, Subphase: sub, Settings: settings, Prompt: prompt, WorkingDirectory: req.Project.WorktreePath, ArtifactPaths: artifacts, RunID: invocationID}
			result, err := r.Run(ctx, one)
			results = append(results, result)
			artifacts = appendUniqueArtifacts(artifacts, result.ArtifactPaths...)
			if err != nil {
				return results, err
			}
		}
	}
	return results, nil
}

func appendUniqueArtifacts(existing []string, additions ...string) []string {
	result := append([]string(nil), existing...)
	seen := make(map[string]struct{}, len(result))
	for _, path := range result {
		seen[path] = struct{}{}
	}
	for _, path := range additions {
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		result = append(result, path)
	}
	return result
}

var _ Runner = (*AgentRunner)(nil)

func appendUniquePath(paths []string, path string) []string {
	for _, existing := range paths {
		if existing == path {
			return paths
		}
	}
	return append(paths, path)
}
