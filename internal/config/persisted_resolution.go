package config

import "fmt"

// PipelineConfigSource loads the persisted configuration layers required to
// resolve settings for one pipeline run. The pipeline composition root supplies
// the source; this package owns the configuration schema and resolution rules.
type PipelineConfigSource interface {
	LoadGlobal() (GlobalConfig, error)
	LoadProject(projectRoot string) (ProjectConfig, error)
}

// ResolvePipelineConfig loads persisted global and project configuration, then
// applies invocation-only overrides. It returns the existing resolved
// configuration contract so pipeline consumers do not need a second schema.
func ResolvePipelineConfig(source PipelineConfigSource, projectRoot string, overrides RunOverrides) (ResolvedConfig, error) {
	global, err := source.LoadGlobal()
	if err != nil {
		return ResolvedConfig{}, fmt.Errorf("load global configuration: %w", err)
	}
	project, err := source.LoadProject(projectRoot)
	if err != nil {
		return ResolvedConfig{}, fmt.Errorf("load project configuration: %w", err)
	}
	resolved, err := Resolve(global, &project, overrides)
	if err != nil {
		return ResolvedConfig{}, fmt.Errorf("resolve pipeline configuration: %w", err)
	}
	return resolved, nil
}
