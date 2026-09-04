package orchestrator_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/config"
	"github.com/VedranJanjetovic/gg/internal/orchestrator"
	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/state"
)

// Older binaries split Development checking into separate testing and review
// subphases and persisted those IDs in the resume cursor. The merged default
// pipeline must keep resuming such projects: any pending legacy checking work
// maps onto the verification subphase, and finished legacy checking advances.
func TestResumeMapsLegacySplitSubphaseCursorsOntoVerification(t *testing.T) {
	tests := []struct {
		name   string
		cursor string
		status state.LifecycleStatus
		want   []string
	}{
		{
			// A stopped legacy testing run still owes its checking work; the
			// merged verification subphase covers tests and review.
			name: "stopped testing resumes at verification", cursor: "testing", status: state.StatusStopped,
			want: []string{"development/verification", "rebase/", "qa/", "test_document/"},
		},
		{
			// A finished legacy testing run still owed a review; verification
			// runs next and covers it.
			name: "finished testing runs verification next", cursor: "testing", status: state.StatusFinished,
			want: []string{"development/verification", "rebase/", "qa/", "test_document/"},
		},
		{
			// A stopped legacy review re-runs as verification (a checking
			// subphase is always safe to repeat).
			name: "stopped review resumes at verification", cursor: "review", status: state.StatusStopped,
			want: []string{"development/verification", "rebase/", "qa/", "test_document/"},
		},
		{
			// A finished legacy review completed its plan phase's checking;
			// resume advances past Development entirely.
			name: "finished review advances to next phase", cursor: "review", status: state.StatusFinished,
			want: []string{"rebase/", "qa/", "test_document/"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := resolvedPipeline(t, config.PhaseQA)
			project := state.ProjectState{
				Slug: "demo", OriginalGoal: "goal", AcceptanceCriteria: []string{"criterion"}, WorktreePath: t.TempDir(),
				Status: state.StatusStopped, CurrentPhase: string(pipeline.PhaseDevelopment), CurrentSubphase: test.cursor,
				PhaseHistory: []state.PhaseRecord{
					{Phase: string(pipeline.PhaseAcceptanceCriteria), Status: state.StatusFinished},
					{Phase: string(pipeline.PhaseDevelopment), Subphase: "implementation", Status: state.StatusFinished},
					{Phase: string(pipeline.PhaseDevelopment), Subphase: test.cursor, Status: test.status},
				},
			}
			store := &persistedResumeState{project: project}
			runner := &finiteRunner{}
			req := resumeRequest(t, project, plan)
			if _, err := orchestrator.NewController(
				orchestrator.WithRunner(runner),
				orchestrator.WithPhaseState(store),
				orchestrator.WithPromptBuilder(fakePrompt{}),
			).Resume(context.Background(), orchestrator.ResumeRequest{ProjectSlug: project.Slug, RunID: req.RunID, Execution: req}); err != nil {
				t.Fatalf("Resume() error = %v", err)
			}
			if !reflect.DeepEqual(runner.calls, test.want) {
				t.Fatalf("resume dispatches = %v, want %v", runner.calls, test.want)
			}
		})
	}
}
