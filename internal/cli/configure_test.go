package cli

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

	"github.com/VedranJanjetovic/gg/internal/config"
	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/tui"
)

type memoryConfigureStore struct {
	global       config.GlobalConfig
	project      config.ProjectConfig
	globalErr    error
	projectErr   error
	savedGlobal  *config.GlobalConfig
	savedProject *config.ProjectConfig
	savedRoot    string
}

func (s *memoryConfigureStore) LoadGlobal() (config.GlobalConfig, error) {
	if s.globalErr != nil {
		return config.GlobalConfig{}, s.globalErr
	}
	return s.global, nil
}
func (s *memoryConfigureStore) SaveGlobal(v config.GlobalConfig) error {
	s.savedGlobal = &v
	return nil
}
func (s *memoryConfigureStore) LoadProject(string) (config.ProjectConfig, error) {
	if s.projectErr != nil {
		return config.ProjectConfig{}, s.projectErr
	}
	return s.project, nil
}
func (s *memoryConfigureStore) SaveProject(root string, v config.ProjectConfig) error {
	s.savedRoot, s.savedProject = root, &v
	return nil
}
func (s *memoryConfigureStore) SaveConfiguration(root string, global config.GlobalConfig, project config.ProjectConfig) error {
	if err := s.SaveGlobal(global); err != nil {
		return err
	}
	return s.SaveProject(root, project)
}

type promptOrderReader struct {
	output    *bytes.Buffer
	sawPrompt bool
}

func (r *promptOrderReader) Read([]byte) (int, error) {
	r.sawPrompt = strings.Contains(r.output.String(), "No global configuration found. Set the required defaults.")
	return 0, io.EOF
}

func TestAppConfigureWritesFirstPromptToSuppliedStdoutBeforeInputEOF(t *testing.T) {
	var stdout, stderr bytes.Buffer
	reader := &promptOrderReader{output: &stdout}
	store := &memoryConfigureStore{globalErr: config.ErrGlobalConfigNotFound, projectErr: config.ErrProjectNotConfigured}
	app := New(
		WithInput(reader),
		WithWorkingDirectory(func() (string, error) { return "/repo", nil }),
		WithConfigStore(store),
	)

	if code := app.Run(context.Background(), []string{"configure"}, &stdout, &stderr); code != 0 {
		t.Fatalf("configure exit code = %d, stderr = %q", code, stderr.String())
	}
	if !reader.sawPrompt {
		t.Fatalf("first configure prompt was not written to supplied stdout before input EOF: stdout = %q", stdout.String())
	}
}

func TestConfigureFirstTimeCollectsDefaultsAndInitializesProject(t *testing.T) {
	root := t.TempDir()
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	var output bytes.Buffer
	workflow := NewConfigureWorkflow(strings.NewReader("codex\ngpt-5\nhigh\n"), &output, func() (string, error) { return root, nil }, config.NewStore())
	if err := workflow.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	global, err := config.NewStore().LoadGlobal()
	if err != nil {
		t.Fatalf("LoadGlobal: %v", err)
	}
	wantGlobal := config.GlobalConfig{Version: config.CurrentSchemaVersion, Defaults: config.AgentSettings{Agent: config.AgentCodex, Model: "gpt-5", Effort: config.EffortHigh}, Folders: []string{root}}
	if !reflect.DeepEqual(global, wantGlobal) {
		t.Errorf("global = %#v, want %#v", global, wantGlobal)
	}
	project, err := config.NewStore().LoadProject(root)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	if project.Version != config.CompleteSchemaVersion {
		t.Errorf("project version = %d", project.Version)
	}
	if _, err := os.Stat(filepath.Join(root, ".gg", "projects")); err != nil {
		t.Errorf("runtime directory: %v", err)
	}
	if !strings.Contains(output.String(), "Configuration saved") {
		t.Errorf("output = %q", output.String())
	}
}

func TestConfigureFirstTimeProjectPreflightFailureLeavesNoConfigFiles(t *testing.T) {
	root := t.TempDir()
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	if err := os.MkdirAll(filepath.Join(root, ".gg"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gg", "projects"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}

	err := NewConfigureWorkflow(
		strings.NewReader("codex\ngpt-5\nhigh\n"),
		&bytes.Buffer{},
		func() (string, error) { return root, nil },
		config.NewStore(),
	).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "prepare project runtime directory") {
		t.Fatalf("Run error = %v, want project preflight failure", err)
	}
	for _, path := range []string{filepath.Join(configHome, "gg", "config.yaml"), filepath.Join(root, ".gg", "config.yaml")} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("config file %s exists after failure: %v", path, err)
		}
	}
}

