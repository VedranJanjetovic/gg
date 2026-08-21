package agent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/VedranJanjetovic/gg/internal/config"
	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/state"
)

type runnerEvents struct {
	mu     sync.Mutex
	events []Event
}

func (s *runnerEvents) Publish(_ context.Context, e Event) error {
	e.Payload = append([]byte(nil), e.Payload...)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
	return nil
}

type runnerClock struct{ now time.Time }

func (c runnerClock) Now() time.Time { return c.now }

func runnerProject(root string) state.ProjectState {
	now := time.Now().UTC()
	return state.ProjectState{SchemaVersion: state.CurrentSchemaVersion, Name: "Runner", Slug: "runner", OriginalGoal: "run fake executable", AcceptanceCriteria: []string{"artifact exists"}, PipelineConfig: state.PipelineConfigSnapshot{SchemaVersion: 1, Data: []byte(`{}`)}, CurrentPhase: "development", Status: state.StatusRunning, WorktreePath: root, BranchName: "runner", CreatedAt: now, UpdatedAt: now, StatusChangedAt: now}
}
func fakeRunner(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-agent")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}
func runnerSettings() config.AgentSettings {
	return config.AgentSettings{Agent: config.AgentClaude, Model: "fake", Effort: config.EffortLow}
}
func runnerRequest(project state.ProjectState, path string, prompt string) RunRequest {
	return RunRequest{Project: project, Phase: pipeline.PhaseDevelopment, Subphase: "implementation", Settings: runnerSettings(), Prompt: prompt, WorkingDirectory: path, ArtifactPaths: []string{"ARTIFACT.md"}, RunID: "runner-test"}
}

func TestAgentRunnerEndToEndSuccessPersistsPromptLogsArtifactsAndReloadsState(t *testing.T) {
	root := t.TempDir()
	script := fakeRunner(t, `printf 'prompt=%s\n' "$9"; printf 'stderr-output\n' >&2; printf 'artifact\n' > ARTIFACT.md
printf '%s\n' '---' 'gg_run_id: "runner-test"' 'gg_disposition: passed' '---' 'development evidence' > .gg/development.md`)
	store, err := state.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	project := runnerProject(root)
	if err := store.Save(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	lifecycle := state.NewLifecycleService(store, runnerClock{now: time.Now().UTC()}, store.Locker())
	events := &runnerEvents{}
	runner := NewAgentRunner(AgentRunnerOptions{Factory: NewExecProcessFactory(nil, nil), Lookup: func(string) (string, error) { return script, nil }, Events: events, Recorder: lifecycle})
	result, err := runner.Run(context.Background(), runnerRequest(project, root, "standalone prompt with spaces"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != state.StatusFinished || result.ExitCode != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(result.ArtifactPaths) != 2 || len(result.LogPaths) != 2 {
		t.Fatalf("artifacts=%v", result.ArtifactPaths)
	}
	logPath := filepath.Join(root, ".gg", "projects", project.Slug, "logs", "development-implementation.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "prompt=standalone prompt with spaces") || !strings.Contains(string(data), "stderr-output") {
		t.Fatalf("log=%q", data)
	}
	reloaded, err := store.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	out := reloaded.PhaseHistory[len(reloaded.PhaseHistory)-1].Outcome
	if out == nil || out.ExitCode != 0 || out.LogPath != logPath {
		t.Fatalf("reloaded outcome=%+v", out)
	}
	if len(reloaded.ArtifactPaths) != 2 || reloaded.ArtifactPaths[0] != "ARTIFACT.md" || reloaded.ArtifactPaths[1] != ".gg/development.md" {
		t.Fatalf("runner-owned paths leaked into artifacts: %v", reloaded.ArtifactPaths)
	}
	if len(events.events) < 3 || events.events[0].Type != EventStarted || events.events[len(events.events)-1].Type != EventCompleted {
		t.Fatalf("events=%+v", events.events)
	}
}

func TestAgentRunnerEndToEndFailureRecordsOutcome(t *testing.T) {
	root := t.TempDir()
	script := fakeRunner(t, `printf 'failure-output\n'; exit 9`)
	project := runnerProject(root)
	events := &runnerEvents{}
	runner := NewAgentRunner(AgentRunnerOptions{Factory: NewExecProcessFactory(nil, nil), Lookup: func(string) (string, error) { return script, nil }, Events: events})
	result, err := runner.Run(context.Background(), runnerRequest(project, root, "failure prompt"))
	if err == nil || result.Status != state.StatusFailed || result.ExitCode != 9 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if events.events[len(events.events)-1].Type != EventFailed {
		t.Fatalf("events=%+v", events.events)
	}
}

func TestAgentRunnerCancelStopsActiveProcessAndRegistry(t *testing.T) {
	root := t.TempDir()
	script := fakeRunner(t, `printf 'started\n'; trap 'exit 143' TERM; while true; do sleep 1; done`)
	project := runnerProject(root)
	registry := NewStopRegistry()
	runner := NewAgentRunner(AgentRunnerOptions{Factory: NewExecProcessFactory(nil, nil), Lookup: func(string) (string, error) { return script, nil }, Registry: registry})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := runner.Run(ctx, runnerRequest(project, root, "cancel prompt")); done <- err }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel error=%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("runner did not stop")
	}
	if registry.Active("runner-test") {
		t.Fatal("registry retained canceled process")
	}
}

func TestAgentRunnerUsesDurableLogRootOutsideExecutionWorktree(t *testing.T) {
	stateRoot := t.TempDir()
	worktree := t.TempDir()
	runRunnerGit(t, worktree, "init", "-q")
	runRunnerGit(t, worktree, "config", "user.email", "gg@example.test")
	runRunnerGit(t, worktree, "config", "user.name", "gg test")
	if err := os.WriteFile(filepath.Join(worktree, "tracked.txt"), []byte("tracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(worktree, ".gg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".gg", "development.md"), []byte("---\ngg_run_id: \"runner-test\"\ngg_disposition: passed\n---\ndevelopment evidence\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runRunnerGit(t, worktree, "add", "tracked.txt", ".gg/development.md")
	runRunnerGit(t, worktree, "-c", "commit.gpgsign=false", "commit", "-qm", "initial")
	script := fakeRunner(t, `printf 'explicit-cwd\n'
printf '%s\n' '---' 'gg_run_id: "runner-test"' 'gg_disposition: passed' '---' 'development evidence' > .gg/development.md`)
	project := runnerProject(stateRoot)
	runner := NewAgentRunner(AgentRunnerOptions{
		Factory: NewExecProcessFactory(nil, nil),
		Lookup:  func(string) (string, error) { return script, nil },
		LogRoot: stateRoot,
	})
	result, err := runner.Run(context.Background(), runnerRequest(project, worktree, "explicit directory prompt"))
	if err != nil {
		t.Fatal(err)
	}
	wantLog := filepath.Join(stateRoot, ".gg", "projects", project.Slug, "logs", "development-implementation.log")
	if len(result.LogPaths) != 2 || result.LogPaths[0] != wantLog {
		t.Fatalf("log paths = %v, want durable log root", result.LogPaths)
	}
	if _, err := os.Stat(wantLog); err != nil {
		t.Fatalf("durable log missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(worktree, ".gg", "projects")); !os.IsNotExist(err) {
		t.Fatalf("runner metadata dirtied execution worktree: %v", err)
	}
	if status := runRunnerGit(t, worktree, "status", "--porcelain"); status != "" {
		t.Fatalf("successful runner left worktree dirty: %q", status)
	}
}

func TestAgentRunnerDiscoversCanonicalArtifactAndRejectsEscapingPaths(t *testing.T) {
	worktree := t.TempDir()
	logRoot := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(worktree, "escape.md")); err != nil {
		t.Fatal(err)
	}
	script := fakeRunner(t, `printf '%s\n' '---' 'gg_run_id: "runner-test"' 'gg_disposition: passed' '---' 'qa evidence' > .gg/qa-report.md`)
	project := runnerProject(worktree)
	req := runnerRequest(project, worktree, "qa prompt")
	req.Phase = pipeline.PhaseQA
	req.Subphase = ""
	req.ArtifactPaths = []string{outside, "../outside.md", "escape.md"}
	runner := NewAgentRunner(AgentRunnerOptions{
		Factory: NewExecProcessFactory(nil, nil),
		Lookup:  func(string) (string, error) { return script, nil },
		LogRoot: logRoot,
	})
	result, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if got := result.ArtifactPaths; len(got) != 1 || got[0] != ".gg/qa-report.md" {
		t.Fatalf("discovered artifacts = %v, want contained canonical output only", got)
	}
}

func TestAgentRunnerRejectsContainedSymlinkAndNonRegularArtifacts(t *testing.T) {
	worktree := t.TempDir()
	logRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(worktree, "actual.md"), []byte("actual\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("actual.md", filepath.Join(worktree, "inside-link.md")); err != nil {
		t.Skipf("create contained artifact symlink: %v", err)
	}
	if err := os.Mkdir(filepath.Join(worktree, "directory.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := fakeRunner(t, `printf 'complete\n'
printf '%s\n' '---' 'gg_run_id: "runner-test"' 'gg_disposition: passed' '---' 'development evidence' > .gg/development.md`)
	project := runnerProject(worktree)
	req := runnerRequest(project, worktree, "artifact validation prompt")
	req.ArtifactPaths = []string{"inside-link.md", "directory.md"}
	runner := NewAgentRunner(AgentRunnerOptions{
		Factory: NewExecProcessFactory(nil, nil),
		Lookup:  func(string) (string, error) { return script, nil },
		LogRoot: logRoot,
	})
	result, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ArtifactPaths) != 1 || result.ArtifactPaths[0] != ".gg/development.md" {
		t.Fatalf("non-regular artifacts were accepted: %v", result.ArtifactPaths)
	}
}

type orderedOutputFactory struct {
	ready         chan struct{}
	exitCode      int
	waitForCancel bool
}

func (f *orderedOutputFactory) Start(ctx context.Context, spec ProcessSpec) (Process, error) {
	outputDone := make(chan struct{})
	close(f.ready)
	go func() {
		defer close(outputDone)
		_ = spec.Events.Publish(ctx, Event{Type: EventOutput, Stream: "stdout", Payload: []byte("fast stdout")})
		_ = spec.Events.Publish(ctx, Event{Type: EventOutput, Stream: "stderr", Payload: []byte("fast stderr")})
	}()
	return &orderedOutputProcess{ctx: ctx, outputDone: outputDone, exitCode: f.exitCode, waitForCancel: f.waitForCancel}, nil
}

type orderedOutputProcess struct {
	ctx           context.Context
	outputDone    <-chan struct{}
	exitCode      int
	waitForCancel bool
}

func (p *orderedOutputProcess) Wait() (ProcessResult, error) {
	<-p.outputDone
	if p.waitForCancel {
		<-p.ctx.Done()
		return ProcessResult{ExitCode: 1}, p.ctx.Err()
	}
	if p.exitCode != 0 {
		return ProcessResult{ExitCode: p.exitCode}, errors.New("fake process failed")
	}
	return ProcessResult{}, nil
}

func (p *orderedOutputProcess) Cancel() error { return nil }

func TestAgentRunnerStartedEventPrecedesFastOutputForSuccessFailureAndCancellation(t *testing.T) {
	tests := []struct {
		name          string
		exitCode      int
		waitForCancel bool
		cancel        bool
		wantTerminal  EventType
	}{
		{name: "success", wantTerminal: EventCompleted},
		{name: "failure", exitCode: 7, wantTerminal: EventFailed},
		{name: "cancellation", waitForCancel: true, cancel: true, wantTerminal: EventCanceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, ".gg"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, ".gg", "development.md"), []byte("---\ngg_run_id: \"runner-test\"\ngg_disposition: passed\n---\nevidence\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			project := runnerProject(root)
			events := &runnerEvents{}
			factory := &orderedOutputFactory{ready: make(chan struct{}), exitCode: tt.exitCode, waitForCancel: tt.waitForCancel}
			runner := NewAgentRunner(AgentRunnerOptions{Factory: factory, Events: events})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := runner.Run(ctx, runnerRequest(project, root, "ordering prompt"))
				done <- err
			}()
			<-factory.ready
			if tt.cancel {
				cancel()
			}
			err := <-done
			if tt.wantTerminal == EventCompleted && err != nil {
				t.Fatal(err)
			}
			if tt.cancel && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation error=%v", err)
			}

			if len(events.events) < 2 {
				t.Fatalf("events=%+v", events.events)
			}
			if events.events[0].Type != EventStarted {
				t.Fatalf("first event=%+v, want started", events.events[0])
			}
			for i, event := range events.events[1 : len(events.events)-1] {
				if event.Type != EventOutput {
					t.Fatalf("event %d=%+v, want output after started", i+1, event)
				}
			}
			if events.events[len(events.events)-1].Type != tt.wantTerminal {
				t.Fatalf("terminal event=%+v, want %s", events.events[len(events.events)-1], tt.wantTerminal)
			}
		})
	}
}

type runIDCheckingPromptBuilder struct {
	inputs []PromptInput
}

func (b *runIDCheckingPromptBuilder) BuildPrompt(input PromptInput) (string, error) {
	b.inputs = append(b.inputs, input)
	if input.RunID == "" {
		return "", errors.New("prompt run ID is required")
	}
	return "standalone pipeline prompt", nil
}

type pipelineInvocationFactory struct {
	registry    *StopRegistry
	expectedIDs []string
	processes   []*pipelineInvocationProcess
}

func (f *pipelineInvocationFactory) Start(_ context.Context, spec ProcessSpec) (Process, error) {
	index := len(f.processes)
	if index >= len(f.expectedIDs) {
		return nil, errors.New("unexpected pipeline dispatch")
	}
	process := &pipelineInvocationProcess{
		registry: f.registry,
		runID:    f.expectedIDs[index],
	}
	if index == 0 {
		process.writeErr = os.WriteFile(
			filepath.Join(spec.WorkingDirectory, ".gg", "acceptance-criteria.md"),
			[]byte("---\ngg_run_id: \""+process.runID+"\"\ngg_disposition: passed\n---\nevidence\n"),
			0o644,
		)
	} else {
		process.waitErr = errors.New("stop after Development dispatch")
	}
	f.processes = append(f.processes, process)
	return process, nil
}

type pipelineInvocationProcess struct {
	registry *StopRegistry
	runID    string
	writeErr error
	waitErr  error
	active   bool
}

func (p *pipelineInvocationProcess) Wait() (ProcessResult, error) {
	p.active = p.registry.Active(p.runID)
	if p.writeErr != nil {
		return ProcessResult{ExitCode: 1}, p.writeErr
	}
	if p.waitErr != nil {
		return ProcessResult{ExitCode: 9}, p.waitErr
	}
	return ProcessResult{}, nil
}

func (p *pipelineInvocationProcess) Cancel() error { return nil }

func TestAgentRunnerRunPipelineUsesSameDerivedRunIDForPromptAndDispatch(t *testing.T) {
	settings := runnerSettings()
	phases := make(map[config.Phase]config.ResolvedPhase)
	for _, phase := range config.RemovablePhases() {
		phases[phase] = config.ResolvedPhase{Enabled: false, AgentSettings: settings}
	}
	executable, err := pipeline.Resolve(
		pipeline.DefaultPipeline(),
		config.ResolvedConfig{Defaults: settings, Phases: phases},
	)
	if err != nil {
		t.Fatal(err)
	}

	worktree := t.TempDir()
	baseRunID := "pipeline-run"
	expectedIDs := []string{
		baseRunID + "/acceptance_criteria/",
		baseRunID + "/development/implementation",
	}
	registry := NewStopRegistry()
	prompts := &runIDCheckingPromptBuilder{}
	factory := &pipelineInvocationFactory{registry: registry, expectedIDs: expectedIDs}
	runner := NewAgentRunner(AgentRunnerOptions{
		Factory:  factory,
		Prompt:   prompts,
		Lookup:   func(string) (string, error) { return "/fake-agent", nil },
		Registry: registry,
		LogRoot:  t.TempDir(),
	})
	project := runnerProject(worktree)
	project.WorktreePath = worktree

	_, runErr := runner.RunPipeline(context.Background(), PipelineRunRequest{
		Project:        project,
		Pipeline:       executable,
		PhaseContracts: executable.PhaseContracts(),
		Subphases:      pipeline.DevelopmentSubphaseGeneration{},
		RunID:          baseRunID,
	})

	if len(factory.processes) != len(expectedIDs) {
		t.Fatalf("pipeline dispatched %d process(es), want %d; error = %v", len(factory.processes), len(expectedIDs), runErr)
	}
	if len(prompts.inputs) != len(expectedIDs) {
		t.Fatalf("pipeline built %d prompt(s), want %d", len(prompts.inputs), len(expectedIDs))
	}
	for index, expected := range expectedIDs {
		if got := prompts.inputs[index].RunID; got != expected {
			t.Errorf("prompt %d run ID = %q, want %q", index, got, expected)
		}
		if !factory.processes[index].active {
			t.Errorf("dispatch %d did not register exact run ID %q", index, expected)
		}
	}
	if runErr == nil || !errors.Is(runErr, factory.processes[1].waitErr) {
		t.Fatalf("RunPipeline() error = %v, want Development dispatch failure", runErr)
	}
}

func runRunnerGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}
