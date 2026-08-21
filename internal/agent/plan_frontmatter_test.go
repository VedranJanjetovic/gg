package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/pipeline"
)

func TestReadPlanFrontmatterExtractsPhasesAndCompletions(t *testing.T) {
	dir := t.TempDir()
	plan := "---\ngg_run_id: \"run\"\ngg_disposition: passed\ngg_plan_phases: [\"Phase 1: core loop\", \"Phase 2: entities\", \" \"]\n---\n# Plan\n"
	if err := os.WriteFile(artifactPath(t, dir, "plan.md"), []byte(plan), 0o644); err != nil {
		t.Fatal(err)
	}
	phases, completed := ReadPlanFrontmatter(dir, pipeline.PhasePlanning)
	if want := []string{"Phase 1: core loop", "Phase 2: entities"}; !reflect.DeepEqual(phases, want) {
		t.Fatalf("phases = %#v, want %#v", phases, want)
	}
	if len(completed) != 0 {
		t.Fatalf("completed = %#v, want none from the planning artifact", completed)
	}

	development := "---\ngg_run_id: \"run\"\ngg_disposition: failed\ngg_plan_completed: [\"Phase 1: core loop\"]\n---\nnotes\n"
	if err := os.WriteFile(artifactPath(t, dir, "development.md"), []byte(development), 0o644); err != nil {
		t.Fatal(err)
	}
	phases, completed = ReadPlanFrontmatter(dir, pipeline.PhaseDevelopment)
	if len(phases) != 0 || !reflect.DeepEqual(completed, []string{"Phase 1: core loop"}) {
		t.Fatalf("phases = %#v completed = %#v", phases, completed)
	}
}

func TestReadPlanFrontmatterToleratesMissingOrMalformedData(t *testing.T) {
	dir := t.TempDir()
	if phases, completed := ReadPlanFrontmatter(dir, pipeline.PhasePlanning); phases != nil || completed != nil {
		t.Fatalf("missing artifact yielded %#v %#v", phases, completed)
	}
	malformed := "---\ngg_plan_phases: not-json\n---\n"
	if err := os.WriteFile(artifactPath(t, dir, "plan.md"), []byte(malformed), 0o644); err != nil {
		t.Fatal(err)
	}
	if phases, _ := ReadPlanFrontmatter(dir, pipeline.PhasePlanning); phases != nil {
		t.Fatalf("malformed array yielded %#v", phases)
	}
}

// artifactPath returns the artifact location inside the worktree's ignored
// artifact directory, creating the directory.
func artifactPath(t *testing.T, worktree, name string) string {
	t.Helper()
	dir := filepath.Join(worktree, pipeline.ArtifactDirectory)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, name)
}
