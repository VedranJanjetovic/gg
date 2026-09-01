package verification

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/state"
)

func TestNormalizeReasonRemovesVolatileDetailsButPreservesMeaning(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "volatile details", in: "at /tmp/run-123/main.go:12 seed=991 addr=0xabc duration 12.5ms", want: "at <temp-path>:12 seed=<seed> addr=<address> duration <duration>"},
		{name: "semantic text", in: "undefined: customer", want: "undefined: customer"},
		{name: "line whitespace", in: " first   reason \n second reason ", want: "first reason\nsecond reason"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := NormalizeReason(test.in); got != test.want {
				t.Fatalf("NormalizeReason() = %q, want %q", got, test.want)
			}
		})
	}
	if NormalizeReason("undefined: customer") == NormalizeReason("undefined: invoice") {
		t.Fatal("semantic reason change was normalized away")
	}
}

func TestParseOutputAdaptersExposeStableFailures(t *testing.T) {
	tests := []struct {
		name     string
		adapter  Adapter
		stdout   string
		stderr   string
		exitCode int
		identity string
		reason   string
	}{
		{name: "go test", adapter: AdapterGoTest, stdout: "--- FAIL: TestParse (0.01s)\n    test.go:12: undefined: customer\nFAIL\n", exitCode: 1, identity: "TestParse", reason: "test.go:12: undefined: customer"},
		{name: "go test subtest with spaces", adapter: AdapterGoTest, stdout: "--- FAIL: TestParse/invalid customer (0.01s)\nFAIL\n", exitCode: 1, identity: "TestParse/invalid customer", reason: "--- FAIL: TestParse/invalid customer (<duration>)"},
		{name: "diagnostic", adapter: AdapterGoDiagnostic, stderr: "main.go:12:4: undefined: customer\n", exitCode: 1, identity: "main.go:12:4", reason: "undefined: customer"},
		{name: "gofmt", adapter: AdapterGofmtEmpty, stdout: "internal/a.go\n", identity: "format:internal/a.go", reason: "file requires gofmt"},
		{name: "diff", adapter: AdapterGitDiffCheck, stdout: "main.go:4: trailing whitespace.\n", identity: "diff:main.go:4", reason: "trailing whitespace."},
		{name: "diff with column", adapter: AdapterGitDiffCheck, stdout: "main.go:4:7: trailing whitespace.\n", exitCode: 1, identity: "diff:main.go:4:7", reason: "trailing whitespace."},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			failures, classifiable, err := ParseOutput(test.adapter, test.stdout, test.stderr, test.exitCode)
			if err != nil {
				t.Fatal(err)
			}
			if !classifiable || len(failures) != 1 {
				t.Fatalf("failures=%#v classifiable=%v", failures, classifiable)
			}
			if failures[0].Identity != test.identity || failures[0].Reason != test.reason {
				t.Fatalf("failure=%#v, want identity=%q reason=%q", failures[0], test.identity, test.reason)
			}
		})
	}
}