func TestConfigureReconfigurationPreflightFailurePreservesConfigBytes(t *testing.T) {
	root := t.TempDir()
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	store := config.NewStore()
	global := config.GlobalConfig{Version: config.CurrentSchemaVersion, Defaults: config.AgentSettings{Agent: config.AgentClaude, Model: "sonnet", Effort: config.EffortMedium}}
	project := config.ProjectConfig{Version: config.CurrentSchemaVersion}
	if err := store.SaveGlobal(global); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveProject(root, project); err != nil {
		t.Fatal(err)
	}
	globalPath := filepath.Join(configHome, "gg", "config.yaml")
	projectPath := filepath.Join(root, ".gg", "config.yaml")
	globalBefore := []byte("# retain comments and formatting\nversion: 1\ndefaults: {agent: claude, model: sonnet, effort: medium}\n")
	if err := os.WriteFile(globalPath, globalBefore, 0600); err != nil {
		t.Fatal(err)
	}
	projectBefore, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(root, ".gg", "projects")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gg", "projects"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	input := "codex\n\n\n" + strings.Repeat("\n", len(config.RemovablePhases())*4)
	err = NewConfigureWorkflow(strings.NewReader(input), &bytes.Buffer{}, func() (string, error) { return root, nil }, store).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "prepare project runtime directory") {
		t.Fatalf("Run error = %v, want project preflight failure", err)
	}
	globalAfter, err := os.ReadFile(globalPath)
	if err != nil {
		t.Fatal(err)
	}
	projectAfter, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(globalAfter, globalBefore) || !bytes.Equal(projectAfter, projectBefore) {
		t.Fatal("reconfiguration failure changed prior global or project bytes")
	}
	for _, path := range []string{globalPath, projectPath} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0600 {
			t.Fatalf("configuration file %s mode = %o after failure, want 600", path, got)
		}
	}
}

func TestConfigureMissingGlobalPreservesExistingProject(t *testing.T) {
	t.Skip("sparse folder configurations are explicitly materialized during reconfiguration")
	disabled := false
	existing := config.ProjectConfig{
		Version:  config.CurrentSchemaVersion,
		Defaults: config.AgentSettingsOverride{Model: "project-model"},
		PhaseOverrides: map[config.Phase]config.PhaseOverride{
			config.PhaseQA: {Enabled: &disabled, AgentSettingsOverride: config.AgentSettingsOverride{Effort: config.EffortHigh}},
		},
	}
	store := &memoryConfigureStore{globalErr: config.ErrGlobalConfigNotFound, project: existing}

	err := NewConfigureWorkflow(
		strings.NewReader("codex\ngpt-5\nmedium\n"),
		&bytes.Buffer{},
		func() (string, error) { return "/repo", nil },
		store,
	).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if store.savedProject == nil {
		t.Fatal("project was not saved")
	}
	if !reflect.DeepEqual(*store.savedProject, existing) {
		t.Fatalf("existing project was replaced: got %#v, want %#v", *store.savedProject, existing)
	}
}

func TestConfigureRetriesInvalidRequiredInput(t *testing.T) {
	store := &memoryConfigureStore{globalErr: config.ErrGlobalConfigNotFound, projectErr: config.ErrProjectNotConfigured}
	var output bytes.Buffer
	input := "gemini\nclaude\n\nsonnet\nextreme\nmedium\n"
	err := NewConfigureWorkflow(strings.NewReader(input), &output, func() (string, error) { return "/repo", nil }, store).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := strings.Count(output.String(), "Invalid value:"); got != 3 {
		t.Errorf("retry count = %d, want 3; output=%q", got, output.String())
	}
	if store.savedGlobal == nil || store.savedGlobal.Defaults.Model != "sonnet" {
		t.Fatalf("saved global = %#v", store.savedGlobal)
	}
}

func TestConfigureReconfigurationPreservesDefaultsAndSetsPhaseOverrides(t *testing.T) {
	t.Skip("required phases no longer expose enabled toggles")
	global := config.GlobalConfig{Version: config.CurrentSchemaVersion, Defaults: config.AgentSettings{Agent: config.AgentClaude, Model: "sonnet", Effort: config.EffortMedium}}
	project := config.ProjectConfig{Version: config.CurrentSchemaVersion}
	store := &memoryConfigureStore{global: global, project: project}
	// Keep global values; opt into phase overrides; override all planning
	// fields; keep every other phase value.
	input := "\n\n\n" + "yes\n" + "no\ncodex\ngpt-5\nhigh\n" + strings.Repeat("\n", (len(configurablePhases())-1)*4)
	var output bytes.Buffer
	err := NewConfigureWorkflow(strings.NewReader(input), &output, func() (string, error) { return "/repo", nil }, store).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !reflect.DeepEqual(*store.savedGlobal, global) {
		t.Errorf("global = %#v, want %#v", *store.savedGlobal, global)
	}
	override := store.savedProject.PhaseOverrides[config.PhasePlanning]
	if override.Enabled == nil || *override.Enabled {
		t.Errorf("planning enabled = %#v, want false", override.Enabled)
	}
	if override.Agent != config.AgentCodex || override.Model != "gpt-5" || override.Effort != config.EffortHigh {
		t.Errorf("planning override = %#v", override)
	}
	if len(store.savedProject.PhaseOverrides) != 1 {
		t.Errorf("phase override count = %d, want 1", len(store.savedProject.PhaseOverrides))
	}
	for _, want := range []string{"Default agent [claude]", "Phase planning", "Phase build_checker", "Enabled [yes]"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("output missing %q", want)
		}
	}
	if strings.Contains(output.String(), "Phase grooming") {
		t.Error("grooming must not be part of interactive configuration")
	}
}

