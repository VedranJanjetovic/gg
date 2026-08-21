package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func validGlobal() GlobalConfig {
	return GlobalConfig{Version: CurrentSchemaVersion, Defaults: AgentSettings{Agent: AgentCodex, Model: "gpt-5", Effort: EffortHigh}}
}
func validProject() ProjectConfig {
	disabled := false
	return ProjectConfig{Version: CurrentSchemaVersion, Defaults: AgentSettingsOverride{Model: "project-model"}, PhaseOverrides: map[Phase]PhaseOverride{PhaseQA: {Enabled: &disabled, AgentSettingsOverride: AgentSettingsOverride{Agent: AgentClaude}}}}
}
func testStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	return newStore(func() (string, error) { return dir, nil }), dir
}

func TestStoreGlobalRoundTripUsesStableSecurePath(t *testing.T) {
	store, configDir := testStore(t)
	cfg := validGlobal()
	if err := store.SaveGlobal(cfg); err != nil {
		t.Fatalf("SaveGlobal: %v", err)
	}
	path := filepath.Join(configDir, "gg", "config.yaml")
	gotPath, err := store.GlobalConfigPath()
	if err != nil || gotPath != path {
		t.Fatalf("GlobalConfigPath() = %q, %v; want %q", gotPath, err, path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat global config: %v", err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Errorf("global config mode = %o, want 600", got)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	wantYAML := "version: 1\ndefaults:\n  agent: codex\n  model: gpt-5\n  effort: high\n"
	if string(data) != wantYAML {
		t.Errorf("persisted YAML =\n%s\nwant:\n%s", data, wantYAML)
	}
	got, err := store.LoadGlobal()
	if err != nil {
		t.Fatalf("LoadGlobal: %v", err)
	}
	if !reflect.DeepEqual(got, cfg) {
		t.Errorf("LoadGlobal() = %#v, want %#v", got, cfg)
	}
}

func TestStoreProjectRoundTripCreatesRuntimeDirectoryAndDetectsConfiguration(t *testing.T) {
	store, _ := testStore(t)
	root := t.TempDir()
	cfg := validProject()
	configured, err := store.IsConfigured(root)
	if err != nil || configured {
		t.Fatalf("IsConfigured before save = %v, %v; want false, nil", configured, err)
	}
	if err := store.SaveProject(root, cfg); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}
	for _, path := range []string{store.ProjectConfigPath(root), store.ProjectRuntimeDir(root)} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("required path %q: %v", path, err)
		}
	}
	info, err := os.Stat(store.ProjectConfigPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("project config mode = %o, want 600", info.Mode().Perm())
	}
	configured, err = store.IsConfigured(root)
	if err != nil || !configured {
		t.Fatalf("IsConfigured after save = %v, %v; want true, nil", configured, err)
	}
	got, err := store.LoadProject(root)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	if !reflect.DeepEqual(got, cfg) {
		t.Errorf("LoadProject() = %#v, want %#v", got, cfg)
	}
}

func TestStoreSecuresExistingConfigurationDirectories(t *testing.T) {
	store, configHome := testStore(t)
	root := t.TempDir()
	globalDir := filepath.Join(configHome, "gg")
	projectDir := filepath.Join(root, ".gg")
	runtimeDir := store.ProjectRuntimeDir(root)
	for _, path := range []string{globalDir, projectDir, runtimeDir} {
		if err := os.MkdirAll(path, 0777); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0777); err != nil {
			t.Fatal(err)
		}
	}

	if err := store.SaveGlobal(validGlobal()); err != nil {
		t.Fatalf("SaveGlobal: %v", err)
	}
	if err := store.SaveProject(root, validProject()); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}

	for _, path := range []string{globalDir, projectDir, runtimeDir} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0700 {
			t.Errorf("directory %q mode = %o, want 700", path, got)
		}
	}
}

func TestStoreProjectValidationFailurePreservesExistingFile(t *testing.T) {
	store, _ := testStore(t)
	root := t.TempDir()
	original := validProject()
	if err := store.SaveProject(root, original); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(store.ProjectConfigPath(root))
	if err != nil {
		t.Fatal(err)
	}

	invalid := original
	invalid.PhaseOverrides = map[Phase]PhaseOverride{"unknown": {}}
	if err := store.SaveProject(root, invalid); err == nil {
		t.Fatal("SaveProject invalid config succeeded")
	}
	after, err := os.ReadFile(store.ProjectConfigPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatal("invalid SaveProject changed the existing file")
	}
}

