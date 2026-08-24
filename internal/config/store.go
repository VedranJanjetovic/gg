package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	configDirectoryName = ".gg"
	configFileName      = "config.yaml"
	projectsDirectory   = "projects"
)

// ProjectConfigClassification describes the persisted folder schema without
// changing the legacy LoadProject API used by existing pipeline snapshots.
type ProjectConfigClassification string

const (
	ProjectConfigComplete          ProjectConfigClassification = "complete"
	ProjectConfigMigrationRequired ProjectConfigClassification = "migration_required"
	ProjectConfigMalformed         ProjectConfigClassification = "malformed"
)

// ProjectConfigLoad is the explicit migration-gate result for folder config.
// A migration-required result is readable for wizard prefilling but must not
// be used as the source of a new project until it is saved in complete form.
type ProjectConfigLoad struct {
	Config         ProjectConfig
	Classification ProjectConfigClassification
	ValidationErr  error
}

// ClassifyProjectConfig identifies complete, legacy sparse, and malformed
// in-memory folder configurations without rewriting or resolving them.
func ClassifyProjectConfig(cfg ProjectConfig) ProjectConfigClassification {
	if cfg.Phases == nil {
		if err := ValidateProjectConfig(cfg); err == nil {
			return ProjectConfigMigrationRequired
		}
		return ProjectConfigMalformed
	}
	if err := ValidateCompleteProjectConfig(cfg); err != nil {
		if isMigrationShape(cfg) {
			return ProjectConfigMigrationRequired
		}
		return ProjectConfigMalformed
	}
	return ProjectConfigComplete
}

// isMigrationShape identifies older complete-shaped data that is structurally
// understandable but cannot be used as a self-contained configuration yet.
// It deliberately rejects invalid values and invalid required-phase state so a
// malformed file is not given a migration escape hatch.
func isMigrationShape(cfg ProjectConfig) bool {
	if cfg.Version == 0 || cfg.Version > CompleteSchemaVersion {
		return false
	}
	if err := validateGitOpsOverride(cfg.GitOps, "gitops"); err != nil {
		return false
	}
	if cfg.PhaseOverrides != nil {
		if !validPartialSettingsOverride(cfg.Defaults) || validatePhaseOverrides(cfg.PhaseOverrides) != nil {
			return false
		}
	}
	if completeSettingsShape(cfg.Defaults) {
		if err := validateCompleteSettings(cfg.Defaults, "defaults"); err != nil {
			return false
		}
	} else if !validPartialSettingsOverride(cfg.Defaults) {
		return false
	}
	if !validMigrationPhases(cfg.Phases) {
		return false
	}
	return len(cfg.Phases) > 0
}

func validMigrationPhases(phases []PhaseConfig) bool {
	order := CompletePhaseOrder()
	if len(phases) > len(order) {
		return false
	}
	for index, entry := range phases {
		if index >= len(order) || entry.Phase != order[index] || !isSupportedPhase(entry.Phase) {
			return false
		}
		if entry.Required != isRequiredPhase(entry.Phase) {
			return false
		}
		if entry.Required && !entry.Enabled {
			return false
		}
		if completeAgentSettingsShape(entry.AgentSettings) {
			if err := validateCompleteAgentSettings(entry.AgentSettings, "phase.settings"); err != nil {
				return false
			}
		} else if !validPartialAgentSettings(entry.AgentSettings) {
			return false
		}
	}
	return true
}

func completeSettingsShape(settings AgentSettingsOverride) bool {
	return settings.Agent != "" && settings.Model != "" && settings.Effort != "" && settings.Provenance != ""
}

func completeAgentSettingsShape(settings AgentSettings) bool {
	return settings.Agent != "" && settings.Model != "" && settings.Effort != "" && settings.Provenance != ""
}

func validPartialAgentSettings(settings AgentSettings) bool {
	if settings.Model != "" && strings.TrimSpace(settings.Model) == "" {
		return false
	}
	if settings.Agent != "" && settings.Agent != AgentClaude && settings.Agent != AgentCodex {
		return false
	}
	if settings.Effort != "" && settings.Effort != EffortLow && settings.Effort != EffortMedium && settings.Effort != EffortHigh {
		return false
	}
	if settings.Provenance != "" && settings.Provenance != ModelProvenanceCatalog && settings.Provenance != ModelProvenanceManual {
		return false
	}
	return true
}

