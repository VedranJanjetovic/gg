package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/cli"
	"github.com/VedranJanjetovic/gg/internal/config"
	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/state"
	"github.com/VedranJanjetovic/gg/internal/tui"
)

type attachmentContextKey struct{}

func TestProjectTUIAttacherMapsProjectStreamsActionsAndErrors(t *testing.T) {
	project := state.ProjectState{
		Name: "Mapped project", Slug: "mapped-project", Status: state.StatusPending,
		CurrentPhase: "pipeline", PipelineConfig: state.PipelineConfigSnapshot{SchemaVersion: 1, Data: []byte(`{}`)},
	}
	latest := project
	latest.Status = state.StatusStopped
	input := bytes.NewBufferString("input")
	output := &bytes.Buffer{}
	runErr := errors.New("TUI failed")
	loads, starts, stops, resumes := 0, 0, 0, 0
	attachment := cli.ProjectAttachment{
		Project: project,
		Load: func(ctx context.Context) (state.ProjectState, error) {
			loads++
			assertAttachmentContext(t, ctx)
			return latest, nil
		},
		Start:  func(ctx context.Context) error { starts++; assertAttachmentContext(t, ctx); return nil },
		Stop:   func(ctx context.Context) error { stops++; assertAttachmentContext(t, ctx); return nil },
		Resume: func(ctx context.Context) error { resumes++; assertAttachmentContext(t, ctx); return nil },
	}
	runner := func(ctx context.Context, gotProject state.ProjectState, loader tui.Loader, actions tui.Actions, gotInput io.Reader, gotOutput io.Writer, options ...tui.Option) error {
		assertAttachmentContext(t, ctx)
		if !reflect.DeepEqual(gotProject, project) {
			t.Fatalf("project = %#v, want %#v", gotProject, project)
		}
		if gotInput != input || gotOutput != output {
			t.Fatalf("streams were not mapped directly")
		}
		if _, err := tui.NewModel(ctx, gotProject, loader, actions, options...); err != nil {
			t.Fatalf("adapter omitted the explicit pending pipeline: %v", err)
		}
		loaded, err := loader(ctx)
		if err != nil || !reflect.DeepEqual(loaded, latest) {
			t.Fatalf("loader result=%#v err=%v", loaded, err)
		}
		for name, action := range map[string]func(context.Context) error{
			"start": actions.Start, "stop": actions.Stop, "resume": actions.Resume,
		} {
			if action == nil {
				t.Fatalf("%s action was not mapped", name)
			}
			if err := action(ctx); err != nil {
				t.Fatalf("%s action: %v", name, err)
			}
		}
		return runErr
	}
	attacher := projectTUIAttacher{input: input, output: output, run: runner}
	ctx := context.WithValue(context.Background(), attachmentContextKey{}, "mapped")

	if err := attacher.Attach(ctx, attachment); !errors.Is(err, runErr) {
		t.Fatalf("Attach error = %v, want preserved runner error", err)
	}
	if loads != 1 || starts != 1 || stops != 1 || resumes != 1 {
		t.Fatalf("callback calls load/start/stop/resume = %d/%d/%d/%d", loads, starts, stops, resumes)
	}
}

func TestProjectTUIAttacherMapsSkipProjectionAndCallback(t *testing.T) {
	project := state.ProjectState{Name: "Skip project", Slug: "skip-project", Status: state.StatusFailed}
	called := 0
	attachment := cli.ProjectAttachment{
		Project:       project,
		Skip:          func(context.Context) error { called++; return nil },
		SkipAvailable: true,
		SkipLabel:     "QA",
		SkipTarget:    func(state.ProjectState) (bool, string) { return true, "QA" },
	}
	runner := func(_ context.Context, _ state.ProjectState, _ tui.Loader, actions tui.Actions, _ io.Reader, _ io.Writer, _ ...tui.Option) error {
		if actions.Skip == nil || !actions.SkipAvailable || actions.SkipLabel != "QA" || actions.SkipTarget == nil {
			t.Fatalf("skip action was not mapped: %+v", actions)
		}
		available, label := actions.SkipTarget(project)
		if !available || label != "QA" {
			t.Fatalf("skip projection = %t/%q", available, label)
		}
		return actions.Skip(context.Background())
	}
	if err := (projectTUIAttacher{run: runner}).Attach(context.Background(), attachment); err != nil {
		t.Fatal(err)
	}
	if called != 1 {
		t.Fatalf("skip callback calls = %d, want 1", called)
	}
}

