package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/config"
	"github.com/VedranJanjetovic/gg/internal/pipeline"
)

type capturePipeline struct {
	runRequests []pipeline.RunRequest
}

func chdirForTest(t *testing.T, dir string) {
	t.Helper()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("change working directory to %q: %v", dir, err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previous); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})
}

func (p *capturePipeline) Run(_ context.Context, request pipeline.RunRequest) error {
	p.runRequests = append(p.runRequests, request)
	return nil
}
func (*capturePipeline) Stop(context.Context, pipeline.StopRequest) error { return nil }
func (*capturePipeline) Prune(context.Context) error                      { return nil }

func TestRunOverridesResolveAndDispatchEffectiveConfiguration(t *testing.T) {
	t.Skip("transient configuration flags were removed in Phase 9")
	root := t.TempDir()
	initTestRepository(t, root)
	chdirForTest(t, root)
	disabled := false
	store := &memoryConfigureStore{
		global: config.GlobalConfig{
			Version:  config.CurrentSchemaVersion,
			Defaults: config.AgentSettings{Agent: config.AgentClaude, Model: "global-model", Effort: config.EffortMedium},
		},
		project: config.ProjectConfig{
			Version:  config.CurrentSchemaVersion,
			Defaults: config.AgentSettingsOverride{Model: "project-model"},
			PhaseOverrides: map[config.Phase]config.PhaseOverride{
				config.PhaseQA: {Enabled: &disabled, AgentSettingsOverride: config.AgentSettingsOverride{Agent: config.AgentCodex}},
			},
		},
	}
	capture := &capturePipeline{}
	var stdout, stderr bytes.Buffer
	app := New(
		WithConfigStore(store),
		WithPipelineService(capture),
		WithWorkingDirectory(func() (string, error) { return "/project", nil }),
	)
	args := []string{
		"run", "--agent", "codex", "--model", "run-model", "--effort", "low",
		"target", "--force", "--phase-agent", "qa=claude", "--phase-effort", "qa=high", "--enable-phase", "qa",
		"--phase-model", "linting=alias-model", "--phase-model", "build_checker=canonical-model",
		"--disable-phase", "ci",
	}

	if code := app.Run(context.Background(), args, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if len(capture.runRequests) != 1 {
		t.Fatalf("run dispatch count = %d, want 1", len(capture.runRequests))
	}
	request := capture.runRequests[0]
	if !reflect.DeepEqual(request.Args, []string{"target", "--force"}) {
		t.Errorf("positional args = %#v", request.Args)
	}
	if request.WorktreePath == "" || request.WorktreePath == "/project" {
		t.Fatalf("pipeline request did not receive the persisted project worktree: %q", request.WorktreePath)
	}
	wantQA := config.ResolvedPhase{Enabled: true, AgentSettings: config.AgentSettings{Agent: config.AgentClaude, Model: "run-model", Effort: config.EffortHigh}}
	if got := request.Config.Phases[config.PhaseQA]; !reflect.DeepEqual(got, wantQA) {
		t.Errorf("qa config = %#v, want %#v", got, wantQA)
	}
	wantBuild := config.ResolvedPhase{Enabled: true, AgentSettings: config.AgentSettings{Agent: config.AgentCodex, Model: "canonical-model", Effort: config.EffortLow}}
	if got := request.Config.Phases[config.PhaseBuildChecker]; !reflect.DeepEqual(got, wantBuild) {
		t.Errorf("build_checker config = %#v, want %#v", got, wantBuild)
	}
	if _, ok := request.Config.Phases[config.PhaseLintingAlias]; ok {
		t.Fatal("effective config contains linting alias")
	}
	if request.Config.Phases[config.PhaseCI].Enabled {
		t.Fatal("ci phase remained enabled")
	}
}

func TestRunRejectsInvalidOverridesBeforeDispatch(t *testing.T) {
	t.Skip("superseded by removed-flag coverage")
	root := t.TempDir()
	initTestRepository(t, root)
	chdirForTest(t, root)
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "unknown flag", args: []string{"--unknown"}, want: "flag provided but not defined"},
		{name: "missing flag value", args: []string{"target", "--agent"}, want: "requires a value"},
		{name: "invalid agent", args: []string{"--agent", "gemini"}, want: "unsupported agent"},
		{name: "invalid agent after positional", args: []string{"target", "--agent", "gemini"}, want: "unsupported agent"},
		{name: "invalid effort", args: []string{"--effort", "extreme"}, want: "unsupported effort"},
		{name: "invalid phase", args: []string{"--disable-phase", "deploy"}, want: "unsupported phase"},
		{name: "missing equals", args: []string{"--phase-model", "qa"}, want: "expected phase=value"},
		{name: "empty value", args: []string{"--phase-model", "qa="}, want: "expected non-empty phase=value"},
		{name: "invalid phase agent", args: []string{"--phase-agent", "qa=gemini"}, want: "unsupported agent"},
		{name: "invalid phase effort", args: []string{"--phase-effort", "qa=extreme"}, want: "unsupported effort"},
		{name: "zero max iterations", args: []string{"--max-iterations", "0"}, want: "must be positive"},
		{name: "negative max iterations", args: []string{"target", "--max-iterations=-1"}, want: "must be positive"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			capture := &capturePipeline{}
			store := configuredMemoryStore()
			var stdout, stderr bytes.Buffer
			app := New(WithConfigStore(store), WithPipelineService(capture))
			code := app.Run(context.Background(), append([]string{"run"}, tt.args...), &stdout, &stderr)
			if code == 0 || !strings.Contains(stderr.String(), tt.want) {
				t.Fatalf("code = %d, stderr = %q, want %q", code, stderr.String(), tt.want)
			}
			if len(capture.runRequests) != 0 {
				t.Fatal("invalid overrides were dispatched")
			}
		})
	}
}