func validPartialSettingsOverride(settings AgentSettingsOverride) bool {
	if settings.Model != "" && strings.TrimSpace(settings.Model) == "" {
		return false
	}
	if settings.Agent != "" && settings.Agent != AgentClaude && settings.Agent != AgentCodex {
		return false
	}
	if settings.Effort != "" && settings.Effort != EffortLow && settings.Effort != EffortMedium && settings.Effort != EffortHigh {
		return false
	}
	if settings.Provenance != "" && settings.Provenance != ModelProvenanceCatalog && settings.Provenance != ModelProvenanceManual {
		return false
	}
	return true
}

// Store persists global and project configuration as YAML.
type Store struct{ userConfigDir func() (string, error) }

// NewStore constructs a configuration store using the operating system's user configuration directory.
func NewStore() *Store { return newStore(userConfigDir) }
func newStore(userConfigDir func() (string, error)) *Store {
	return &Store{userConfigDir: userConfigDir}
}

func appendUniqueFolder(folders []string, folder string) []string {
	for _, existing := range folders {
		if existing == folder {
			return folders
		}
	}
	return append(folders, folder)
}

// userConfigDir honors XDG_CONFIG_HOME on every platform. os.UserConfigDir
// ignores it on macOS and Windows, which lets sandboxed environments — most
// importantly gg's own test suite, which isolates itself by setting
// XDG_CONFIG_HOME — leak reads and writes into the user's real configuration.
func userConfigDir() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); dir != "" {
		return dir, nil
	}
	return os.UserConfigDir()
}

// GlobalConfigPath returns the stable path to the user's global gg config.
func (s *Store) GlobalConfigPath() (string, error) {
	dir, err := s.userConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user configuration directory: %w", err)
	}
	return filepath.Join(dir, "gg", configFileName), nil
}

// ProjectConfigPath returns the configuration path for projectRoot.
func (s *Store) ProjectConfigPath(root string) string {
	return filepath.Join(root, configDirectoryName, configFileName)
}

// ProjectRuntimeDir returns the directory used for project runtime state.
func (s *Store) ProjectRuntimeDir(root string) string {
	return filepath.Join(root, configDirectoryName, projectsDirectory)
}

// LoadGlobal loads and validates the global configuration.
func (s *Store) LoadGlobal() (GlobalConfig, error) {
	path, err := s.GlobalConfigPath()
	if err != nil {
		return GlobalConfig{}, err
	}
	var cfg GlobalConfig
	if err := readYAML(path, &cfg); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return GlobalConfig{}, ErrGlobalConfigNotFound
		}
		return GlobalConfig{}, fmt.Errorf("load global configuration: %w", err)
	}
	if err := ValidateGlobalConfig(cfg); err != nil {
		return GlobalConfig{}, fmt.Errorf("validate global configuration: %w", err)
	}
	return cfg, nil
}

// SaveGlobal validates and atomically persists the global configuration.
func (s *Store) SaveGlobal(cfg GlobalConfig) error {
	if err := ValidateGlobalConfig(cfg); err != nil {
		return fmt.Errorf("validate global configuration: %w", err)
	}
	path, err := s.GlobalConfigPath()
	if err != nil {
		return err
	}
	if err := writeYAMLAtomic(path, cfg); err != nil {
		return fmt.Errorf("save global configuration: %w", err)
	}
	return nil
}

// LoadProject loads and validates configuration for projectRoot.
func (s *Store) LoadProject(root string) (ProjectConfig, error) {
	loaded, err := s.LoadProjectClassified(root)
	if err != nil {
		return ProjectConfig{}, err
	}
	if loaded.ValidationErr != nil {
		return ProjectConfig{}, fmt.Errorf("validate project configuration: %w", loaded.ValidationErr)
	}
	loaded.Config.PhaseOverrides = NormalizePhaseOverrides(loaded.Config.PhaseOverrides)
	return loaded.Config, nil
}

// LoadProjectClassified loads folder configuration and reports whether it is
// complete or requires explicit reconfiguration. Sparse data is returned
// unchanged, so callers can resolve it only for wizard prefilling.
func (s *Store) LoadProjectClassified(root string) (ProjectConfigLoad, error) {
	var cfg ProjectConfig
	if err := readYAML(s.ProjectConfigPath(root), &cfg); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ProjectConfigLoad{}, ErrProjectNotConfigured
		}
		if strings.Contains(err.Error(), "decode ") {
			return ProjectConfigLoad{Classification: ProjectConfigMalformed, ValidationErr: err}, nil
		}
		return ProjectConfigLoad{}, fmt.Errorf("load project configuration: %w", err)
	}
	classification := ClassifyProjectConfig(cfg)
	var validationErr error
	if classification == ProjectConfigMalformed {
		if cfg.Phases != nil {
			validationErr = ValidateCompleteProjectConfig(cfg)
		} else {
			validationErr = ValidateProjectConfig(cfg)
		}
	}
	return ProjectConfigLoad{Config: cfg, Classification: classification, ValidationErr: validationErr}, nil
}