func TestProjectTUIAttacherNonTTYStartsAndPrintsFinalStatus(t *testing.T) {
	initial := state.ProjectState{
		Name: "Non-TTY project", Slug: "non-tty-project", Status: state.StatusPending,
		CurrentPhase: "pipeline", PipelineConfig: state.PipelineConfigSnapshot{SchemaVersion: 1, Data: []byte(`{}`)},
	}
	latest := initial
	starts, loads := 0, 0
	input := bytes.NewBuffer(nil)
	output := &bytes.Buffer{}
	attachment := cli.ProjectAttachment{
		Project: initial,
		Start: func(context.Context) error {
			starts++
			latest.Status = state.StatusFinished
			latest.CurrentPhase = string(pipeline.PhaseTestDocument)
			latest.PipelineConfig = runtimeTestSnapshot(t)
			return nil
		},
		Load: func(context.Context) (state.ProjectState, error) {
			loads++
			return latest, nil
		},
	}
	attacher := projectTUIAttacher{input: input, output: output, run: tui.Run}

	if err := attacher.Attach(context.Background(), attachment); err != nil {
		t.Fatal(err)
	}
	if starts != 1 || loads != 1 {
		t.Fatalf("starts=%d loads=%d, want one synchronous call each", starts, loads)
	}
	if !strings.Contains(output.String(), "Status: succeeded") || strings.Contains(output.String(), "q quit") || strings.Contains(output.String(), "\x1b[") {
		t.Fatalf("non-TTY output:\n%s", output.String())
	}
}

