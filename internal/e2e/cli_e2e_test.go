package e2e

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/config"
	"github.com/VedranJanjetovic/gg/internal/git"
	"github.com/VedranJanjetovic/gg/internal/state"
	"gopkg.in/yaml.v3"
)

func TestRealCLIConfigureCreatesProjectAndDisposableWorktree(t *testing.T) {
	env := NewEnvironment(t)
	repo := NewGitRepository(t)
	binary := BuildBinary(t)
	logPath := filepath.Join(env.Root, "agent.log")
	processEnv := append(env.Env(), "GG_FAKE_AGENT_LOG="+logPath)

	configured := RunWithInputTimeout(t, repo.Root, processEnv,
		strings.NewReader("codex\ngpt-5\nhigh\n"), binary, "configure")
	if configured.Err != nil || configured.ExitCode != 0 {
		t.Fatalf("configure result = %+v\nstdout:\n%s\nstderr:\n%s", configured, configured.Stdout, configured.Stderr)
	}
	globalBytes, err := os.ReadFile(filepath.Join(env.ConfigHome, "gg", "config.yaml"))
	if err != nil {
		t.Fatalf("read persisted global config: %v", err)
	}
	var global config.GlobalConfig
	if err := yaml.Unmarshal(globalBytes, &global); err != nil {
		t.Fatalf("decode persisted global config: %v", err)
	}
	wantGlobal := config.GlobalConfig{Version: config.CurrentSchemaVersion,
		Defaults: config.AgentSettings{Agent: config.AgentCodex, Model: "gpt-5", Effort: config.EffortHigh}}
	if global.Version != wantGlobal.Version || global.Defaults != wantGlobal.Defaults {
		t.Fatalf("global config = %#v, want version/defaults %#v", global, wantGlobal)
	}
	projectConfig, err := os.ReadFile(filepath.Join(repo.Root, ".gg", "config.yaml"))
	if err != nil {
		t.Fatalf("read persisted project config: %v", err)
	}
	var projectSettings config.ProjectConfig
	if err := yaml.Unmarshal(projectConfig, &projectSettings); err != nil {
		t.Fatalf("decode persisted project config: %v", err)
	}
	if projectSettings.Version != config.CompleteSchemaVersion {
		t.Fatalf("project config version = %d, want %d", projectSettings.Version, config.CompleteSchemaVersion)
	}

	created := RunWithInputTimeout(t, repo.Root, processEnv,
		strings.NewReader("Build a release dashboard.\nPersist the project state.\n\n"), binary, "run")
	if created.ExitCode == 0 {
		t.Fatalf("run unexpectedly completed pipeline; stdout=%q stderr=%q", created.Stdout, created.Stderr)
	}
	if !strings.Contains(created.Stdout, "Describe the project") {
		t.Fatalf("run stdout = %q, missing project description prompt", created.Stdout)
	}

	store, err := state.NewFileStore(repo.Root)
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.Load(context.Background(), "release-dashboard")
	if err != nil {
		t.Fatalf("load persisted project state: %v", err)
	}
	naming, err := git.ProjectNamingForSlug(repo.Root, project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if project.Name != "release_dashboard" || project.OriginalGoal != "Build a release dashboard.\nPersist the project state." {
		t.Fatalf("project identity = %#v", project)
	}
	if !git.PathsEqual(project.WorktreePath, naming.WorktreePath) || project.BranchName != naming.BranchName {
		t.Fatalf("project worktree metadata = path %q branch %q, want %q %q", project.WorktreePath, project.BranchName, naming.WorktreePath, naming.BranchName)
	}
	if _, err := os.Stat(project.WorktreePath); err != nil {
		t.Fatalf("created worktree missing: %v", err)
	}
	branch := RunWithTimeout(t, project.WorktreePath, processEnv, "git", "branch", "--show-current")
	if branch.Err != nil || strings.TrimSpace(branch.Stdout) != project.BranchName {
		t.Fatalf("worktree branch = %+v, want %q", branch, project.BranchName)
	}
	if _, err := os.Stat(filepath.Join(repo.Root, ".gg-worktrees")); !os.IsNotExist(err) {
		t.Fatalf("worktree was not isolated from repository root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(env.Home, ".gg")); !os.IsNotExist(err) {
		t.Fatalf("unexpected state under isolated HOME: %v", err)
	}
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read fake-agent invocation log: %v", err)
	}
	if !strings.Contains(string(logBytes), "agent=codex\n") {
		t.Fatalf("fake codex did not run; log=%q", logBytes)
	}
	var decoded state.ProjectState
	stateBytes, err := os.ReadFile(filepath.Join(repo.Root, ".gg", "projects", project.Slug, "state.json"))
	if err != nil {
		t.Fatalf("read persisted state bytes: %v", err)
	}
	if err := json.Unmarshal(stateBytes, &decoded); err != nil || decoded.Slug != project.Slug {
		t.Fatalf("persisted state decode = %#v, err=%v", decoded, err)
	}
}