func TestParseOutputUsesStrictFallbackForUnidentifiedFailures(t *testing.T) {
	failures, classifiable, err := ParseOutput(AdapterGoTest, "go test failed unexpectedly\n", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if classifiable || len(failures) != 0 {
		t.Fatalf("ParseOutput() = failures=%#v classifiable=%v, want strict fallback", failures, classifiable)
	}
}

func TestParseGoTestFailureWithoutDetail(t *testing.T) {
	failures, classifiable, err := ParseOutput(AdapterGoTest, "--- FAIL: TestBroken (2ms)\nFAIL\n", "", 1)
	if err != nil || !classifiable || len(failures) != 1 || failures[0].Identity != "TestBroken" {
		t.Fatalf("parse=%#v classifiable=%v err=%v", failures, classifiable, err)
	}
}

func TestCompareClassifiesBaselineTransitions(t *testing.T) {
	base := CaptureBaseline(Report{Results: []CommandResult{{StepName: "tests", Status: CommandFailed, Failures: []IndividualFailure{{Identity: "TestBroken", Reason: "undefined: customer"}}}}})
	key := FailureKey{CheckName: "tests", Identity: "TestBroken"}
	tests := []struct {
		name       string
		current    Report
		options    CompareOptions
		passed     bool
		class      Classification
		warnings   int
		promotions int
	}{
		{name: "unchanged warning", current: Report{Results: []CommandResult{{StepName: "tests", Status: CommandFailed, Failures: []IndividualFailure{{Identity: "TestBroken", Reason: "undefined: customer"}}}}}, passed: true, class: ClassificationUnchangedBaseline, warnings: 1},
		{name: "new failure", current: Report{Results: []CommandResult{{StepName: "tests", Status: CommandFailed, Failures: []IndividualFailure{{Identity: "TestNew", Reason: "panic"}}}}}, passed: false, promotions: 1, class: ClassificationNew},
		{name: "changed reason", current: Report{Results: []CommandResult{{StepName: "tests", Status: CommandFailed, Failures: []IndividualFailure{{Identity: "TestBroken", Reason: "undefined: invoice"}}}}}, passed: false, class: ClassificationChangedReason},
		{name: "repair target remains", current: Report{Results: []CommandResult{{StepName: "tests", Status: CommandFailed, Failures: []IndividualFailure{{Identity: "TestBroken", Reason: "undefined: customer"}}}}}, options: CompareOptions{RepairMode: true, RepairTargets: []FailureKey{key}}, passed: false, class: ClassificationUnchangedBaseline},
		{name: "repaired", current: Report{Results: []CommandResult{{StepName: "tests", Status: CommandPassed}}}, passed: true, promotions: 1},
		{name: "repaired regressed", current: Report{Results: []CommandResult{{StepName: "tests", Status: CommandFailed, Failures: []IndividualFailure{{Identity: "TestBroken", Reason: "undefined: customer"}}}}}, options: CompareOptions{Promoted: []FailureKey{key}}, passed: false, class: ClassificationRepairedRegressed},
		{name: "unclassifiable", current: Report{Results: []CommandResult{{StepName: "tests", Status: CommandUnclassifiable}}}, passed: false, class: ClassificationUnclassifiable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := Compare(base, test.current, test.options)
			if got.Passed != test.passed || len(got.Warnings) != test.warnings || len(got.Promotions) != test.promotions {
				t.Fatalf("report=%#v", got)
			}
			if test.class != "" && (len(got.Findings) == 0 || got.Findings[0].Classification != test.class) {
				t.Fatalf("findings=%#v, want first classification %q", got.Findings, test.class)
			}
		})
	}
}

func TestCompareNormalizesReasonsBeforeComparison(t *testing.T) {
	base := CaptureBaseline(Report{Results: []CommandResult{{StepName: "diagnostic", Status: CommandFailed, Failures: []IndividualFailure{{Identity: "file.go:4", Reason: "at /tmp/one 10ms"}}}}})
	current := Report{Results: []CommandResult{{StepName: "diagnostic", Status: CommandFailed, Failures: []IndividualFailure{{Identity: "file.go:4", Reason: "at /tmp/two 20ms"}}}}}
	got := Compare(base, current, CompareOptions{})
	if !got.Passed || len(got.Warnings) != 1 {
		t.Fatalf("normalized equivalent failure = %#v", got)
	}
}

func TestCompareDoesNotTreatMissingOrUnclassifiableResultsAsRepairs(t *testing.T) {
	baseline := CaptureBaseline(Report{Results: []CommandResult{{StepName: "tests", Status: CommandFailed, Failures: []IndividualFailure{{Identity: "TestBroken", Reason: "panic"}}}}})
	for _, current := range []Report{
		{},
		{Results: []CommandResult{{StepName: "tests", Status: CommandFailed}}},
		{Results: []CommandResult{{StepName: "tests", Status: CommandUnclassifiable}}},
	} {
		got := Compare(baseline, current, CompareOptions{})
		if got.Passed || !got.Blocked || len(got.Promotions) != 0 || len(got.Findings) == 0 {
			t.Fatalf("Compare(%#v) = %#v, want strict blocked result without promotion", current, got)
		}
	}
}