func TestStoreSaveConfigurationPreflightsProjectBeforeGlobalWrite(t *testing.T) {
	store, _ := testStore(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".gg"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.ProjectRuntimeDir(root), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}

	err := store.SaveConfiguration(root, validGlobal(), validProject())
	if err == nil || !strings.Contains(err.Error(), "prepare project runtime directory") {
		t.Fatalf("SaveConfiguration error = %v, want project preflight failure", err)
	}
	globalPath, pathErr := store.GlobalConfigPath()
	if pathErr != nil {
		t.Fatal(pathErr)
	}
	if _, err := os.Stat(globalPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("global config exists after preflight failure: %v", err)
	}
	if _, err := os.Stat(store.ProjectConfigPath(root)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("project config exists after preflight failure: %v", err)
	}
}

func TestStoreSaveConfigurationRejectsProjectDirectoryConflictBeforeGlobalWrite(t *testing.T) {
	store, _ := testStore(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".gg"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}

	err := store.SaveConfiguration(root, validGlobal(), validProject())
	if err == nil || !strings.Contains(err.Error(), "prepare project configuration directory") {
		t.Fatalf("SaveConfiguration error = %v, want project directory preflight failure", err)
	}
	globalPath, pathErr := store.GlobalConfigPath()
	if pathErr != nil {
		t.Fatal(pathErr)
	}
	if _, err := os.Stat(globalPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("global config exists after project directory preflight failure: %v", err)
	}
}

func TestStoreSaveConfigurationRejectsSymlinkedProjectDirectoryWithoutOutsideMutation(t *testing.T) {
	store, _ := testStore(t)
	if err := store.SaveGlobal(validGlobal()); err != nil {
		t.Fatal(err)
	}
	globalPath, err := store.GlobalConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	globalBefore, err := os.ReadFile(globalPath)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Chmod(outside, 0755); err != nil {
		t.Fatal(err)
	}
	outsideConfig := filepath.Join(outside, configFileName)
	original := []byte("outside must remain unchanged\n")
	if err := os.WriteFile(outsideConfig, original, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, configDirectoryName)); err != nil {
		t.Fatal(err)
	}

	err = store.SaveConfiguration(root, validGlobal(), validProject())
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("SaveConfiguration error = %v, want symlink rejection", err)
	}
	globalAfter, readErr := os.ReadFile(globalPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !reflect.DeepEqual(globalAfter, globalBefore) {
		t.Fatalf("global config changed: got %q, want %q", globalAfter, globalBefore)
	}
	after, readErr := os.ReadFile(outsideConfig)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !reflect.DeepEqual(after, original) {
		t.Fatalf("outside file changed: got %q, want %q", after, original)
	}
	info, statErr := os.Stat(outside)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if got := info.Mode().Perm(); got != 0755 {
		t.Fatalf("outside directory mode = %o, want unchanged 755", got)
	}
	if _, err := os.Stat(filepath.Join(outside, projectsDirectory)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outside runtime directory was created: %v", err)
	}
}

func TestStoreSaveConfigurationRejectsCanonicalGlobalProjectCollisionBeforeMutation(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, configDirectoryName)
	if err := os.Mkdir(projectDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(projectDir, filepath.Join(root, "gg")); err != nil {
		t.Fatal(err)
	}
	store := newStore(func() (string, error) { return root, nil })
	path := store.ProjectConfigPath(root)
	original := []byte("collision must remain unchanged\n")
	if err := os.WriteFile(path, original, 0644); err != nil {
		t.Fatal(err)
	}

	err := store.SaveConfiguration(root, validGlobal(), validProject())
	if err == nil || !strings.Contains(err.Error(), "same canonical path") {
		t.Fatalf("SaveConfiguration error = %v, want canonical collision", err)
	}
	after, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !reflect.DeepEqual(after, original) {
		t.Fatalf("colliding config changed: got %q, want %q", after, original)
	}
	info, statErr := os.Stat(projectDir)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if got := info.Mode().Perm(); got != 0755 {
		t.Fatalf("project directory mode = %o, want unchanged 755", got)
	}
}

func TestStoreSaveConfigurationRejectsSymlinkedProjectChildren(t *testing.T) {
	for _, child := range []string{configFileName, projectsDirectory} {
		t.Run(child, func(t *testing.T) {
			store, _ := testStore(t)
			root := t.TempDir()
			projectDir := filepath.Join(root, configDirectoryName)
			outside := t.TempDir()
			if err := os.Mkdir(projectDir, 0700); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(outside, child)
			if child == projectsDirectory {
				if err := os.Mkdir(target, 0755); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(target, []byte("outside\n"), 0644); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(projectDir, child)); err != nil {
				t.Fatal(err)
			}

			if err := store.SaveConfiguration(root, validGlobal(), validProject()); err == nil || !strings.Contains(err.Error(), "symbolic link") {
				t.Fatalf("SaveConfiguration error = %v, want symlink rejection", err)
			}
			info, err := os.Stat(target)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != 0755 && child == projectsDirectory {
				t.Fatalf("outside runtime mode = %o, want unchanged 755", got)
			}
			if got := info.Mode().Perm(); got != 0644 && child == configFileName {
				t.Fatalf("outside config mode = %o, want unchanged 644", got)
			}
		})
	}
}