func TestProjectTUIAttacherNonTTYPropagatesLifecycleErrors(t *testing.T) {
	pending := state.ProjectState{
		Name: "Pending project", Slug: "pending-project", Status: state.StatusPending,
		CurrentPhase: "pipeline", PipelineConfig: state.PipelineConfigSnapshot{SchemaVersion: 1, Data: []byte(`{}`)},
	}
	startErr := errors.New("cannot start")
	loads := 0
	output := &bytes.Buffer{}
	attacher := projectTUIAttacher{input: bytes.NewBuffer(nil), output: output, run: tui.Run}
	err := attacher.Attach(context.Background(), cli.ProjectAttachment{
		Project: pending,
		Start:   func(context.Context) error { return startErr },
		Load: func(context.Context) (state.ProjectState, error) {
			loads++
			return pending, nil
		},
	})
	if !errors.Is(err, startErr) || !strings.Contains(err.Error(), "start project") {
		t.Fatalf("start error = %v", err)
	}
	if loads != 0 || output.Len() != 0 {
		t.Fatalf("start failure loads=%d output=%q", loads, output.String())
	}

	loadErr := errors.New("cannot load")
	running := pending
	running.Status = state.StatusRunning
	running.CurrentPhase = string(pipeline.PhaseDevelopment)
	running.PipelineConfig = runtimeTestSnapshot(t)
	output.Reset()
	err = attacher.Attach(context.Background(), cli.ProjectAttachment{
		Project: running,
		Load: func(context.Context) (state.ProjectState, error) {
			return state.ProjectState{}, loadErr
		},
	})
	if !errors.Is(err, loadErr) || !strings.Contains(err.Error(), "load project status") {
		t.Fatalf("load error = %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("load failure output=%q", output.String())
	}
}

func TestNewAppWithIOWiresBareProjectAttachment(t *testing.T) {
	repo := t.TempDir()
	runProductionGit(t, repo, "init", "-q")
	runProductionGit(t, repo, "config", "user.email", "gg@example.test")
	runProductionGit(t, repo, "config", "user.name", "gg test")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runProductionGit(t, repo, "add", "README.md")
	runProductionGit(t, repo, "-c", "commit.gpgsign=false", "commit", "-qm", "initial")
	defaults := config.AgentSettings{Agent: config.AgentCodex, Model: "gpt-5.6-sol", Effort: config.EffortHigh, Provenance: config.ModelProvenanceCatalog}
	phases := make([]config.PhaseConfig, 0, len(config.CompletePhaseOrder()))
	for _, phase := range config.CompletePhaseOrder() {
		required := false
		for _, candidate := range config.RequiredPhases() {
			if candidate == phase {
				required = true
				break
			}
		}
		phases = append(phases, config.PhaseConfig{Phase: phase, Enabled: true, Required: required, AgentSettings: defaults})
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store := config.NewStore()
	if err := store.SaveGlobal(config.GlobalConfig{Version: config.CurrentSchemaVersion, Defaults: defaults}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveProject(repo, config.CompleteProjectConfig(config.CompleteSchemaVersion, defaults, phases, config.GitOpsOverride{})); err != nil {
		t.Fatal(err)
	}
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })

	input := bytes.NewBufferString("Build an attached project.\nThe project opens a session.\n\n")
	tuiOutput := &bytes.Buffer{}
	runCalls := 0
	runner := func(_ context.Context, project state.ProjectState, _ tui.Loader, actions tui.Actions, gotInput io.Reader, gotOutput io.Writer, options ...tui.Option) error {
		runCalls++
		if project.Slug != "attached-project" || project.Status != state.StatusPending {
			t.Fatalf("attached project = %#v", project)
		}
		// Pending-pipeline fallback plus the update-availability checker.
		if actions.Start == nil || actions.OpenCode == nil || actions.OpenTerminal == nil || gotInput != input || gotOutput != tuiOutput || len(options) != 2 {
			t.Fatal("production attachment mapping is incomplete")
		}
		return nil
	}
	app, err := newAppWithIO(context.Background(), input, tuiOutput, runner)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer

	if code := app.Run(context.Background(), nil, &stdout, &stderr); code != 0 {
		t.Fatalf("bare gg code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if runCalls != 1 {
		t.Fatalf("TUI runner calls = %d, want 1", runCalls)
	}
}

func TestNewAppWithIOWiresExistingProjectAttachOnly(t *testing.T) {
	root := t.TempDir()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })

	store, err := state.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	projects := state.NewLifecycleService(store, nil, store.Locker())
	project := state.ProjectState{
		Name:               "Existing Project",
		Slug:               "existing-project",
		OriginalGoal:       "Attach without restarting the pipeline.",
		AcceptanceCriteria: []string{"The existing project is not mutated."},
		PipelineConfig:     runtimeTestSnapshot(t),
		CurrentPhase:       string(pipeline.PhaseDevelopment),
		CurrentSubphase:    "testing",
		Status:             state.StatusStopped,
		WorktreePath:       filepath.Join(root, ".gg", "worktrees", "existing-project"),
		BranchName:         "gg/existing-project",
	}
	if err := projects.Create(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	if err := config.NewStore().SaveProject(root, config.ProjectConfig{Version: config.CurrentSchemaVersion}); err != nil {
		t.Fatal(err)
	}
	before, err := store.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}

	input := bytes.NewBuffer(nil)
	tuiOutput := &bytes.Buffer{}
	runCalls := 0
	runner := func(ctx context.Context, attached state.ProjectState, loader tui.Loader, actions tui.Actions, gotInput io.Reader, gotOutput io.Writer, _ ...tui.Option) error {
		runCalls++
		if !reflect.DeepEqual(attached, before) {
			t.Fatalf("attached project = %#v, want %#v", attached, before)
		}
		if actions.Start != nil {
			t.Fatal("existing-project attachment received a Start action")
		}
		if actions.Stop == nil || actions.Resume == nil || gotInput != input || gotOutput != tuiOutput {
			t.Fatal("existing-project attachment mapping is incomplete")
		}
		loaded, loadErr := loader(ctx)
		if loadErr != nil || !reflect.DeepEqual(loaded, before) {
			t.Fatalf("loader result=%#v err=%v", loaded, loadErr)
		}
		return nil
	}
	app, err := newAppWithIO(context.Background(), input, tuiOutput, runner)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer

	if code := app.Run(context.Background(), []string{"Existing Project"}, &stdout, &stderr); code != 0 {
		t.Fatalf("existing attach code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if runCalls != 1 {
		t.Fatalf("TUI runner calls = %d, want 1", runCalls)
	}
	after, err := store.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("attach-only mutated state\nbefore: %#v\nafter:  %#v", before, after)
	}
}

func assertAttachmentContext(t *testing.T, ctx context.Context) {
	t.Helper()
	if got := ctx.Value(attachmentContextKey{}); got != "mapped" {
		t.Fatalf("callback context value = %v", got)
	}
}

func runtimeTestSnapshot(t *testing.T) state.PipelineConfigSnapshot {
	t.Helper()
	settings := config.AgentSettings{Agent: config.AgentCodex, Model: "gpt-5", Effort: config.EffortMedium}
	resolved := config.ResolvedConfig{Defaults: settings, Phases: make(map[config.Phase]config.ResolvedPhase)}
	for _, phase := range config.RemovablePhases() {
		resolved.Phases[phase] = config.ResolvedPhase{Enabled: false, AgentSettings: settings}
	}
	plan, err := pipeline.Resolve(pipeline.DefaultPipeline(), resolved)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := pipeline.SnapshotExecution(plan, pipeline.DevelopmentSubphaseGeneration{}, 3)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}