func TestCompareKeepsFlakyBaselineFailureAsWarningInsteadOfRepair(t *testing.T) {
	key := FailureKey{CheckName: "tests", Identity: "TestBroken"}
	baseline := CaptureBaseline(Report{Results: []CommandResult{{StepName: "tests", Status: CommandFailed, Failures: []IndividualFailure{{Identity: key.Identity, Reason: "panic"}}}}})
	current := Report{Results: []CommandResult{{StepName: "tests", Status: CommandPassed}}, Warnings: []Warning{{Key: key, Reason: "panic", Classification: ClassificationFlaky}}}
	got := Compare(baseline, current, CompareOptions{})
	if !got.Passed || got.Blocked || len(got.Promotions) != 0 || len(got.Warnings) != 1 || got.Warnings[0].Classification != ClassificationFlaky {
		t.Fatalf("flaky comparison = %#v, want warning without repair promotion", got)
	}
}

func TestCompareDoesNotLetChangedFlakyReasonBypassRegression(t *testing.T) {
	key := FailureKey{CheckName: "tests", Identity: "TestBroken"}
	baseline := CaptureBaseline(Report{Results: []CommandResult{{StepName: key.CheckName, Status: CommandFailed, Failures: []IndividualFailure{{Identity: key.Identity, Reason: "panic"}}}}})
	current := Report{
		Results:  []CommandResult{{StepName: key.CheckName, Status: CommandFailed, Failures: []IndividualFailure{{Identity: key.Identity, Reason: "undefined: customer"}}}},
		Warnings: []Warning{{Key: key, Reason: "panic", Classification: ClassificationFlaky}},
	}
	got := Compare(baseline, current, CompareOptions{})
	if got.Passed || len(got.Findings) == 0 || got.Findings[0].Classification != ClassificationChangedReason {
		t.Fatalf("changed flaky reason = %#v, want blocking changed-reason finding", got)
	}
}

func TestCompareFailsClosedForUnknownCommandStatus(t *testing.T) {
	key := FailureKey{CheckName: "tests", Identity: "TestBroken"}
	baseline := CaptureBaseline(Report{Results: []CommandResult{{StepName: key.CheckName, Status: CommandFailed, Failures: []IndividualFailure{{Identity: key.Identity, Reason: "panic"}}}}})
	got := Compare(baseline, Report{Results: []CommandResult{{StepName: key.CheckName}}}, CompareOptions{})
	if got.Passed || !got.Blocked || len(got.Promotions) != 0 || len(got.Findings) == 0 || got.Findings[0].Classification != ClassificationUnclassifiable {
		t.Fatalf("unknown status comparison = %#v, want strict fallback", got)
	}
}

func TestCompareReportsRepairedPromotion(t *testing.T) {
	key := FailureKey{CheckName: "tests", Identity: "TestBroken"}
	baseline := CaptureBaseline(Report{Results: []CommandResult{{StepName: "tests", Status: CommandFailed, Failures: []IndividualFailure{{Identity: key.Identity, Reason: "panic"}}}}})
	got := Compare(baseline, Report{Results: []CommandResult{{StepName: "tests", Status: CommandPassed}}}, CompareOptions{})
	if !got.Passed || len(got.Promotions) != 1 || len(got.Findings) != 1 || got.Findings[0].Classification != ClassificationRepaired || !got.Findings[0].RequiredGreen {
		t.Fatalf("repair comparison = %#v, want required-green promotion", got)
	}
}

type fakeExecutor struct {
	calls []fakeCall
	queue []fakeExecution
}

type fakeCall struct {
	worktree string
	command  string
	args     []string
	env      []string
}

type fakeExecution struct {
	stdout, stderr string
	exitCode       int
	err            error
}