func TestRunRejectsCIWhenPRIsDisabledBeforeDispatch(t *testing.T) {
	t.Skip("phase toggles are no longer run flags")
	root := t.TempDir()
	initTestRepository(t, root)
	chdirForTest(t, root)
	store := configuredMemoryStore()
	capture := &capturePipeline{}
	var stdout, stderr bytes.Buffer
	app := New(WithConfigStore(store), WithPipelineService(capture))

	code := app.Run(context.Background(), []string{"run", "--disable-phase", "pr"}, &stdout, &stderr)
	if code == 0 || !strings.Contains(stderr.String(), "enable PR or disable CI") {
		t.Fatalf("code = %d, stderr = %q, want CI/PR compatibility error", code, stderr.String())
	}
	if len(capture.runRequests) != 0 {
		t.Fatal("incompatible pipeline configuration was dispatched")
	}
}

func TestRunDuplicateAndConflictingPhaseFlagsUseLastValue(t *testing.T) {
	t.Skip("phase toggles are no longer run flags")
	root := t.TempDir()
	initTestRepository(t, root)
	chdirForTest(t, root)
	store := configuredMemoryStore()
	capture := &capturePipeline{}
	var stdout, stderr bytes.Buffer
	app := New(WithConfigStore(store), WithPipelineService(capture))
	args := []string{
		"run",
		"--phase-model", "qa=first",
		"--disable-phase", "qa",
		"target",
		"--phase-model=qa=second",
		"--enable-phase=qa",
	}

	if code := app.Run(context.Background(), args, &stdout, &stderr); code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr.String())
	}
	if len(capture.runRequests) != 1 {
		t.Fatalf("run dispatch count = %d, want 1", len(capture.runRequests))
	}
	qa := capture.runRequests[0].Config.Phases[config.PhaseQA]
	if qa.Model != "second" || !qa.Enabled {
		t.Fatalf("qa config = %#v, want last model and enabled values", qa)
	}
}