func TestConfigureReconfigurationSkipsPhaseWalkByDefault(t *testing.T) {
	store := configuredMemoryStore()
	// Keep defaults; press Enter on the per-phase opt-in question.
	var output bytes.Buffer
	err := NewConfigureWorkflow(strings.NewReader("\n\n\n\n"), &output, func() (string, error) { return "/repo", nil }, store).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if store.savedGlobal == nil || store.savedProject == nil {
		t.Fatal("configuration was not saved")
	}
	if len(store.savedProject.PhaseOverrides) != 0 {
		t.Errorf("phase overrides = %#v, want none", store.savedProject.PhaseOverrides)
	}
	if strings.Contains(output.String(), "Phase ") {
		t.Errorf("phase prompts shown without opt-in: %q", output.String())
	}
}

func TestConfigureReconfigurationPreservesArbitraryModelValues(t *testing.T) {
	t.Skip("complete configurations store whole phase tuples")
	global := config.GlobalConfig{Version: config.CurrentSchemaVersion, Defaults: config.AgentSettings{Agent: config.AgentClaude, Model: "provider-specific-model", Effort: config.EffortMedium}}
	project := config.ProjectConfig{Version: config.CurrentSchemaVersion, Defaults: config.AgentSettingsOverride{Model: "project-provider-model"}, PhaseOverrides: map[config.Phase]config.PhaseOverride{
		config.PhaseQA: {AgentSettingsOverride: config.AgentSettingsOverride{Model: "qa-provider-model"}},
	}}
	store := &memoryConfigureStore{global: global, project: project}
	input := "\n\n\n" + strings.Repeat("\n", len(config.RemovablePhases())*4)

	err := NewConfigureWorkflow(strings.NewReader(input), &bytes.Buffer{}, func() (string, error) { return "/repo", nil }, store).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := store.savedGlobal.Defaults.Model; got != global.Defaults.Model {
		t.Fatalf("global model = %q, want arbitrary persisted value %q", got, global.Defaults.Model)
	}
	if got := store.savedProject.Defaults.Model; got != project.Defaults.Model {
		t.Fatalf("project default model = %q, want arbitrary persisted value %q", got, project.Defaults.Model)
	}
	if got := store.savedProject.PhaseOverrides[config.PhaseQA].Model; got != project.PhaseOverrides[config.PhaseQA].Model {
		t.Fatalf("phase model = %q, want arbitrary persisted value %q", got, project.PhaseOverrides[config.PhaseQA].Model)
	}
}

func TestConfigureReconfigurationRetriesInvalidPhaseValues(t *testing.T) {
	store := configuredMemoryStore()
	input := "\n\n\n" + "yes\n" + "maybe\nyes\ngemini\nclaude\n\nturbo\nlow\n" + strings.Repeat("\n", (len(configurablePhases())-1)*4)
	var output bytes.Buffer
	err := NewConfigureWorkflow(strings.NewReader(input), &output, func() (string, error) { return "/repo", nil }, store).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := strings.Count(output.String(), "Invalid value:"); got != 3 {
		t.Errorf("retry count = %d, want 3", got)
	}
}

func TestConfigureEOFDoesNotWriteStagedChanges(t *testing.T) {
	store := configuredMemoryStore()
	disabled := false
	store.project.PhaseOverrides = map[config.Phase]config.PhaseOverride{
		config.PhaseQA: {Enabled: &disabled, AgentSettingsOverride: config.AgentSettingsOverride{Model: "qa-model"}},
	}
	originalGlobal := store.global
	originalProject := cloneProjectConfig(store.project)
	var output bytes.Buffer
	err := NewConfigureWorkflow(strings.NewReader("codex\n"), &output, func() (string, error) { return "/repo", nil }, store).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "input ended before completion") {
		t.Fatalf("Run error = %v", err)
	}
	if store.savedGlobal != nil || store.savedProject != nil {
		t.Fatal("configuration was written after EOF")
	}
	if !reflect.DeepEqual(store.global, originalGlobal) || !reflect.DeepEqual(store.project, originalProject) {
		t.Fatal("persisted configuration changed after EOF")
	}
}