func (f *fakeExecutor) Execute(_ context.Context, worktree, command string, args []string, env []string) (string, string, int, error) {
	f.calls = append(f.calls, fakeCall{worktree: worktree, command: command, args: append([]string(nil), args...), env: append([]string(nil), env...)})
	if len(f.queue) == 0 {
		return "", "", 0, nil
	}
	next := f.queue[0]
	f.queue = f.queue[1:]
	return next.stdout, next.stderr, next.exitCode, next.err
}

func TestRunnerUsesDirectArgumentsFixedEnvironmentAndBoundedLogs(t *testing.T) {
	root := t.TempDir()
	fake := &fakeExecutor{queue: []fakeExecution{{stdout: strings.Repeat("x", 100), exitCode: 0}}}
	runner := NewRunner(RunnerOptions{Executor: fake, Environment: map[string]string{"GG_FIXED": "yes"}, LogDirectory: filepath.Join(root, "logs"), MaxOutputBytes: 24})
	report, err := runner.Run(context.Background(), root, []Step{{Name: "format", Command: "gofmt", Args: []string{"-l", "."}, Env: map[string]string{"GG_STEP": "yes"}, Adapter: AdapterGofmtEmpty}})
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) != 1 || fake.calls[0].command != "gofmt" || strings.Join(fake.calls[0].args, " ") != "-l ." {
		t.Fatalf("calls=%#v", fake.calls)
	}
	env := strings.Join(fake.calls[0].env, "\n")
	if !strings.Contains(env, "GG_FIXED=yes") || !strings.Contains(env, "GG_STEP=yes") {
		t.Fatalf("environment overlay missing: %s", env)
	}
	data, readErr := os.ReadFile(report.Results[0].LogPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(data) > 24 {
		t.Fatalf("log length=%d, want at most 24", len(data))
	}
}

func TestRunnerRetriesExactlyOnceAndRecordsFlakyWarning(t *testing.T) {
	root := t.TempDir()
	fake := &fakeExecutor{queue: []fakeExecution{
		{stderr: "file.go:4: undefined: customer\n", exitCode: 1},
		{exitCode: 0},
	}}
	runner := NewRunner(RunnerOptions{Executor: fake, LogDirectory: filepath.Join(root, "logs")})
	report, err := runner.Verify(context.Background(), root, []Step{{Name: "vet", Command: "go", Args: []string{"vet", "./..."}, Adapter: AdapterGoDiagnostic}})
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) != 2 || report.Results[0].RetryCount != 1 || report.Results[0].Status != CommandPassed {
		t.Fatalf("calls=%#v report=%#v", fake.calls, report)
	}
	if len(report.Warnings) != 1 || report.Warnings[0].Classification != ClassificationFlaky {
		t.Fatalf("warnings=%#v", report.Warnings)
	}
}

func TestRunnerRetriesARepeatedFailureOnce(t *testing.T) {
	root := t.TempDir()
	fake := &fakeExecutor{queue: []fakeExecution{
		{stdout: "--- FAIL: TestBroken (1ms)\nFAIL\n", exitCode: 1},
		{stdout: "--- FAIL: TestBroken (2ms)\nFAIL\n", exitCode: 1},
	}}
	runner := NewRunner(RunnerOptions{Executor: fake, LogDirectory: filepath.Join(root, "logs")})
	report, err := runner.Verify(context.Background(), root, []Step{{Name: "tests", Command: "go", Args: []string{"test", "./..."}, Adapter: AdapterGoTest}})
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) != 2 || report.Results[0].RetryCount != 1 || report.Results[0].Status != CommandFailed || len(report.Warnings) != 0 {
		t.Fatalf("calls=%#v report=%#v", fake.calls, report)
	}
	if len(fake.calls[1].args) != 4 || fake.calls[1].args[1] != "-run" || !strings.Contains(fake.calls[1].args[2], "TestBroken") {
		t.Fatalf("retry args=%#v, want one narrowed go test retry", fake.calls[1].args)
	}
}