func TestRunDelimiterStopsOverrideParsingAndIsNotDispatched(t *testing.T) {
	t.Skip("phase toggles are no longer run flags")
	root := t.TempDir()
	initTestRepository(t, root)
	chdirForTest(t, root)
	store := configuredMemoryStore()
	capture := &capturePipeline{}
	var stdout, stderr bytes.Buffer
	app := New(WithConfigStore(store), WithPipelineService(capture))
	args := []string{
		"run", "target", "--model", "override-model", "--",
		"--agent", "gemini",
		"--model", "pipeline-model",
		"--effort", "extreme",
		"--phase-agent", "deploy=gemini",
		"--phase-model", "deploy=pipeline-model",
		"--phase-effort", "deploy=extreme",
		"--enable-phase", "deploy",
		"--disable-phase", "deploy",
		"--agent=gemini",
		"--model=pipeline-model",
		"--effort=extreme",
		"--phase-agent=deploy=gemini",
		"--phase-model=deploy=pipeline-model",
		"--phase-effort=deploy=extreme",
		"--enable-phase=deploy",
		"--disable-phase=deploy",
		"KEY=value",
	}

	if code := app.Run(context.Background(), args, &stdout, &stderr); code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr.String())
	}
	if len(capture.runRequests) != 1 {
		t.Fatalf("run dispatch count = %d, want 1", len(capture.runRequests))
	}
	request := capture.runRequests[0]
	wantArgs := append([]string{"target"}, args[5:]...)
	if !reflect.DeepEqual(request.Args, wantArgs) {
		t.Fatalf("pipeline args = %#v, want %#v", request.Args, wantArgs)
	}
	if got := request.Config.Phases[config.PhaseQA].Model; got != "override-model" {
		t.Fatalf("qa model = %q, want pre-delimiter override", got)
	}
}

func TestRunDelimiterForwardsHelpTokensToPipeline(t *testing.T) {
	root := t.TempDir()
	initTestRepository(t, root)
	chdirForTest(t, root)
	for _, helpToken := range []string{"--help", "-h"} {
		t.Run(helpToken, func(t *testing.T) {
			store := configuredMemoryStore()
			capture := &capturePipeline{}
			var stdout, stderr bytes.Buffer
			app := New(WithConfigStore(store), WithPipelineService(capture))

			if code := app.Run(context.Background(), []string{"run", "--", helpToken}, &stdout, &stderr); code != 0 {
				t.Fatalf("code = %d, stderr = %q", code, stderr.String())
			}
			if len(capture.runRequests) != 1 {
				t.Fatalf("run dispatch count = %d, want 1", len(capture.runRequests))
			}
			if got, want := capture.runRequests[0].Args, []string{helpToken}; !reflect.DeepEqual(got, want) {
				t.Fatalf("pipeline args = %#v, want %#v", got, want)
			}
			if strings.Contains(stdout.String(), "Usage:\n  gg run") {
				t.Fatalf("delimiter help token printed command help:\n%s", stdout.String())
			}
		})
	}
}

func TestRunHelpDocumentsTransientFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	store := &memoryConfigureStore{globalErr: config.ErrGlobalConfigNotFound, projectErr: config.ErrProjectNotConfigured}
	code := New(WithConfigStore(store)).Run(context.Background(), []string{"run", "--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr.String())
	}
	for _, want := range []string{"--parent-branch", "--base-ref", "--max-iterations", "--repair-existing-verification", "Inherit or Pick", "pass every following token to the pipeline unchanged"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("help missing %q:\n%s", want, stdout.String())
		}
	}
	for _, removed := range []string{"--agent", "--model", "--effort", "--phase-agent", "--phase-model", "--phase-effort", "--enable-phase", "--disable-phase"} {
		if strings.Contains(stdout.String(), removed) {
			t.Errorf("help contains removed flag %q:\n%s", removed, stdout.String())
		}
	}
}

func TestRunRejectsRemovedConfigurationFlags(t *testing.T) {
	for _, flagName := range []string{"--agent", "--model", "--effort", "--phase-agent", "--phase-model", "--phase-effort", "--enable-phase", "--disable-phase"} {
		t.Run(flagName, func(t *testing.T) {
			app := New(WithConfigStore(configuredMemoryStore()), WithPipelineService(&capturePipeline{}))
			var stdout, stderr bytes.Buffer
			if code := app.Run(context.Background(), []string{"run", flagName, "value"}, &stdout, &stderr); code == 0 || !strings.Contains(stderr.String(), "flag provided but not defined") {
				t.Fatalf("code = %d, stderr = %q", code, stderr.String())
			}
		})
	}
}