// SaveProject validates and atomically persists project configuration and creates its runtime directory.
func (s *Store) SaveProject(root string, cfg ProjectConfig) error {
	if err := ValidateProjectConfig(cfg); err != nil {
		return fmt.Errorf("validate project configuration: %w", err)
	}
	cfg.PhaseOverrides = NormalizePhaseOverrides(cfg.PhaseOverrides)
	if err := ensurePrivateDir(s.ProjectRuntimeDir(root)); err != nil {
		return fmt.Errorf("create project runtime directory: %w", err)
	}
	if err := writeYAMLAtomic(s.ProjectConfigPath(root), cfg); err != nil {
		return fmt.Errorf("save project configuration: %w", err)
	}
	return nil
}

// SaveConfiguration coordinates global and project persistence for configure.
// Project paths are prepared before global state changes. If the later project
// save fails, the previous global file is restored byte-for-byte (or removed
// when this was a first-time configuration).
func (s *Store) SaveConfiguration(root string, global GlobalConfig, project ProjectConfig) error {
	global = global.Clone()
	if err := ValidateGlobalConfig(global); err != nil {
		return fmt.Errorf("validate global configuration: %w", err)
	}
	if err := ValidateProjectConfig(project); err != nil {
		return fmt.Errorf("validate project configuration: %w", err)
	}
	globalPath, err := s.GlobalConfigPath()
	if err != nil {
		return err
	}
	if err := s.preflightConfigurationPaths(root, globalPath); err != nil {
		return err
	}
	if err := s.prepareProject(root); err != nil {
		return err
	}
	previousGlobal, globalExisted, err := snapshotFile(globalPath)
	if err != nil {
		return fmt.Errorf("snapshot global configuration: %w", err)
	}
	// Register the folder machine-wide so the global project view can list
	// its projects from any directory.
	if canonical, canonicalErr := filepath.Abs(root); canonicalErr == nil {
		global.Folders = appendUniqueFolder(global.Folders, filepath.Clean(canonical))
	}
	if err := s.SaveGlobal(global); err != nil {
		return err
	}
	if err := s.SaveProject(root, project); err != nil {
		if rollbackErr := restoreFile(globalPath, previousGlobal, globalExisted); rollbackErr != nil {
			return errors.Join(err, fmt.Errorf("restore global configuration: %w", rollbackErr))
		}
		return err
	}
	return nil
}

// SaveCompleteConfiguration materializes a legacy sparse folder only at an
// explicit configure/save boundary, then persists the complete schema
// atomically with the global configuration. Ordinary loads never call this
// method, so migration remains an explicit user action.
func (s *Store) SaveCompleteConfiguration(root string, global GlobalConfig, project ProjectConfig) error {
	classification := ClassifyProjectConfig(project)
	switch classification {
	case ProjectConfigMigrationRequired:
		materialized, err := MaterializeCompleteProjectConfig(global, &project)
		if err != nil {
			return fmt.Errorf("materialize complete project configuration: %w", err)
		}
		project = materialized
	case ProjectConfigMalformed:
		return fmt.Errorf("validate complete project configuration: %w", projectConfigValidationError(project))
	case ProjectConfigComplete:
		// The classifier has already validated the complete schema.
	default:
		return fmt.Errorf("classify project configuration: unknown classification %q", classification)
	}
	if err := ValidateCompleteProjectConfig(project); err != nil {
		return fmt.Errorf("validate complete project configuration: %w", err)
	}
	return s.SaveConfiguration(root, global, project)
}

func projectConfigValidationError(project ProjectConfig) error {
	if project.Phases != nil {
		return ValidateCompleteProjectConfig(project)
	}
	return ValidateProjectConfig(project)
}