func TestConfigureEmptyInputDoesNotWrite(t *testing.T) {
	store := configuredMemoryStore()
	var output bytes.Buffer
	err := NewConfigureWorkflow(strings.NewReader(""), &output, func() (string, error) { return "/repo", nil }, store).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "input ended before completion") {
		t.Fatalf("Run error = %v, want incomplete-input error", err)
	}
	if store.savedGlobal != nil || store.savedProject != nil {
		t.Fatal("configuration was written after empty-input EOF")
	}
	if output.Len() == 0 {
		t.Fatal("expected configure to show a prompt before empty-input EOF")
	}
}

func TestConfigureCancelledContextDoesNotWrite(t *testing.T) {
	store := configuredMemoryStore()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := NewConfigureWorkflow(strings.NewReader(""), &bytes.Buffer{}, func() (string, error) { return "/repo", nil }, store).Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
	if store.savedGlobal != nil || store.savedProject != nil {
		t.Fatal("configuration was written after cancellation")
	}
}

func TestConfigureCurrentDirectoryFailureDoesNotLoadOrWrite(t *testing.T) {
	sentinel := errors.New("cwd unavailable")
	store := configuredMemoryStore()
	err := NewConfigureWorkflow(strings.NewReader(""), &bytes.Buffer{}, func() (string, error) { return "", sentinel }, store).Run(context.Background())
	if !errors.Is(err, sentinel) {
		t.Fatalf("Run error = %v", err)
	}
	if store.savedGlobal != nil || store.savedProject != nil {
		t.Fatal("configuration was written")
	}
}

func configuredMemoryStore() *memoryConfigureStore {
	return &memoryConfigureStore{
		global:  config.GlobalConfig{Version: config.CurrentSchemaVersion, Defaults: config.AgentSettings{Agent: config.AgentClaude, Model: "sonnet", Effort: config.EffortMedium}},
		project: config.ProjectConfig{Version: config.CurrentSchemaVersion},
	}
}

func TestConfigurePickerRunsBeforeEffortAndPersistsAtomically(t *testing.T) {
	store := &memoryConfigureStore{globalErr: config.ErrGlobalConfigNotFound, projectErr: config.ErrProjectNotConfigured}
	catalog := config.NewStaticAgentCatalogSource(config.NewAgentCatalog(config.AgentCatalogEntry{Agent: config.AgentCodex, Models: []string{"gpt-5"}, ModelListStatus: config.ModelListAvailable}))
	called := false
	workflow := NewConfigureWorkflowWithPicker(strings.NewReader("high\n"), &bytes.Buffer{}, func() (string, error) { return "/repo", nil }, store, catalog, func(_ context.Context, got config.AgentCatalog, _ tui.WizardDefaults, _ io.Reader, _ io.Writer) (tui.PickerResult, error) {
		called = true
		if _, ok := got.Lookup(config.AgentCodex); !ok {
			t.Fatal("picker did not receive catalog")
		}
		return tui.PickerResult{Agent: config.AgentCodex, Model: "gpt-5", Effort: config.EffortHigh}, nil
	})
	if err := workflow.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !called || store.savedGlobal == nil {
		t.Fatalf("called=%v saved=%#v", called, store.savedGlobal)
	}
	if got := store.savedGlobal.Defaults; got.Agent != config.AgentCodex || got.Model != "gpt-5" || got.Effort != config.EffortHigh {
		t.Fatalf("defaults=%#v", got)
	}
}

func TestConfigurePickerCancellationDoesNotPersist(t *testing.T) {
	store := &memoryConfigureStore{globalErr: config.ErrGlobalConfigNotFound, projectErr: config.ErrProjectNotConfigured}
	workflow := NewConfigureWorkflowWithPicker(strings.NewReader(""), &bytes.Buffer{}, func() (string, error) { return "/repo", nil }, store, config.NewDefaultAgentCatalogSource(), func(context.Context, config.AgentCatalog, tui.WizardDefaults, io.Reader, io.Writer) (tui.PickerResult, error) {
		return tui.PickerResult{}, tui.ErrPickerCancelled
	})
	err := workflow.Run(context.Background())
	if !errors.Is(err, tui.ErrPickerCancelled) || store.savedGlobal != nil || store.savedProject != nil {
		t.Fatalf("err=%v savedGlobal=%#v savedProject=%#v", err, store.savedGlobal, store.savedProject)
	}
}

func TestConfigurePickerManualModelPersistsWithoutCatalogMembership(t *testing.T) {
	store := &memoryConfigureStore{globalErr: config.ErrGlobalConfigNotFound, projectErr: config.ErrProjectNotConfigured}
	workflow := NewConfigureWorkflowWithPicker(strings.NewReader("high\n"), &bytes.Buffer{}, func() (string, error) { return "/repo", nil }, store, config.NewDefaultAgentCatalogSource(), func(context.Context, config.AgentCatalog, tui.WizardDefaults, io.Reader, io.Writer) (tui.PickerResult, error) {
		return tui.PickerResult{Agent: config.AgentClaude, Model: "future-model", Manual: true, Effort: config.EffortHigh}, nil
	})
	if err := workflow.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if store.savedGlobal == nil || store.savedGlobal.Defaults.Model != "future-model" {
		t.Fatalf("saved = %#v, want manually entered model persisted", store.savedGlobal)
	}
}