func TestRunOverridesDoNotRewriteConfigurationFiles(t *testing.T) {
	t.Skip("transient configuration flags were removed in Phase 9")
	root := t.TempDir()
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	store := config.NewStore()
	global := config.GlobalConfig{
		Version:  config.CurrentSchemaVersion,
		Defaults: config.AgentSettings{Agent: config.AgentClaude, Model: "global-model", Effort: config.EffortMedium},
	}
	project := config.ProjectConfig{Version: config.CurrentSchemaVersion, Defaults: config.AgentSettingsOverride{Model: "project-model"}}
	if err := store.SaveGlobal(global); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveProject(root, project); err != nil {
		t.Fatal(err)
	}
	globalPath := filepath.Join(configHome, "gg", "config.yaml")
	projectPath := filepath.Join(root, ".gg", "config.yaml")
	globalBefore, err := os.ReadFile(globalPath)
	if err != nil {
		t.Fatal(err)
	}
	projectBefore, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatal(err)
	}

	capture := &capturePipeline{}
	var stdout, stderr bytes.Buffer
	app := New(WithConfigStore(store), WithPipelineService(capture), WithWorkingDirectory(func() (string, error) { return root, nil }))
	args := []string{"run", "--agent", "codex", "--phase-model", "qa=one-run-model", "--disable-phase", "planning"}
	if code := app.Run(context.Background(), args, &stdout, &stderr); code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr.String())
	}
	globalAfter, err := os.ReadFile(globalPath)
	if err != nil {
		t.Fatal(err)
	}
	projectAfter, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(globalBefore, globalAfter) {
		t.Fatal("global configuration changed byte-for-byte")
	}
	if !bytes.Equal(projectBefore, projectAfter) {
		t.Fatal("project configuration changed byte-for-byte")
	}
}

func TestParseRunOptionsGitOpsOverrides(t *testing.T) {
	options, err := parseRunOptions([]string{"--parent-branch", "develop", "--base-ref=origin/develop", "--disable-pr", "--enable-ci"})
	if err != nil {
		t.Fatal(err)
	}
	if options.overrides.GitOps.ParentBranch != "develop" || options.overrides.GitOps.BaseRef != "origin/develop" || options.overrides.GitOps.EnablePR == nil || *options.overrides.GitOps.EnablePR || options.overrides.GitOps.EnableCI == nil || !*options.overrides.GitOps.EnableCI {
		t.Fatalf("GitOps overrides = %#v", options.overrides.GitOps)
	}
}

// --repair-existing-verification is a boolean flag, so it must never consume
// the token that follows it. Treating it as a valued flag silently pushed the
// remaining flags behind a positional argument, where flag.Parse stops.
func TestParseRunOptionsRepairFlagDoesNotConsumeFollowingToken(t *testing.T) {
	tests := []struct {
		name          string
		args          []string
		wantRepair    bool
		wantParent    string
		wantIterating int
	}{
		{name: "flag then selector then valued flag", args: []string{"--repair-existing-verification", "proj", "--parent-branch", "main"}, wantRepair: true, wantParent: "main", wantIterating: 3},
		{name: "flag then selector then max iterations", args: []string{"--repair-existing-verification", "proj", "--max-iterations", "5"}, wantRepair: true, wantIterating: 5},
		{name: "selector first", args: []string{"proj", "--repair-existing-verification"}, wantRepair: true, wantIterating: 3},
		{name: "assignment form", args: []string{"--repair-existing-verification=true", "proj"}, wantRepair: true, wantIterating: 3},
		{name: "single dash", args: []string{"-repair-existing-verification", "proj"}, wantRepair: true, wantIterating: 3},
		{name: "explicitly false", args: []string{"--repair-existing-verification=false", "proj"}, wantIterating: 3},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options, err := parseRunOptions(test.args)
			if err != nil {
				t.Fatal(err)
			}
			if options.repairExistingVerification != test.wantRepair {
				t.Errorf("repairExistingVerification = %v, want %v", options.repairExistingVerification, test.wantRepair)
			}
			if options.overrides.GitOps.ParentBranch != test.wantParent {
				t.Errorf("ParentBranch = %q, want %q", options.overrides.GitOps.ParentBranch, test.wantParent)
			}
			if options.maxIterations != test.wantIterating {
				t.Errorf("maxIterations = %d, want %d", options.maxIterations, test.wantIterating)
			}
			if len(options.args) != 1 || options.args[0] != "proj" {
				t.Errorf("positional args = %v, want [proj]", options.args)
			}
		})
	}
}