func (s *Store) preflightConfigurationPaths(root, globalPath string) error {
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("resolve project root: %w", err)
	}
	canonicalRoot, err = filepath.Abs(canonicalRoot)
	if err != nil {
		return fmt.Errorf("resolve absolute project root: %w", err)
	}
	rootInfo, err := os.Stat(canonicalRoot)
	if err != nil {
		return fmt.Errorf("inspect project root: %w", err)
	}
	if !rootInfo.IsDir() {
		return fmt.Errorf("project root %q is not a directory", root)
	}

	projectDir := filepath.Join(canonicalRoot, configDirectoryName)
	if err := rejectSymlink(projectDir, "project configuration directory"); err != nil {
		return err
	}
	if info, err := os.Lstat(projectDir); err == nil && !info.IsDir() {
		return fmt.Errorf("prepare project configuration directory: %q is not a directory", projectDir)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect project configuration directory: %w", err)
	}
	for _, candidate := range []struct {
		path string
		name string
	}{
		{path: filepath.Join(projectDir, configFileName), name: "project configuration file"},
		{path: filepath.Join(projectDir, projectsDirectory), name: "project runtime directory"},
	} {
		if err := rejectSymlink(candidate.path, candidate.name); err != nil {
			return err
		}
	}

	canonicalGlobal, err := canonicalPath(globalPath)
	if err != nil {
		return fmt.Errorf("resolve canonical global configuration path: %w", err)
	}
	canonicalProject, err := canonicalPath(filepath.Join(projectDir, configFileName))
	if err != nil {
		return fmt.Errorf("resolve canonical project configuration path: %w", err)
	}
	if canonicalGlobal == canonicalProject {
		return fmt.Errorf("global and project configuration resolve to the same canonical path %q", canonicalGlobal)
	}
	return nil
}

func rejectSymlink(path, name string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect %s: %w", name, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s %q must not be a symbolic link", name, path)
	}
	return nil
}

// canonicalPath resolves every existing path component while retaining any
// not-yet-created suffix. It performs no filesystem mutation.
func canonicalPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	current := filepath.Clean(absolute)
	var suffix []string
	for {
		if _, err := os.Lstat(current); err == nil {
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return filepath.Clean(resolved), nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}

		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("no existing parent for %q", path)
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

func (s *Store) prepareProject(root string) error {
	if err := ensurePrivateDir(filepath.Dir(s.ProjectConfigPath(root))); err != nil {
		return fmt.Errorf("prepare project configuration directory: %w", err)
	}
	if err := ensurePrivateDir(s.ProjectRuntimeDir(root)); err != nil {
		return fmt.Errorf("prepare project runtime directory: %w", err)
	}
	return nil
}

func snapshotFile(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

func restoreFile(path string, data []byte, existed bool) error {
	if !existed {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return writeBytesAtomic(path, data)
}

// IsConfigured reports whether projectRoot contains a readable, valid project configuration.
func (s *Store) IsConfigured(root string) (bool, error) {
	_, err := s.LoadProject(root)
	if errors.Is(err, ErrProjectNotConfigured) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func readYAML(path string, dst any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	d := yaml.NewDecoder(f)
	d.KnownFields(true)
	if err := d.Decode(dst); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("decode %s: multiple YAML documents are not allowed", path)
		}
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}
func writeYAMLAtomic(path string, value any) (retErr error) {
	if err := ensurePrivateDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("create configuration directory: %w", err)
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary configuration: %w", err)
	}
	tmp := f.Name()
	defer func() {
		if err := os.Remove(tmp); err != nil && !errors.Is(err, os.ErrNotExist) && retErr == nil {
			retErr = fmt.Errorf("remove temporary configuration: %w", err)
		}
	}()
	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		return fmt.Errorf("secure temporary configuration: %w", err)
	}
	enc := yaml.NewEncoder(f)
	enc.SetIndent(2)
	if err := enc.Encode(value); err != nil {
		_ = f.Close()
		return fmt.Errorf("encode configuration: %w", err)
	}
	if err := enc.Close(); err != nil {
		_ = f.Close()
		return fmt.Errorf("finish configuration encoding: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync configuration: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close configuration: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace configuration: %w", err)
	}
	return nil
}

func writeBytesAtomic(path string, data []byte) (retErr error) {
	if err := ensurePrivateDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("create configuration directory: %w", err)
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary configuration: %w", err)
	}
	tmp := f.Name()
	defer func() {
		if err := os.Remove(tmp); err != nil && !errors.Is(err, os.ErrNotExist) && retErr == nil {
			retErr = fmt.Errorf("remove temporary configuration: %w", err)
		}
	}()
	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		return fmt.Errorf("secure temporary configuration: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("write temporary configuration: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync configuration: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close configuration: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace configuration: %w", err)
	}
	return nil
}

func ensurePrivateDir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	if err := os.Chmod(path, 0700); err != nil {
		return err
	}
	return nil
}