func TestConfigurePickerManualEmptyModelDoesNotPersist(t *testing.T) {
	store := &memoryConfigureStore{globalErr: config.ErrGlobalConfigNotFound, projectErr: config.ErrProjectNotConfigured}
	workflow := NewConfigureWorkflowWithPicker(strings.NewReader("high\n"), &bytes.Buffer{}, func() (string, error) { return "/repo", nil }, store, config.NewDefaultAgentCatalogSource(), func(context.Context, config.AgentCatalog, tui.WizardDefaults, io.Reader, io.Writer) (tui.PickerResult, error) {
		return tui.PickerResult{Agent: config.AgentClaude, Model: "   ", Manual: true}, nil
	})
	err := workflow.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "staged global configuration") {
		t.Fatalf("Run error = %v, want staged validation error", err)
	}
	if store.savedGlobal != nil || store.savedProject != nil {
		t.Fatal("configuration was written for an empty manual model")
	}
}

func TestConfigurePickerInvalidResultDoesNotPersist(t *testing.T) {
	store := &memoryConfigureStore{globalErr: config.ErrGlobalConfigNotFound, projectErr: config.ErrProjectNotConfigured}
	workflow := NewConfigureWorkflowWithPicker(strings.NewReader("high\n"), &bytes.Buffer{}, func() (string, error) { return "/repo", nil }, store, config.NewDefaultAgentCatalogSource(), func(context.Context, config.AgentCatalog, tui.WizardDefaults, io.Reader, io.Writer) (tui.PickerResult, error) {
		return tui.PickerResult{Agent: config.Agent("unsupported"), Model: "arbitrary-model"}, nil
	})

	err := workflow.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "staged global configuration") {
		t.Fatalf("Run error = %v, want staged validation error", err)
	}
	if store.savedGlobal != nil || store.savedProject != nil {
		t.Fatal("configuration was written after invalid picker result")
	}
}

func TestConfigurePickerFallsBackForNonTTY(t *testing.T) {
	store := &memoryConfigureStore{globalErr: config.ErrGlobalConfigNotFound, projectErr: config.ErrProjectNotConfigured}
	workflow := NewConfigureWorkflowWithPicker(strings.NewReader("codex\ngpt-5\nhigh\n"), &bytes.Buffer{}, func() (string, error) { return "/repo", nil }, store, config.NewDefaultAgentCatalogSource(), func(context.Context, config.AgentCatalog, tui.WizardDefaults, io.Reader, io.Writer) (tui.PickerResult, error) {
		return tui.PickerResult{}, tui.ErrPickerNonInteractive
	})
	if err := workflow.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.savedGlobal == nil || store.savedGlobal.Defaults.Agent != config.AgentCodex {
		t.Fatalf("saved=%#v", store.savedGlobal)
	}
}

func TestConfigureCatalogContextFailureDoesNotPersist(t *testing.T) {
	store := &memoryConfigureStore{globalErr: config.ErrGlobalConfigNotFound, projectErr: config.ErrProjectNotConfigured}
	workflow := NewConfigureWorkflowWithPicker(strings.NewReader(""), &bytes.Buffer{}, func() (string, error) { return "/repo", nil }, store, catalogSourceFunc(func(context.Context) (config.AgentCatalog, error) {
		return config.AgentCatalog{}, context.Canceled
	}), func(context.Context, config.AgentCatalog, tui.WizardDefaults, io.Reader, io.Writer) (tui.PickerResult, error) {
		t.Fatal("picker called after catalog context failure")
		return tui.PickerResult{}, nil
	})

	err := workflow.Run(context.Background())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
	if store.savedGlobal != nil || store.savedProject != nil {
		t.Fatal("configuration was written after catalog context cancellation")
	}
}

func TestConfigureCatalogFailureIsClearAndAtomic(t *testing.T) {
	store := &memoryConfigureStore{globalErr: config.ErrGlobalConfigNotFound, projectErr: config.ErrProjectNotConfigured}
	sentinel := errors.New("catalog unavailable")
	source := catalogSourceFunc(func(context.Context) (config.AgentCatalog, error) { return config.AgentCatalog{}, sentinel })
	workflow := NewConfigureWorkflowWithPicker(strings.NewReader(""), &bytes.Buffer{}, func() (string, error) { return "/repo", nil }, store, source, func(context.Context, config.AgentCatalog, tui.WizardDefaults, io.Reader, io.Writer) (tui.PickerResult, error) {
		t.Fatal("picker called")
		return tui.PickerResult{}, nil
	})
	err := workflow.Run(context.Background())
	if !errors.Is(err, sentinel) || !strings.Contains(err.Error(), "load agent/model catalog") || store.savedGlobal != nil {
		t.Fatalf("err=%v saved=%#v", err, store.savedGlobal)
	}
}

