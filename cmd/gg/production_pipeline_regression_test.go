package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/config"
	gggit "github.com/VedranJanjetovic/gg/internal/git"
	"github.com/VedranJanjetovic/gg/internal/state"
)

func TestProductionCompositionRunsFakeAgentsGitStateAndPersistsAllEvents(t *testing.T) {
	repo := t.TempDir()
	runProductionGit(t, repo, "init", "-q")
	runProductionGit(t, repo, "config", "user.email", "gg@example.test")
	runProductionGit(t, repo, "config", "user.name", "gg test")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("production fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runProductionGit(t, repo, "add", "README.md")
	runProductionGit(t, repo, "-c", "commit.gpgsign=false", "commit", "-qm", "initial")
	runProductionGit(t, repo, "branch", "-M", "master")
	remote := t.TempDir()
	runProductionGit(t, remote, "init", "--bare", "-q")
	runProductionGit(t, repo, "remote", "add", "origin", remote)
	runProductionGit(t, repo, "push", "-q", "origin", "master")

	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	store := config.NewStore()
	if err := store.SaveGlobal(config.GlobalConfig{
		Version: config.CurrentSchemaVersion,
		Defaults: config.AgentSettings{
			Agent: config.AgentClaude, Model: "fake-model", Effort: config.EffortLow,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveProject(repo, config.ProjectConfig{Version: config.CurrentSchemaVersion}); err != nil {
		t.Fatal(err)
	}

	binDir := t.TempDir()
	fakeAgent := filepath.Join(binDir, "claude")
	if err := os.WriteFile(fakeAgent, []byte(productionFakeAgentScript), 0o755); err != nil {
		t.Fatal(err)
	}
	fakeGH := filepath.Join(binDir, "gh")
	if err := os.WriteFile(fakeGH, []byte(productionFakeGHScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previous); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	ctx := context.Background()
	app, err := newApp(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := app.Run(ctx, []string{"run", "e2e-production"}, &stdout, &stderr); code != 0 {
		t.Fatalf("production run exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}

	stateStore, err := state.NewFileStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	project, err := stateStore.Load(ctx, "e2e-production")
	if err != nil {
		t.Fatal(err)
	}
	if project.Status != state.StatusFinished {
		t.Fatalf("project status = %s, want finished", project.Status)
	}
	if project.WorktreePath == "" || project.BranchName != "gg/e2e-production" {
		t.Fatalf("project Git identity = worktree %q branch %q", project.WorktreePath, project.BranchName)
	}
	for _, artifact := range []string{
		".gg/acceptance-criteria.md", ".gg/grooming.md", ".gg/plan.md", ".gg/development.md",
		".gg/qa-report.md", ".gg/test-document.md", ".gg/build-checker.md",
	} {
		if !containsProductionString(project.ArtifactPaths, artifact) {
			t.Errorf("project artifacts %v missing %q", project.ArtifactPaths, artifact)
		}
	}
	for _, artifact := range []string{"rebase-report.md", "pr.md", "ci-report.md"} {
		if !containsProductionBasename(project.ArtifactPaths, artifact) {
			t.Errorf("project artifacts %v missing basename %q", project.ArtifactPaths, artifact)
		}
	}
	proofPath := filepath.Join(repo, ".gg", "projects", project.Slug, "artifacts", "PROOF.md")
	proofData, err := os.ReadFile(proofPath)
	if err != nil {
		t.Fatalf("copied PROOF.md missing at %s: %v", proofPath, err)
	}
	if !bytes.Contains(proofData, []byte("## Validation: production flow")) || !bytes.Contains(proofData, []byte("$ go test ./cmd/gg -run TestProductionCompositionRunsFakeAgentsGitStateAndPersistsAllEvents -count=1; result: exit code 0")) {
		t.Fatalf("copied PROOF.md has unexpected content: %q", proofData)
	}
	if !containsProductionPath(project.ArtifactPaths, proofPath) {
		t.Fatalf("project artifacts %v missing durable proof path %q", project.ArtifactPaths, proofPath)
	}
	var qaPhaseProof bool
	for _, record := range project.PhaseHistory {
		if record.Phase == "qa" && containsProductionPath(record.ArtifactPaths, proofPath) {
			qaPhaseProof = true
		}
	}
	if !qaPhaseProof {
		t.Fatalf("QA phase history does not persist durable proof path %q: %#v", proofPath, project.PhaseHistory)
	}
	var qaFailed, qaFinished bool
	for _, record := range project.PhaseHistory {
		if record.Phase != "qa" {
			continue
		}
		qaFailed = qaFailed || record.Status == state.StatusFailed
		qaFinished = qaFinished || record.Status == state.StatusFinished
	}
	if !qaFailed || !qaFinished {
		t.Fatalf("QA loop history lacks fail/fix/pass transitions: %#v", project.PhaseHistory)
	}

	signatures := strings.Fields(runProductionGit(t, project.WorktreePath, "log", "--format=%G?", "--grep=^development-"))
	if len(signatures) != 6 {
		t.Fatalf("development commits = %d, want 6 initial/fix subphase commits; signatures=%v", len(signatures), signatures)
	}
	for i, signature := range signatures {
		if signature != "N" {
			t.Fatalf("development commit %d signature = %q, want unsigned N", i, signature)
		}
	}
	if _, err := gggit.NewClient(repo, nil).HeadCommit(ctx, project.WorktreePath); err != nil {
		t.Fatalf("real Git client could not inspect production worktree: %v", err)
	}

	eventsPath := filepath.Join(repo, ".gg", "projects", project.Slug, "events.jsonl")
	eventData, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("production composition did not persist lifecycle/process events at %s: %v", eventsPath, err)
	}
	var records []productionEventRecord
	for _, line := range bytes.Split(bytes.TrimSpace(eventData), []byte{'\n'}) {
		var event productionEventRecord
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatalf("event is not JSONL: %q: %v", line, err)
		}
		records = append(records, event)
	}
	var lifecycle []string
	processCounts := make(map[string]int)
	for _, event := range records {
		switch event.Type {
		case "project_created", "phase_started", "phase_succeeded", "phase_failed",
			"feedback_created", "phase_retried", "project_stopped", "project_finished":
			identity := event.Type
			if event.Phase != "" {
				identity += ":" + event.Phase + "/" + event.Subphase
			}
			lifecycle = append(lifecycle, identity)
		case "started", "output", "completed", "failed", "canceled":
			processCounts[event.Type]++
		}
	}
	wantLifecycle := []string{
		"project_created",
		"phase_started:acceptance_criteria/", "phase_succeeded:acceptance_criteria/",
		"phase_started:grooming/", "phase_succeeded:grooming/",
		"phase_started:planning/", "phase_succeeded:planning/",
		"phase_started:development/implementation", "phase_succeeded:development/implementation",
		"phase_started:development/testing", "phase_succeeded:development/testing",
		"phase_started:development/review", "phase_succeeded:development/review",
		"phase_started:rebase/", "phase_succeeded:rebase/",
		"phase_started:qa/", "phase_failed:qa/", "feedback_created:qa/", "phase_retried:qa/",
		"phase_started:development/implementation", "phase_succeeded:development/implementation",
		"phase_started:development/testing", "phase_succeeded:development/testing",
		"phase_started:development/review", "phase_succeeded:development/review",
		"phase_started:rebase/", "phase_succeeded:rebase/",
		"phase_started:qa/", "phase_succeeded:qa/",
		"phase_started:test_document/", "phase_succeeded:test_document/",
		"phase_started:build_checker/", "phase_succeeded:build_checker/",
		"phase_started:pr/", "phase_succeeded:pr/",
		"phase_started:ci/", "phase_succeeded:ci/",
		"project_finished",
	}
	if strings.Join(lifecycle, "\n") != strings.Join(wantLifecycle, "\n") {
		t.Fatalf("durable lifecycle event order:\n got %v\nwant %v", lifecycle, wantLifecycle)
	}
	if processCounts["started"] != 13 || processCounts["completed"] != 12 || processCounts["failed"] != 1 || processCounts["canceled"] != 0 {
		t.Fatalf("agent lifecycle event counts = %v, want 13 started, 12 completed, 1 failed, 0 canceled", processCounts)
	}
	if processCounts["output"] < processCounts["started"] {
		t.Fatalf("agent output events = %d, want at least one per invocation (%d)", processCounts["output"], processCounts["started"])
	}
	if len(records) == 0 || records[len(records)-1].Type != "project_finished" {
		t.Fatalf("last durable event = %#v, want unique terminal project_finished", records[len(records)-1])
	}
}

type productionEventRecord struct {
	Type     string `json:"type"`
	Phase    string `json:"phase"`
	Subphase string `json:"subphase"`
}

const productionFakeAgentScript = `#!/bin/sh
set -eu

prompt=
for argument in "$@"; do
	prompt=$argument
done

phase=
subphase=
artifact=
case "$prompt" in
	*'"acceptance_criteria"'*) phase=acceptance_criteria; artifact=acceptance-criteria.md ;;
	*'"grooming"'*) phase=grooming; artifact=grooming.md ;;
	*'"planning"'*) phase=planning; artifact=plan.md ;;
	*'"development" / "implementation"'*) phase=development; subphase=implementation; artifact=development.md ;;
	*'"development" / "testing"'*) phase=development; subphase=testing; artifact=development.md ;;
	*'"development" / "review"'*) phase=development; subphase=review; artifact=development.md ;;
	*'"qa"'*) phase=qa; artifact=PROOF.md ;;
	*'"rebase"'*) phase=rebase; artifact=rebase-report.md ;;
	*'"test_document"'*) phase=test_document; artifact=test-document.md ;;
	*'"build_checker"'*) phase=build_checker; artifact=build-checker.md ;;
	*'"pr"'*) phase=pr; artifact=pr.md ;;
	*'"ci"'*) phase=ci; artifact=ci-report.md ;;
	*) printf 'unknown phase prompt\n' >&2; exit 64 ;;
esac

disposition=passed
exit_code=0
if [ "$phase" = qa ] && [ ! -f .gg/qa-report.md ]; then
	disposition=failed
fi

run_id=$(printf '%s\n' "$prompt" | sed -n 's/^gg_run_id: "\(.*\)"$/\1/p' | head -n 1)
if [ -z "$run_id" ]; then
	printf 'missing run ID protocol in prompt\n' >&2
	exit 65
fi
if [ "$phase" = planning ]; then
cat > ".gg/$artifact" <<EOF
---
gg_run_id: "$run_id"
gg_disposition: passed
gg_plan_complexity: "Trivial"
gg_plan_complexity_evidence: ["The fixture exercises one cohesive pipeline outcome."]
gg_plan_phases: ["Phase 1: production pipeline"]
gg_plan_phase_boundaries: [{"phase":"Phase 1: production pipeline","justification":"The fixture is one cohesive outcome with no dependency ordering."}]
---
# Implementation Plan

## Complexity assessment

- Complexity category: **Trivial**
- Selected phase count: **1**

Supporting evidence:

1. The fixture exercises one cohesive pipeline outcome.

## Phase 1: production pipeline

Boundary justification: The fixture is one cohesive outcome with no dependency ordering.
EOF
else
printf '%s\n' '---' "gg_run_id: \"$run_id\"" "gg_disposition: $disposition" '---' \
	"phase=$phase subphase=$subphase disposition=$disposition" > ".gg/$artifact"
fi
if [ "$phase" = qa ]; then
cat > .gg/PROOF.md <<EOF
---
gg_run_id: "$run_id"
---

## Validation: production flow
- Status: $([ "$disposition" = failed ] && printf feedback || printf pass)
- Test location: production fixture
- Test name: TestProductionCompositionRunsFakeAgentsGitStateAndPersistsAllEvents
- Flow/scenario: execute the configured production pipeline through the CLI
- What it verifies: each configured phase runs and persists artifacts
- Proof it passed: \$ go test ./cmd/gg -run TestProductionCompositionRunsFakeAgentsGitStateAndPersistsAllEvents -count=1; result: exit code 0
- Manual run instructions: configure the repository and run gg run e2e-production.
EOF
if [ "$disposition" = failed ]; then printf '%s\n' '## Feedback' 'The first QA attempt needs the prior QA report before it can pass.' >> .gg/PROOF.md; fi
printf '%s\n' '---' "gg_run_id: \"$run_id\"" 'gg_disposition: passed' '---' 'legacy qa report' > .gg/qa-report.md
fi
printf 'fake-agent phase=%s subphase=%s\n' "$phase" "$subphase"

if [ "$phase" = development ]; then
	printf '%s\n' "$subphase" >> development-progress.txt
	git add -f .gg/development.md; git add development-progress.txt
	git -c commit.gpgsign=false commit -m "development-$subphase"
fi

exit "$exit_code"
`

const productionFakeGHScript = `#!/bin/sh
set -eu

case "$*" in
  pr\ create*) printf '%s\n' 'https://github.com/example/production/pull/1' ;;
  pr\ checks*) printf '%s\n' '[{"name":"production","state":"SUCCESS","bucket":"pass","link":"https://github.com/example/production/actions/1"}]' ;;
  pr\ view*) printf '%s\n' '{"url":"https://github.com/example/production/pull/1","state":"MERGED","mergeable":"MERGEABLE","updatedAt":"2026-08-24T00:00:00Z"}' ;;
  *) printf 'unsupported fake gh command: %s\n' "$*" >&2; exit 64 ;;
esac
`

func runProductionGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	command.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+filepath.Join(t.TempDir(), "empty-gitconfig"),
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func containsProductionString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsProductionBasename(values []string, want string) bool {
	for _, value := range values {
		if filepath.Base(filepath.Clean(value)) == want {
			return true
		}
	}
	return false
}

func containsProductionPath(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
		resolvedValue, valueErr := filepath.EvalSymlinks(value)
		resolvedWant, wantErr := filepath.EvalSymlinks(want)
		if valueErr == nil && wantErr == nil && resolvedValue == resolvedWant {
			return true
		}
	}
	return false
}
