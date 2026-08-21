package state_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/VedranJanjetovic/gg/internal/state"
)

func contractProject(status state.LifecycleStatus) state.ProjectState {
	now := time.Unix(10, 0).UTC()
	return state.ProjectState{
		SchemaVersion: state.CurrentSchemaVersion, Name: "Demo", Slug: "demo",
		OriginalGoal: "goal", AcceptanceCriteria: []string{"done"},
		PipelineConfig: state.PipelineConfigSnapshot{SchemaVersion: 1, Data: json.RawMessage(`{}`)},
		CurrentPhase:   "pipeline", Status: status, WorktreePath: "/tmp/demo", BranchName: "gg/demo",
		CreatedAt: now, UpdatedAt: now, StatusChangedAt: now,
	}
}

func TestObserveSeparatesPipelineCompletionFromMergedPullRequest(t *testing.T) {
	pipeline := contractProject(state.StatusFinished)
	merged := pipeline
	merged.Terminal = &state.TerminalState{Kind: state.TerminalPullRequestMerged, At: pipeline.UpdatedAt, PullRequestURL: "https://github.com/o/r/pull/1"}

	if got := state.Observe(pipeline); !got.Terminal || got.TerminalKind != state.TerminalPipelineComplete {
		t.Fatalf("pipeline observation = %#v", got)
	}
	if got := state.Observe(merged); got.TerminalKind != state.TerminalPullRequestMerged || got.Project.Terminal.PullRequestURL == "" {
		t.Fatalf("merged observation = %#v", got)
	}
}

func TestObserveAllPreservesDeterministicStoreOrder(t *testing.T) {
	projects := []state.ProjectState{contractProject(state.StatusRunning), contractProject(state.StatusStopped)}
	projects[0].Slug, projects[1].Slug = "a", "b"
	observations := state.ObserveAll(projects)
	if len(observations) != 2 || observations[0].Project.Slug != "a" || observations[1].Project.Slug != "b" {
		t.Fatalf("observations = %#v", observations)
	}
}

func TestClassifyRerunKeepsTerminalAndStoppedSemanticsDistinct(t *testing.T) {
	tests := []struct {
		status state.LifecycleStatus
		want   state.RerunKind
	}{
		{state.StatusPending, state.RerunNew},
		{state.StatusStopped, state.RerunResume},
		{state.StatusFailed, state.RerunResume},
		{state.StatusFinished, state.RerunFinished},
		{state.StatusTerminated, state.RerunFinished},
	}
	for _, test := range tests {
		if got := state.ClassifyRerun(contractProject(test.status)); got != test.want {
			t.Errorf("status %s classified as %s, want %s", test.status, got, test.want)
		}
	}
}

func TestLegacyProjectStateRemainsValidWithoutTerminalMarker(t *testing.T) {
	project, err := state.NewProjectState(contractProject(state.StatusFinished))
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(project)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) == "" || project.Terminal != nil {
		t.Fatalf("project = %#v", project)
	}
}
