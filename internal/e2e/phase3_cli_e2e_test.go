//go:build linux || darwin

package e2e

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/VedranJanjetovic/gg/internal/config"
	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/proof"
	"github.com/VedranJanjetovic/gg/internal/state"
	"gopkg.in/yaml.v3"
)

func phase3FakeBin(t *testing.T, env *Environment) string {
	t.Helper()
	bin := filepath.Join(env.Root, "phase3-bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
set -eu
agent=$(basename "$0")
prompt=
for arg do prompt=$arg; done
phase_line=$(printf '%s\n' "$prompt" | sed -n '/^## Phase$/ {n; p;}' | head -n 1)
phase=$(printf '%s\n' "$phase_line" | sed 's/^"//; s/" \/ ".*"$//; s/"$//')
subphase=$(printf '%s\n' "$phase_line" | sed -n 's/^".*" \/ "\(.*\)"$/\1/p')
run_id=$(printf '%s\n' "$prompt" | sed -n 's/^gg_run_id: "\(.*\)"$/\1/p' | head -n 1)
log=${GG_FAKE_AGENT_LOG:-}
if [ -n "$log" ]; then
  { printf 'agent=%s\n' "$agent"; printf 'phase=%s\n' "$phase"; printf 'subphase=%s\n' "$subphase"; printf 'run_id=%s\n' "$run_id"; for arg do printf 'arg=%s\n' "$arg"; done; } >>"$log"
fi
if [ "${GG_FAKE_BLOCK_FILE:-}" != "" ] && [ -e "$GG_FAKE_BLOCK_FILE" ]; then
  while [ -e "$GG_FAKE_BLOCK_FILE" ]; do sleep 0.02; done
fi
artifact=
case "$phase" in
 acceptance_criteria) artifact=acceptance-criteria.md;; grooming) artifact=grooming.md;; planning) artifact=plan.md;; development) artifact=development.md;; qa) artifact=qa-report.md;; rebase) artifact=rebase-report.md;; test_document) artifact=test-document.md;; build_checker) artifact=build-checker.md;;
esac
if [ -n "$artifact" ]; then
  cat >"$artifact" <<EOF
---
gg_run_id: "$run_id"
gg_disposition: passed
---

Deterministic fake $agent result for $phase $subphase.
EOF
fi
if [ "$phase" = development ]; then
  git add development.md
  git -c commit.gpgsign=false commit -m "fake: $subphase development" >/dev/null
fi
if [ "$phase" = qa ]; then
  tick=$(printf '\140')
  qa_status=pass
  if [ "${GG_FAKE_QA_ALWAYS_FAIL:-}" = 1 ]; then qa_status=feedback; fi
  if [ "${GG_FAKE_QA_FAIL_ONCE:-}" = 1 ] && [ ! -e "${GG_FAKE_QA_MARKER:-}" ]; then qa_status=feedback; : >"${GG_FAKE_QA_MARKER:-.fake-qa-failed}"; fi
  proof_mode=${GG_FAKE_PROOF_MODE:-valid}
  if [ "$proof_mode" != missing ]; then
    if [ "$proof_mode" = malformed ]; then
      printf '%s\n' '# malformed proof' >PROOF.md
    else
      proof_run_id=$run_id
      if [ "$proof_mode" = stale ]; then proof_run_id=stale-run; fi
      cat >PROOF.md <<EOF
---
gg_run_id: "$proof_run_id"
---

## Validation: fake QA
- Status: $qa_status
- Test location: internal/e2e/phase3_cli_e2e_test.go
- Test name: real fake pipeline
- Flow/Scenario: deterministic CLI pipeline
- What it verifies: the fake QA executable produced canonical evidence
- Proof it passed: ${tick}go test ./...${tick} returned result $qa_status
- Manual run instructions: run the focused E2E test
EOF
      if [ "$proof_mode" = tracked ]; then git add PROOF.md; fi
    fi
  fi