func TestRunnerConfirmsEachFailedGoTestAndPreservesThePersistentFailure(t *testing.T) {
	root := t.TempDir()
	fake := &fakeExecutor{queue: []fakeExecution{
		{stdout: "--- FAIL: TestFlaky (1ms)\n--- FAIL: TestBroken (1ms)\nFAIL\n", exitCode: 1},
		{stdout: "--- FAIL: TestBroken (2ms)\nFAIL\n", exitCode: 1},
		{exitCode: 0},
	}}
	runner := NewRunner(RunnerOptions{Executor: fake, LogDirectory: filepath.Join(root, "logs")})
	report, err := runner.Verify(context.Background(), root, []Step{{Name: "tests", Command: "go", Args: []string{"test", "./..."}, Adapter: AdapterGoTest}})
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) != 3 || report.Results[0].RetryCount != 2 || report.Results[0].Status != CommandFailed {
		t.Fatalf("calls=%#v report=%#v, want one confirmation per failed test", fake.calls, report)
	}
	if len(report.Results[0].Failures) != 1 || report.Results[0].Failures[0].Identity != "TestBroken" {
		t.Fatalf("persistent failures=%#v, want TestBroken only", report.Results[0].Failures)
	}
	if len(report.Warnings) != 1 || report.Warnings[0].Key.Identity != "TestFlaky" {
		t.Fatalf("flaky warnings=%#v, want TestFlaky only", report.Warnings)
	}
	if _, err := os.Stat(filepath.Join(root, "logs", "00-tests.log")); err != nil {
		t.Fatalf("initial log missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "logs", "00-tests-retry-1.log")); err != nil {
		t.Fatalf("first retry log missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "logs", "00-tests-retry-2.log")); err != nil {
		t.Fatalf("second retry log missing: %v", err)
	}
}

func TestRetryStepReplacesExistingRunFilter(t *testing.T) {
	step := Step{Command: "go", Adapter: AdapterGoTest, Args: []string{"test", "./...", "-run", "TestOld"}}
	got := retryStep(step, IndividualFailure{Identity: "pkg:TestNew"})
	// The retry must not append a second competing filter; the existing
	// filter is replaced in place, regardless of where the package argument
	// appears.
	if strings.Join(got.Args, " ") != "test ./... -run ^TestNew$" {
		t.Fatalf("retry args=%v, want existing -run filter replaced", got.Args)
	}
}

func TestRunnerReturnsExplicitUnavailableError(t *testing.T) {
	root := t.TempDir()
	fake := &fakeExecutor{queue: []fakeExecution{{err: errors.New("not installed")}}}
	runner := NewRunner(RunnerOptions{Executor: fake, LogDirectory: filepath.Join(root, "logs")})
	report, err := runner.Run(context.Background(), root, []Step{{Name: "tests", Command: "missing-tool", Adapter: AdapterGoTest}})
	if err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("Run() error=%v, want explicit unavailable error", err)
	}
	if len(report.Results) != 1 || report.Results[0].Status != CommandUnavailable {
		t.Fatalf("report=%#v", report)
	}
}

func TestLookPathUsesEnvironmentOverlay(t *testing.T) {
	goPath, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go is unavailable in the test environment")
	}
	pathValue := filepath.Dir(goPath)
	resolved, err := lookPathWithEnvironment("go", []string{"PATH=" + pathValue})
	if err != nil {
		t.Fatalf("lookPathWithEnvironment() error = %v", err)
	}
	if filepath.Dir(resolved) != pathValue {
		t.Fatalf("resolved path=%q, want executable from overlay %q", resolved, pathValue)
	}
}

func TestStepsFromStateCopiesMutableContractData(t *testing.T) {
	original := []state.VerificationStep{{Name: "tests", Command: "go", Args: []string{"test"}, Env: map[string]string{"GO": "1.22"}, Adapter: state.VerificationAdapterGoTest}}
	converted := StepsFromState(original)
	converted[0].Args[0] = "vet"
	converted[0].Env["GO"] = "1.23"
	if original[0].Args[0] != "test" || original[0].Env["GO"] != "1.22" {
		t.Fatalf("conversion aliased input: %#v", original)
	}
}