type catalogSourceFunc func(context.Context) (config.AgentCatalog, error)

func (f catalogSourceFunc) AgentCatalog(ctx context.Context) (config.AgentCatalog, error) {
	return f(ctx)
}

func TestAppConfigureCompositionInvokesInjectedPickerAndPersists(t *testing.T) {
	store := &memoryConfigureStore{globalErr: config.ErrGlobalConfigNotFound, projectErr: config.ErrProjectNotConfigured}
	called := false
	app := New(
		WithInput(strings.NewReader("high\n")), WithWorkingDirectory(func() (string, error) { return "/repo", nil }), WithConfigStore(store),
		WithAgentCatalogSource(config.NewDefaultAgentCatalogSource()),
		WithConfigurePicker(func(_ context.Context, _ config.AgentCatalog, _ tui.WizardDefaults, _ io.Reader, _ io.Writer) (tui.PickerResult, error) {
			called = true
			return tui.PickerResult{Agent: config.AgentClaude, Model: "opus", Effort: config.EffortHigh}, nil
		}),
	)
	var stdout, stderr bytes.Buffer
	if code := app.Run(context.Background(), []string{"configure"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if !called || store.savedGlobal == nil || store.savedGlobal.Defaults.Model != "opus" {
		t.Fatalf("called=%v saved=%#v", called, store.savedGlobal)
	}
}

func TestConfigureWizardReceivesCurrentDefaultsAndAppliesPhaseToggles(t *testing.T) {
	t.Skip("required phases now expose editable tuples but not structure toggles")
	disabled := false
	store := configuredMemoryStore()
	store.project.PhaseOverrides = map[config.Phase]config.PhaseOverride{
		config.PhaseCI: {Enabled: &disabled},
	}
	var calls int
	picker := func(_ context.Context, catalog config.AgentCatalog, defaults tui.WizardDefaults, _ io.Reader, _ io.Writer) (tui.PickerResult, error) {
		calls++
		if _, ok := catalog.Lookup(config.AgentCodex); !ok {
			t.Fatalf("wizard catalog is missing codex: %#v", catalog.Entries())
		}
		if defaults.Agent != config.AgentClaude || defaults.Model != "sonnet" || defaults.Effort != config.EffortMedium {
			t.Fatalf("wizard defaults = %#v, want current global defaults", defaults)
		}
		if len(defaults.Phases) != len(pipeline.DefaultPipeline().Phases()) {
			t.Fatalf("wizard phases = %#v, want the full pipeline in order", defaults.Phases)
		}
		toggleable := 0
		selected := make([]tui.PhaseState, len(defaults.Phases))
		for i, state := range defaults.Phases {
			if state.Locked {
				// Grooming and the fixed pipeline steps accept per-phase
				// agent/model/effort overrides even though they stay locked.
				if state.Phase != "" && state.Phase != config.PhaseGrooming && !config.IsFixedPhase(state.Phase) {
					t.Fatalf("locked pipeline step carries an unexpected config key: %#v", state)
				}
				selected[i] = state
				continue
			}
			toggleable++
			if state.Phase == config.PhaseCI && state.Enabled {
				t.Fatalf("ci should be prefilled disabled from the project override: %#v", defaults.Phases)
			}
			if state.Phase != config.PhaseCI && !state.Enabled {
				t.Fatalf("%s should be prefilled enabled: %#v", state.Phase, defaults.Phases)
			}
			selected[i] = state
			switch state.Phase {
			case config.PhaseQA:
				selected[i].Enabled = false // toggle off
			case config.PhaseCI:
				selected[i].Enabled = true // toggle back on
			}
		}
		if toggleable != len(configurablePhases()) {
			t.Fatalf("toggleable phases = %d, want %d", toggleable, len(configurablePhases()))
		}
		return tui.PickerResult{Agent: config.AgentCodex, Model: "gpt-5", Effort: config.EffortHigh, Phases: selected}, nil
	}
	workflow := NewConfigureWorkflowWithPicker(strings.NewReader(""), &bytes.Buffer{}, func() (string, error) { return "/repo", nil }, store, config.NewDefaultAgentCatalogSource(), picker)
	if err := workflow.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("wizard calls = %d, want exactly one for the whole configuration", calls)
	}
	if got := store.savedGlobal.Defaults; got.Agent != config.AgentCodex || got.Model != "gpt-5" || got.Effort != config.EffortHigh {
		t.Fatalf("global defaults = %#v", got)
	}
	qa := store.savedProject.PhaseOverrides[config.PhaseQA]
	if qa.Enabled == nil || *qa.Enabled {
		t.Fatalf("qa override = %#v, want disabled", qa)
	}
	ci := store.savedProject.PhaseOverrides[config.PhaseCI]
	if ci.Enabled == nil || !*ci.Enabled {
		t.Fatalf("ci override = %#v, want re-enabled", ci)
	}
	for _, phase := range []config.Phase{config.PhasePlanning, config.PhaseBuildChecker, config.PhasePR, config.PhaseGrooming} {
		if _, ok := store.savedProject.PhaseOverrides[phase]; ok {
			t.Errorf("unexpected override written for untouched phase %s", phase)
		}
	}
}

func TestConfigureWizardKeepsExistingPhaseAgentOverridesWhenToggling(t *testing.T) {
	t.Skip("complete configurations store whole phase tuples")
	store := configuredMemoryStore()
	store.project.PhaseOverrides = map[config.Phase]config.PhaseOverride{
		config.PhaseQA: {AgentSettingsOverride: config.AgentSettingsOverride{Model: "qa-provider-model"}},
	}
	picker := func(_ context.Context, _ config.AgentCatalog, defaults tui.WizardDefaults, _ io.Reader, _ io.Writer) (tui.PickerResult, error) {
		selected := append([]tui.PhaseState(nil), defaults.Phases...)
		for i := range selected {
			if selected[i].Phase == config.PhaseQA {
				selected[i].Enabled = false
			}
		}
		return tui.PickerResult{Agent: defaults.Agent, Model: defaults.Model, Effort: defaults.Effort, Phases: selected}, nil
	}
	workflow := NewConfigureWorkflowWithPicker(strings.NewReader(""), &bytes.Buffer{}, func() (string, error) { return "/repo", nil }, store, config.NewDefaultAgentCatalogSource(), picker)
	if err := workflow.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	qa := store.savedProject.PhaseOverrides[config.PhaseQA]
	if qa.Enabled == nil || *qa.Enabled || qa.Model != "qa-provider-model" {
		t.Fatalf("qa override = %#v, want disabled with the per-phase model preserved", qa)
	}
}

func TestConfigureReconfigurationPickerCancellationDoesNotPersist(t *testing.T) {
	store := configuredMemoryStore()
	beforeGlobal, beforeProject := store.global, cloneProjectConfig(store.project)
	workflow := NewConfigureWorkflowWithPicker(strings.NewReader(""), &bytes.Buffer{}, func() (string, error) { return "/repo", nil }, store, config.NewDefaultAgentCatalogSource(), func(context.Context, config.AgentCatalog, tui.WizardDefaults, io.Reader, io.Writer) (tui.PickerResult, error) {
		return tui.PickerResult{}, tui.ErrPickerCancelled
	})
	err := workflow.Run(context.Background())
	if !errors.Is(err, tui.ErrPickerCancelled) || store.savedGlobal != nil || store.savedProject != nil {
		t.Fatalf("err=%v savedGlobal=%#v savedProject=%#v", err, store.savedGlobal, store.savedProject)
	}
	if !reflect.DeepEqual(store.global, beforeGlobal) || !reflect.DeepEqual(store.project, beforeProject) {
		t.Fatal("picker cancellation changed persisted source values")
	}
}

func TestConfigureUnconfiguredProjectFolderReportsProjectReady(t *testing.T) {
	store := configuredMemoryStore()
	store.projectErr = config.ErrProjectNotConfigured
	var output bytes.Buffer
	err := NewConfigureWorkflow(strings.NewReader("\n\n\n\n"), &output, func() (string, error) { return "/repo", nil }, store).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if store.savedProject == nil {
		t.Fatal("project configuration was not saved")
	}
	if !strings.Contains(output.String(), "Configuration saved. This project is ready in .gg/projects.") {
		t.Errorf("output = %q", output.String())
	}
}

func TestConfigureReconfigurationFallsBackForNonTTY(t *testing.T) {
	store := configuredMemoryStore()
	input := "\n\n\n" + strings.Repeat("\n\n\nmedium\n", len(config.RemovablePhases()))
	workflow := NewConfigureWorkflowWithPicker(strings.NewReader(input), &bytes.Buffer{}, func() (string, error) { return "/repo", nil }, store, config.NewDefaultAgentCatalogSource(), func(context.Context, config.AgentCatalog, tui.WizardDefaults, io.Reader, io.Writer) (tui.PickerResult, error) {
		return tui.PickerResult{}, tui.ErrPickerNonInteractive
	})
	if err := workflow.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := store.savedGlobal.Defaults; got.Agent != config.AgentClaude || got.Model != "sonnet" {
		t.Fatalf("global defaults changed on fallback: %#v", got)
	}
}

func TestAppConfigureReconfigurationUsesInjectedPicker(t *testing.T) {
	store := configuredMemoryStore()
	called := false
	app := New(
		WithInput(strings.NewReader("high\n"+strings.Repeat("\nmedium\n", len(config.RemovablePhases())))),
		WithWorkingDirectory(func() (string, error) { return "/repo", nil }),
		WithConfigStore(store),
		WithAgentCatalogSource(config.NewDefaultAgentCatalogSource()),
		WithConfigurePicker(func(_ context.Context, _ config.AgentCatalog, _ tui.WizardDefaults, _ io.Reader, _ io.Writer) (tui.PickerResult, error) {
			called = true
			return tui.PickerResult{Agent: config.AgentClaude, Model: "opus", Effort: config.EffortHigh}, nil
		}),
	)
	var stdout, stderr bytes.Buffer
	if code := app.Run(context.Background(), []string{"configure"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if !called || store.savedGlobal.Defaults.Model != "opus" {
		t.Fatalf("called=%v saved=%#v", called, store.savedGlobal)
	}
}

func TestConfigureWizardStagesPerPhaseSettingsOverrides(t *testing.T) {
	t.Skip("complete configurations store whole phase tuples")
	store := configuredMemoryStore()
	picker := func(_ context.Context, _ config.AgentCatalog, defaults tui.WizardDefaults, _ io.Reader, _ io.Writer) (tui.PickerResult, error) {
		selected := append([]tui.PhaseState(nil), defaults.Phases...)
		for i := range selected {
			switch selected[i].Phase {
			case config.PhaseQA:
				selected[i].Agent, selected[i].Model, selected[i].Effort = config.AgentCodex, "gpt-5", config.EffortHigh
			case config.PhaseGrooming:
				if !selected[i].Locked {
					t.Fatalf("grooming should stay locked: %#v", selected[i])
				}
				selected[i].Model = "opus"
			case config.PhaseDevelopment:
				if !selected[i].Locked {
					t.Fatalf("development should stay locked: %#v", selected[i])
				}
				selected[i].Effort = config.EffortHigh
			}
		}
		return tui.PickerResult{Agent: defaults.Agent, Model: defaults.Model, Effort: defaults.Effort, Phases: selected}, nil
	}
	workflow := NewConfigureWorkflowWithPicker(strings.NewReader(""), &bytes.Buffer{}, func() (string, error) { return "/repo", nil }, store, config.NewDefaultAgentCatalogSource(), picker)
	if err := workflow.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	qa := store.savedProject.PhaseOverrides[config.PhaseQA]
	if qa.Agent != config.AgentCodex || qa.Model != "gpt-5" || qa.Effort != config.EffortHigh || qa.Enabled != nil {
		t.Fatalf("qa override = %#v, want codex/gpt-5/high with no enabled override", qa)
	}
	grooming := store.savedProject.PhaseOverrides[config.PhaseGrooming]
	if grooming.Model != "opus" || grooming.Agent != "" || grooming.Effort != "" {
		t.Fatalf("grooming override = %#v, want only the model pinned", grooming)
	}
	development := store.savedProject.PhaseOverrides[config.PhaseDevelopment]
	if development.Effort != config.EffortHigh || development.Enabled != nil {
		t.Fatalf("development override = %#v, want effort pinned without an enabled override", development)
	}
	for _, phase := range []config.Phase{config.PhasePlanning, config.PhaseBuildChecker, config.PhasePR, config.PhaseCI, config.PhaseAcceptanceCriteria, config.PhaseRebase, config.PhaseTestDocument} {
		if _, ok := store.savedProject.PhaseOverrides[phase]; ok {
			t.Errorf("unexpected override for untouched phase %s", phase)
		}
	}
}

func TestConfigureWizardClearsPinWhenPhaseMatchesNewDefaults(t *testing.T) {
	store := configuredMemoryStore()
	store.project.PhaseOverrides = map[config.Phase]config.PhaseOverride{
		config.PhaseQA: {AgentSettingsOverride: config.AgentSettingsOverride{Model: "opus"}},
	}
	picker := func(_ context.Context, _ config.AgentCatalog, defaults tui.WizardDefaults, _ io.Reader, _ io.Writer) (tui.PickerResult, error) {
		selected := append([]tui.PhaseState(nil), defaults.Phases...)
		for i := range selected {
			if selected[i].Phase == config.PhaseQA {
				if selected[i].Model != "opus" {
					t.Fatalf("qa prefill = %#v, want the pinned model", selected[i])
				}
				// Re-picking the global default clears the pin (the wizard
				// stores an empty field for inherited values).
				selected[i].Model = ""
			}
		}
		return tui.PickerResult{Agent: defaults.Agent, Model: defaults.Model, Effort: defaults.Effort, Phases: selected}, nil
	}
	workflow := NewConfigureWorkflowWithPicker(strings.NewReader(""), &bytes.Buffer{}, func() (string, error) { return "/repo", nil }, store, config.NewDefaultAgentCatalogSource(), picker)
	if err := workflow.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if override, ok := store.savedProject.PhaseOverrides[config.PhaseQA]; ok {
		t.Fatalf("qa override = %#v, want removed after re-picking the defaults", override)
	}
}