func TestStoreSaveConfigurationAllowsSymlinkedProjectRoot(t *testing.T) {
	store, _ := testStore(t)
	parent := t.TempDir()
	realRoot := filepath.Join(parent, "real-project")
	linkedRoot := filepath.Join(parent, "linked-project")
	if err := os.Mkdir(realRoot, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realRoot, linkedRoot); err != nil {
		t.Fatal(err)
	}

	if err := store.SaveConfiguration(linkedRoot, validGlobal(), validProject()); err != nil {
		t.Fatalf("SaveConfiguration through symlinked project root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(realRoot, configDirectoryName, configFileName)); err != nil {
		t.Fatalf("real project config: %v", err)
	}
	if _, err := store.LoadProject(linkedRoot); err != nil {
		t.Fatalf("LoadProject through symlinked root: %v", err)
	}
}

func TestStoreSaveConfigurationRemovesFirstGlobalWhenProjectSaveFails(t *testing.T) {
	store, _ := testStore(t)
	root := t.TempDir()
	if err := os.MkdirAll(store.ProjectRuntimeDir(root), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(store.ProjectConfigPath(root), 0700); err != nil {
		t.Fatal(err)
	}

	err := store.SaveConfiguration(root, validGlobal(), validProject())
	if err == nil || !strings.Contains(err.Error(), "save project configuration") {
		t.Fatalf("SaveConfiguration error = %v, want project save failure", err)
	}
	globalPath, pathErr := store.GlobalConfigPath()
	if pathErr != nil {
		t.Fatal(pathErr)
	}
	if _, err := os.Stat(globalPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("first global config remains after rollback: %v", err)
	}
}

func TestStoreSaveConfigurationRollsBackGlobalBytesWhenProjectSaveFails(t *testing.T) {
	store, _ := testStore(t)
	root := t.TempDir()
	if err := store.SaveGlobal(validGlobal()); err != nil {
		t.Fatal(err)
	}
	globalPath, err := store.GlobalConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	before := []byte("# preserve this exact representation\nversion: 1\ndefaults: {agent: codex, model: gpt-5, effort: high}\n")
	if err := os.WriteFile(globalPath, before, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(store.ProjectRuntimeDir(root), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(store.ProjectConfigPath(root), 0700); err != nil {
		t.Fatal(err)
	}
	updated := validGlobal()
	updated.Defaults.Model = "new-model"

	err = store.SaveConfiguration(root, updated, validProject())
	if err == nil || !strings.Contains(err.Error(), "save project configuration") {
		t.Fatalf("SaveConfiguration error = %v, want project save failure", err)
	}
	after, err := os.ReadFile(globalPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("global bytes changed after rollback:\ngot:  %q\nwant: %q", after, before)
	}
	info, err := os.Stat(globalPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("restored global mode = %o, want 600", info.Mode().Perm())
	}
}

func TestStoreConfiguredDetectionRejectsBrokenProject(t *testing.T) {
	store, _ := testStore(t)
	root := t.TempDir()
	path := store.ProjectConfigPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("version: 1\nunknown: true\n"), 0600); err != nil {
		t.Fatal(err)
	}

	configured, err := store.IsConfigured(root)
	if err == nil || configured {
		t.Fatalf("IsConfigured() = %v, %v; want false and malformed-config error", configured, err)
	}
	if errors.Is(err, ErrProjectNotConfigured) {
		t.Fatalf("malformed project was reported as missing: %v", err)
	}
}

func TestStoreReturnsExplicitMissingConfigurationErrors(t *testing.T) {
	store, _ := testStore(t)
	if _, err := store.LoadGlobal(); !errors.Is(err, ErrGlobalConfigNotFound) {
		t.Errorf("LoadGlobal error = %v, want ErrGlobalConfigNotFound", err)
	}
	if _, err := store.LoadProject(t.TempDir()); !errors.Is(err, ErrProjectNotConfigured) {
		t.Errorf("LoadProject error = %v, want ErrProjectNotConfigured", err)
	}
}

func TestStoreRejectsInvalidYAMLAndConfiguration(t *testing.T) {
	tests := []struct{ name, contents, want string }{
		{"unknown field", "version: 1\ndefaults:\n  agent: codex\n  model: gpt\n  effort: high\nunknown: true\n", "field unknown not found"},
		{"malformed", "version: [\n", "did not find expected node content"},
		{"multiple documents", "version: 1\ndefaults: {agent: codex, model: gpt, effort: high}\n---\nversion: 1\n", "multiple YAML documents"},
		{"invalid value", "version: 1\ndefaults: {agent: other, model: gpt, effort: high}\n", "unsupported agent"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, dir := testStore(t)
			path := filepath.Join(dir, "gg", "config.yaml")
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(tt.contents), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := store.LoadGlobal()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("LoadGlobal error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestStoreValidatesBeforeReplacingExistingConfiguration(t *testing.T) {
	store, dir := testStore(t)
	original := validGlobal()
	if err := store.SaveGlobal(original); err != nil {
		t.Fatal(err)
	}
	invalid := original
	invalid.Version++
	if err := store.SaveGlobal(invalid); err == nil {
		t.Fatal("SaveGlobal invalid config succeeded")
	}
	got, err := store.LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, original) {
		t.Errorf("existing config replaced: got %#v, want %#v", got, original)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "gg"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Errorf("temporary file leaked: %s", entry.Name())
		}
	}
}

func TestStorePropagatesUserConfigDirectoryErrors(t *testing.T) {
	sentinel := errors.New("no config home")
	store := newStore(func() (string, error) { return "", sentinel })
	if _, err := store.GlobalConfigPath(); !errors.Is(err, sentinel) {
		t.Fatalf("GlobalConfigPath error = %v, want wrapped sentinel", err)
	}
	if err := store.SaveGlobal(validGlobal()); !errors.Is(err, sentinel) {
		t.Fatalf("SaveGlobal error = %v, want wrapped sentinel", err)
	}
}

func TestNewStoreRespectsXDGConfigHome(t *testing.T) {
	xdgHome := filepath.Join(t.TempDir(), "xdg")
	t.Setenv("XDG_CONFIG_HOME", xdgHome)

	got, err := NewStore().GlobalConfigPath()
	if err != nil {
		t.Fatalf("GlobalConfigPath: %v", err)
	}
	want := filepath.Join(xdgHome, "gg", "config.yaml")
	if got != want {
		t.Errorf("GlobalConfigPath() = %q, want %q", got, want)
	}
}

func TestStoreUsesCanonicalValidation(t *testing.T) {
	store, _ := testStore(t)
	cfg := validGlobal()
	cfg.Defaults.Model = " \t"

	err := store.SaveGlobal(cfg)
	if err == nil || !strings.Contains(err.Error(), "defaults.model") {
		t.Fatalf("SaveGlobal error = %v, want canonical defaults.model validation error", err)
	}
}

func TestStoreNormalizesProjectPhaseAliases(t *testing.T) {
	store, _ := testStore(t)
	root := t.TempDir()
	disabled := false
	cfg := ProjectConfig{
		Version: CurrentSchemaVersion,
		PhaseOverrides: map[Phase]PhaseOverride{
			PhaseLintingAlias: {Enabled: &disabled},
		},
	}

	if err := store.SaveProject(root, cfg); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}
	if _, exists := cfg.PhaseOverrides[PhaseLintingAlias]; !exists {
		t.Fatal("SaveProject mutated its input")
	}
	data, err := os.ReadFile(store.ProjectConfigPath(root))
	if err != nil {
		t.Fatalf("read project config: %v", err)
	}
	if strings.Contains(string(data), "linting:") || !strings.Contains(string(data), "build_checker:") {
		t.Fatalf("persisted phase overrides were not normalized:\n%s", data)
	}

	got, err := store.LoadProject(root)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	if _, exists := got.PhaseOverrides[PhaseLintingAlias]; exists {
		t.Fatal("LoadProject returned the linting alias")
	}
	if override, exists := got.PhaseOverrides[PhaseBuildChecker]; !exists || override.Enabled == nil || *override.Enabled {
		t.Fatalf("LoadProject build_checker override = %#v, want enabled=false", override)
	}
}

func TestSaveConfigurationRegistersFolderInGlobalRegistry(t *testing.T) {
	store, root := testStore(t)
	global := validGlobal()
	if err := store.SaveConfiguration(root, global, ProjectConfig{Version: CurrentSchemaVersion}); err != nil {
		t.Fatal(err)
	}
	// Registering twice must not duplicate.
	if err := store.SaveConfiguration(root, global, ProjectConfig{Version: CurrentSchemaVersion}); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, folder := range loaded.Folders {
		if folder == root {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("registry = %v, want %q exactly once", loaded.Folders, root)
	}
}