func TestCompareRecordsQuarantinedUnclassifiableCheckAsWarningInsteadOfBlocking(t *testing.T) {
	base := CaptureBaseline(Report{Results: []CommandResult{{StepName: "affected-unit-tests", Status: CommandPassed}}})
	current := Report{Results: []CommandResult{{StepName: "affected-unit-tests", Status: CommandUnclassifiable, LogPath: "/logs/unit"}}}
	got := Compare(base, current, CompareOptions{Quarantined: []string{"affected-unit-tests"}})
	if got.Blocked || !got.Passed || len(got.Findings) != 0 {
		t.Fatalf("quarantined unclassifiable check = %#v", got)
	}
	if len(got.Warnings) != 1 || got.Warnings[0].Classification != ClassificationUnclassifiable || got.Warnings[0].Key.CheckName != "affected-unit-tests" {
		t.Fatalf("warnings=%#v, want one unclassifiable warning naming the quarantined check", got.Warnings)
	}
}

func TestCompareStillBlocksAnUnclassifiableCheckThatIsNotQuarantined(t *testing.T) {
	base := CaptureBaseline(Report{Results: []CommandResult{{StepName: "affected-unit-tests", Status: CommandPassed}, {StepName: "affected-race-tests", Status: CommandPassed}}})
	current := Report{Results: []CommandResult{{StepName: "affected-unit-tests", Status: CommandUnclassifiable}, {StepName: "affected-race-tests", Status: CommandUnclassifiable}}}
	got := Compare(base, current, CompareOptions{Quarantined: []string{"affected-unit-tests"}})
	if !got.Blocked || got.Passed {
		t.Fatalf("non-quarantined unclassifiable check = %#v", got)
	}
	if len(got.Findings) != 1 || got.Findings[0].Key.CheckName != "affected-race-tests" {
		t.Fatalf("findings=%#v, want only the non-quarantined check to block", got.Findings)
	}
}

func TestCompareNeverClassifiesQuarantinedCheckFailuresAsNew(t *testing.T) {
	base := CaptureBaseline(Report{Results: []CommandResult{{StepName: "affected-unit-tests", Status: CommandPassed}}})
	current := Report{Results: []CommandResult{{StepName: "affected-unit-tests", Status: CommandFailed, Failures: []IndividualFailure{{Identity: "TestNew", Reason: "panic"}}}}}
	got := Compare(base, current, CompareOptions{Quarantined: []string{"affected-unit-tests"}})
	if got.Blocked || !got.Passed || len(got.Findings) != 0 {
		t.Fatalf("quarantined failure = %#v", got)
	}
	for _, warning := range got.Warnings {
		if warning.Classification == ClassificationNew {
			t.Fatalf("warnings=%#v, want no new classification for a quarantined check", got.Warnings)
		}
	}
	if len(got.Warnings) != 1 || got.Warnings[0].Key.Identity != "TestNew" {
		t.Fatalf("warnings=%#v, want the quarantined failure recorded once", got.Warnings)
	}
}

func TestCompareDoesNotPromoteBaselineFailuresOfAQuarantinedCheck(t *testing.T) {
	base := CaptureBaseline(Report{Results: []CommandResult{{StepName: "affected-unit-tests", Status: CommandFailed, Failures: []IndividualFailure{{Identity: "TestBroken", Reason: "undefined: customer"}}}}})
	current := Report{Results: []CommandResult{{StepName: "affected-unit-tests", Status: CommandUnclassifiable}}}
	got := Compare(base, current, CompareOptions{Quarantined: []string{"affected-unit-tests"}})
	if got.Blocked || len(got.Promotions) != 0 {
		t.Fatalf("quarantined check promoted a repair: %#v", got)
	}
}