func TestParseResumeOptionsAcceptsEveryRepairFlagSpelling(t *testing.T) {
	tests := []struct {
		args       []string
		wantRepair bool
	}{
		{args: []string{"proj", "--repair-existing-verification"}, wantRepair: true},
		{args: []string{"proj", "--repair-existing-verification=true"}, wantRepair: true},
		{args: []string{"proj", "-repair-existing-verification"}, wantRepair: true},
		{args: []string{"proj", "-repair-existing-verification=true"}, wantRepair: true},
		{args: []string{"--repair-existing-verification", "proj"}, wantRepair: true},
		{args: []string{"proj", "--repair-existing-verification=false"}},
		{args: []string{"proj"}},
	}
	for _, test := range tests {
		t.Run(strings.Join(test.args, " "), func(t *testing.T) {
			options, err := parseResumeOptions(test.args)
			if err != nil {
				t.Fatal(err)
			}
			if options.selector != "proj" {
				t.Errorf("selector = %q, want %q", options.selector, "proj")
			}
			if options.repairExistingVerification != test.wantRepair {
				t.Errorf("repairExistingVerification = %v, want %v", options.repairExistingVerification, test.wantRepair)
			}
		})
	}
}

func TestParseResumeOptionsStillRejectsTwoSelectors(t *testing.T) {
	if _, err := parseResumeOptions([]string{"one", "two"}); err == nil {
		t.Fatal("expected an error for two project selectors")
	}
}

func TestFinishedRerunPlanExcludesDevelopmentAndRejectsNoGitOps(t *testing.T) {
	settings := config.AgentSettings{Agent: config.AgentCodex, Model: "m", Effort: config.EffortLow}
	resolved := config.ResolvedConfig{Defaults: settings, Phases: map[config.Phase]config.ResolvedPhase{}}
	for _, phase := range config.RemovablePhases() {
		resolved.Phases[phase] = config.ResolvedPhase{Enabled: phase == config.PhasePR || phase == config.PhaseCI, AgentSettings: settings}
	}
	plan, err := pipeline.Resolve(pipeline.DefaultPipeline(), resolved)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := pipeline.SnapshotExecution(plan, pipeline.DevelopmentSubphaseGeneration{}, 4)
	if err != nil {
		t.Fatal(err)
	}
	selected, _, attempts, err := finishedRerunPlan(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 4 {
		t.Fatalf("attempts = %d, want 4", attempts)
	}
	for _, phase := range selected.Phases() {
		if phase.Phase().ID() == pipeline.PhaseDevelopment {
			t.Fatal("development selected for finished rerun")
		}
	}
	resolved.Phases[config.PhasePR] = config.ResolvedPhase{Enabled: false, AgentSettings: settings}
	resolved.Phases[config.PhaseCI] = config.ResolvedPhase{Enabled: false, AgentSettings: settings}
	noGitOps, err := pipeline.Resolve(pipeline.DefaultPipeline(), resolved)
	if err != nil {
		t.Fatal(err)
	}
	noSnapshot, err := pipeline.SnapshotExecution(noGitOps, pipeline.DevelopmentSubphaseGeneration{}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := finishedRerunPlan(noSnapshot); err == nil || !strings.Contains(err.Error(), "no enabled PR/CI") {
		t.Fatalf("no-GitOps error = %v", err)
	}
}