fi
printf 'fake-%s: deterministic response\n' "$agent"
`
	scriptPath := filepath.Join(bin, "fake-agent")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, agent := range []string{"claude", "codex"} {
		if err := os.Symlink(scriptPath, filepath.Join(bin, agent)); err != nil {
			t.Fatal(err)
		}
	}
	return bin
}

func phase3Env(env *Environment, bin, log string) []string {
	// The network-denied child boundary is installed by the E2E runner, not by
	// proxy environment convention. The configured phases below also assert
	// that PR/CI/build-checker are disabled and absent from the invocation log.
	return append(env.Env(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GG_FAKE_AGENT_LOG="+log,
		"GIT_TERMINAL_PROMPT=0", "GIT_SSH_COMMAND=false",
	)
}

func phase3Config() config.ProjectConfig {
	no := false
	return config.ProjectConfig{Version: config.CurrentSchemaVersion, PhaseOverrides: map[config.Phase]config.PhaseOverride{
		config.PhaseGrooming: {Enabled: &no}, config.PhasePlanning: {Enabled: &no}, config.PhaseBuildChecker: {Enabled: &no}, config.PhasePR: {Enabled: &no}, config.PhaseCI: {Enabled: &no},
	}}
}

func phase3Configure(t *testing.T, binary string, env *Environment, repo *GitRepository, cfg config.ProjectConfig) {
	t.Helper()
	result := RunWithInputTimeout(t, repo.Root, env.Env(), strings.NewReader("codex\ngpt-5\nhigh\n"), binary, "configure")
	if result.Err != nil {
		t.Fatalf("configure: %+v", result)
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo.Root, ".gg", "config.yaml"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func phase3WaitLog(t *testing.T, path, needle string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(data), needle) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	data, _ := os.ReadFile(path)
	t.Fatalf("log never contained %q: %q", needle, data)
}

func phase3WaitProjectStatus(t *testing.T, store *state.FileStore, slug string, want state.LifecycleStatus) state.ProjectState {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last state.ProjectState
	var lastErr error
	for time.Now().Before(deadline) {
		project, err := store.Load(t.Context(), slug)
		if err == nil {
			last = project
			lastErr = nil
			if project.Status == want {
				return project
			}
		} else {
			lastErr = err
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("project %q never reached status %s: last=%s err=%v", slug, want, last.Status, lastErr)
	return state.ProjectState{}
}

func phase3Start(t *testing.T, dir, binary string, env []string, input string, args ...string) (*exec.Cmd, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	cmd, err := networkDeniedCommand(dir, env, binary, args...)
	if err != nil {
		t.Skipf("network-denied E2E unsupported: %v", err)
	}
	cmd.Stdin = strings.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return cmd, &stdout, &stderr
}

func phase3Phases(t *testing.T, log []byte) []string {
	t.Helper()
	var phases []string
	for _, line := range strings.Split(string(log), "\n") {
		if strings.HasPrefix(line, "phase=") {
			phases = append(phases, strings.TrimPrefix(line, "phase="))
		}
	}
	return phases
}

func TestRealCLIFakePipelineOrdersAgentsAndCopiesCanonicalProof(t *testing.T) {
	env, repo, binary := NewEnvironment(t), NewGitRepository(t), BuildBinary(t)
	bin := phase3FakeBin(t, env)
	log := filepath.Join(env.Root, "pipeline.log")
	marker := filepath.Join(env.Root, "qa-once")
	cfg := phase3Config()
	qaAgent := config.AgentClaude
	cfg.PhaseOverrides[config.PhaseQA] = config.PhaseOverride{AgentSettingsOverride: config.AgentSettingsOverride{Agent: qaAgent, Model: "claude-model", Effort: config.EffortMedium}}
	phase3Configure(t, binary, env, repo, cfg)
	processEnv := append(phase3Env(env, bin, log), "GG_FAKE_QA_FAIL_ONCE=1", "GG_FAKE_QA_MARKER="+marker)
	result := RunWithInputNetworkDeniedTimeout(t, repo.Root, processEnv, strings.NewReader("Ship a dashboard.\nusers can view it\n\n"), binary, "run")
	if result.Err != nil {
		t.Fatalf("run: %+v\n%s", result, result.Stderr)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"acceptance_criteria", "development", "development", "development", "rebase", "qa", "development", "development", "development", "rebase", "qa", "test_document"}
	if got := phase3Phases(t, data); !equalStrings(got, want) {
		t.Fatalf("phase order = %v, want %v\nlog=%s", got, want, data)
	}
	for _, disabled := range []string{"build_checker", "pr", "ci"} {
		if strings.Contains(string(data), "phase="+disabled+"\n") {
			t.Fatalf("networked phase %q was invoked despite deny-by-configuration", disabled)
		}
	}
	for _, phase := range []config.Phase{config.PhaseBuildChecker, config.PhasePR, config.PhaseCI} {
		if cfg.PhaseOverrides[phase].Enabled == nil || *cfg.PhaseOverrides[phase].Enabled {
			t.Fatalf("networked phase %q is not explicitly disabled", phase)
		}
	}
	text := string(data)
	for _, part := range []string{"agent=codex", "arg=exec", "arg=--model", "arg=gpt-5", "arg=--config", "arg=model_reasoning_effort=high", "agent=claude", "arg=--print", "arg=claude-model"} {
		if !strings.Contains(text, part) {
			t.Fatalf("log missing %q: %s", part, text)
		}
	}
	store, err := state.NewFileStore(repo.Root)
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.Load(t.Context(), "ship-a-dashboard")
	if err != nil {
		t.Fatal(err)
	}
	if project.Status != state.StatusFinished {
		t.Fatalf("status = %s", project.Status)
	}
	for _, name := range []string{"acceptance-criteria.md", "development.md", "qa-report.md", "rebase-report.md", "test-document.md"} {
		if _, err := os.Stat(filepath.Join(project.WorktreePath, name)); err != nil {
			t.Fatalf("missing canonical %s: %v", name, err)
		}
	}
	proofPath := filepath.Join(repo.Root, ".gg", "projects", project.Slug, "artifacts", proof.ArtifactName)
	sourceProofPath := filepath.Join(project.WorktreePath, proof.ArtifactName)
	sourceProof, err := os.ReadFile(sourceProofPath)
	if err != nil {
		t.Fatalf("source proof missing: %v", err)
	}
	proofBytes, err := os.ReadFile(proofPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(sourceProof, proofBytes) {
		t.Fatalf("durable proof changed bytes: source=%q durable=%q", sourceProof, proofBytes)
	}
	parsed, err := proof.Parse(proofBytes)
	if err != nil || parsed.Classify() != proof.ClassificationPass {
		t.Fatalf("proof = %#v, err=%v", parsed, err)
	}
	stateBytes, err := os.ReadFile(filepath.Join(repo.Root, ".gg", "projects", project.Slug, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded state.ProjectState
	if err := json.Unmarshal(stateBytes, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Slug != project.Slug || len(decoded.PhaseHistory) < len(want) {
		t.Fatalf("durable state = %#v", decoded)
	}
}

func TestRealCLIFailureProofsAreTerminalAndNotCopied(t *testing.T) {
	for _, mode := range []string{"malformed", "missing", "stale", "tracked"} {
		t.Run(mode, func(t *testing.T) {
			env, repo, binary := NewEnvironment(t), NewGitRepository(t), BuildBinary(t)
			bin := phase3FakeBin(t, env)
			log := filepath.Join(env.Root, mode+".log")
			phase3Configure(t, binary, env, repo, phase3Config())
			processEnv := append(phase3Env(env, bin, log), "GG_FAKE_PROOF_MODE="+mode)
			result := RunWithInputNetworkDeniedTimeout(t, repo.Root, processEnv, strings.NewReader("Reject invalid proof.\ninvalid proof is terminal\n\n"), binary, "run", "--max-iterations", "2")
			if result.Err == nil {
				t.Fatalf("invalid %s proof unexpectedly succeeded: %+v", mode, result)
			}
			phases := phase3Phases(t, mustRead(t, log))
			want := []string{"acceptance_criteria", "development", "development", "development", "qa"}
			if !equalStrings(phases, want) {
				t.Fatalf("phases = %v, want terminal QA without Development retry", phases)
			}
			store, err := state.NewFileStore(repo.Root)
			if err != nil {
				t.Fatal(err)
			}
			project, err := store.Load(t.Context(), "reject-invalid-proof")
			if err != nil {
				t.Fatal(err)
			}
			if project.Status != state.StatusFailed {
				t.Fatalf("persisted status = %s, want failed", project.Status)
			}
			artifact := filepath.Join(repo.Root, ".gg", "projects", project.Slug, "artifacts", proof.ArtifactName)
			if _, err := os.Stat(artifact); !os.IsNotExist(err) {
				t.Fatalf("durable proof artifact exists for %s failure: err=%v", mode, err)
			}
		})
	}
}

func TestRealCLIFakePipelineStopsAndResumesPersistedState(t *testing.T) {
	env, repo, binary := NewEnvironment(t), NewGitRepository(t), BuildBinary(t)
	bin := phase3FakeBin(t, env)
	log := filepath.Join(env.Root, "stop.log")
	block := filepath.Join(env.Root, "block")
	if err := os.WriteFile(block, []byte("block\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	phase3Configure(t, binary, env, repo, phase3Config())
	processEnv := append(phase3Env(env, bin, log), "GG_FAKE_BLOCK_FILE="+block)
	cmd, stdout, stderr := phase3Start(t, repo.Root, binary, processEnv, "Stop and resume.\nstate is durable\n\n", "run")
	phase3WaitLog(t, log, "phase=acceptance_criteria")
	stop := RunWithTimeout(t, repo.Root, processEnv, binary, "stop", "stop-and-resume")
	if stop.Err != nil {
		t.Fatalf("stop: %+v", stop)
	}
	store, err := state.NewFileStore(repo.Root)
	if err != nil {
		t.Fatal(err)
	}
	stopped := phase3WaitProjectStatus(t, store, "stop-and-resume", state.StatusStopped)
	if stopped.CurrentPhase != string(pipeline.PhaseAcceptanceCriteria) || stopped.ActiveRunID != "" || stopped.DispatchClaimRunID != "" {
		t.Fatalf("stopped cursor/run = phase=%q active=%q claim=%q", stopped.CurrentPhase, stopped.ActiveRunID, stopped.DispatchClaimRunID)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatalf("stopped run succeeded: %s / %s", stdout, stderr)
	}
	if err := os.Remove(block); err != nil {
		t.Fatalf("remove blocker: %v", err)
	}
	resumed := RunWithTimeout(t, repo.Root, phase3Env(env, bin, log), binary, "resume", "stop-and-resume")
	if resumed.Err != nil {
		t.Fatalf("resume: %+v\n%s", resumed, resumed.Stderr)
	}
	finished, err := store.Load(t.Context(), "stop-and-resume")
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != state.StatusFinished {
		t.Fatalf("resumed status = %s", finished.Status)
	}
	if got := strings.Count(string(mustRead(t, log)), "phase=acceptance_criteria\n"); got != 2 {
		t.Fatalf("acceptance dispatch count = %d, want 2", got)
	}
}

func TestRealCLIFakePipelineBoundsQAAttempts(t *testing.T) {
	env, repo, binary := NewEnvironment(t), NewGitRepository(t), BuildBinary(t)
	bin := phase3FakeBin(t, env)
	log := filepath.Join(env.Root, "bounded.log")
	phase3Configure(t, binary, env, repo, phase3Config())
	processEnv := append(phase3Env(env, bin, log), "GG_FAKE_QA_ALWAYS_FAIL=1")
	result := RunWithInputNetworkDeniedTimeout(t, repo.Root, processEnv, strings.NewReader("Bound QA retries.\nretry count is bounded\n\n"), binary, "run", "--max-iterations", "2")
	if result.Err == nil {
		t.Fatalf("bounded run unexpectedly succeeded: %+v", result)
	}
	phases := phase3Phases(t, mustRead(t, log))
	if len(phases) != 9 || phases[4] != "qa" || phases[8] != "qa" {
		t.Fatalf("bounded phases = %v", phases)
	}
	store, err := state.NewFileStore(repo.Root)
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.Load(t.Context(), "bound-qa-retries")
	if err != nil {
		t.Fatal(err)
	}
	if project.Status != state.StatusFailed || project.MaxQAAttempts != 2 || project.QACompletedAttempts != 2 || project.QALoopStage != "exhausted" {
		t.Fatalf("bounded state = %#v", project)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
