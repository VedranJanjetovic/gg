package pipeline_test

import (
	"strings"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/pipeline"
)

func TestDefaultPipelineHasStableOrderedPhases(t *testing.T) {
	t.Parallel()

	want := []struct {
		id          pipeline.PhaseID
		displayName string
		optional    bool
	}{
		{id: "acceptance_criteria", displayName: "Acceptance criteria"},
		{id: "grooming", displayName: "Grooming", optional: true},
		{id: "planning", displayName: "Planning", optional: true},
		{id: "development", displayName: "Development"},
		{id: "qa", displayName: "QA", optional: true},
		{id: "rebase", displayName: "Rebase"},
		{id: "test_document", displayName: "Test/Document"},
		{id: "build_checker", displayName: "Build checker", optional: true},
		{id: "pr", displayName: "PR", optional: true},
		{id: "ci", displayName: "CI", optional: true},
	}

	got := pipeline.DefaultPipeline().Phases()
	if len(got) != len(want) {
		t.Fatalf("default phase count = %d, want %d", len(got), len(want))
	}

	for index, wantPhase := range want {
		gotPhase := got[index]
		if gotPhase.ID() != wantPhase.id {
			t.Errorf("phase %d ID = %q, want %q", index, gotPhase.ID(), wantPhase.id)
		}
		if gotPhase.Metadata().DisplayName != wantPhase.displayName {
			t.Errorf("phase %d display name = %q, want %q", index, gotPhase.Metadata().DisplayName, wantPhase.displayName)
		}
		if gotPhase.Metadata().Optional != wantPhase.optional {
			t.Errorf("phase %d optional = %t, want %t", index, gotPhase.Metadata().Optional, wantPhase.optional)
		}
	}
}

func TestPipelinePhasesReturnsCopy(t *testing.T) {
	t.Parallel()

	defaultPipeline := pipeline.DefaultPipeline()
	phases := defaultPipeline.Phases()
	phases[0] = pipeline.Phase{}

	phase := defaultPipeline.Phases()[0]
	if phase.ID() != pipeline.PhaseAcceptanceCriteria {
		t.Errorf("first phase ID after modifying returned slice = %q, want %q", phase.ID(), pipeline.PhaseAcceptanceCriteria)
	}
	if phase.Metadata().DisplayName != "Acceptance criteria" {
		t.Errorf("first phase display name after modifying returned slice = %q, want %q", phase.Metadata().DisplayName, "Acceptance criteria")
	}
}

func TestGenerateDevelopmentSubphasesUsesStableDefaults(t *testing.T) {
	t.Parallel()

	got, err := pipeline.GenerateDevelopmentSubphases(pipeline.DevelopmentSubphaseGeneration{})
	if err != nil {
		t.Fatalf("GenerateDevelopmentSubphases() error = %v", err)
	}

	want := []struct {
		id          pipeline.DevelopmentSubphaseID
		displayName string
	}{
		{id: pipeline.DevelopmentSubphaseImplementation, displayName: "Implementation"},
		{id: pipeline.DevelopmentSubphaseTesting, displayName: "Testing"},
		{id: pipeline.DevelopmentSubphaseReview, displayName: "Review"},
	}
	if len(got) != len(want) {
		t.Fatalf("generated subphase count = %d, want %d", len(got), len(want))
	}
	for index, wantSubphase := range want {
		if got[index].ID() != wantSubphase.id {
			t.Errorf("subphase %d ID = %q, want %q", index, got[index].ID(), wantSubphase.id)
		}
		if got[index].DisplayName() != wantSubphase.displayName {
			t.Errorf("subphase %d display name = %q, want %q", index, got[index].DisplayName(), wantSubphase.displayName)
		}
	}
}

func TestGenerateDevelopmentSubphasesOverridesAndDisables(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		generation pipeline.DevelopmentSubphaseGeneration
		want       []struct {
			id          pipeline.DevelopmentSubphaseID
			displayName string
		}
	}{
		{
			name: "override preserves caller order",
			generation: pipeline.DevelopmentSubphaseGeneration{
				Mode: pipeline.DevelopmentSubphasesOverride,
				Subphases: []pipeline.DevelopmentSubphaseDefinition{
					{ID: "design", DisplayName: "Design"},
					{ID: "build", DisplayName: "Build the feature"},
				},
			},
			want: []struct {
				id          pipeline.DevelopmentSubphaseID
				displayName string
			}{
				{id: "design", displayName: "Design"},
				{id: "build", displayName: "Build the feature"},
			},
		},
		{
			name:       "disabled produces no subphases",
			generation: pipeline.DevelopmentSubphaseGeneration{Mode: pipeline.DevelopmentSubphasesDisabled},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := pipeline.GenerateDevelopmentSubphases(test.generation)
			if err != nil {
				t.Fatalf("GenerateDevelopmentSubphases() error = %v", err)
			}
			if len(got) != len(test.want) {
				t.Fatalf("generated subphase count = %d, want %d", len(got), len(test.want))
			}
			for index, wantSubphase := range test.want {
				if got[index].ID() != wantSubphase.id {
					t.Errorf("subphase %d ID = %q, want %q", index, got[index].ID(), wantSubphase.id)
				}
				if got[index].DisplayName() != wantSubphase.displayName {
					t.Errorf("subphase %d display name = %q, want %q", index, got[index].DisplayName(), wantSubphase.displayName)
				}
			}
		})
	}
}

func TestGenerateDevelopmentSubphasesRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		generation pipeline.DevelopmentSubphaseGeneration
		wantError  string
	}{
		{
			name: "duplicate IDs",
			generation: pipeline.DevelopmentSubphaseGeneration{
				Mode: pipeline.DevelopmentSubphasesOverride,
				Subphases: []pipeline.DevelopmentSubphaseDefinition{
					{ID: "implement", DisplayName: "Implementation"},
					{ID: "implement", DisplayName: "Review"},
				},
			},
			wantError: "duplicate Development subphase ID",
		},
		{
			name: "empty ID",
			generation: pipeline.DevelopmentSubphaseGeneration{
				Mode:      pipeline.DevelopmentSubphasesOverride,
				Subphases: []pipeline.DevelopmentSubphaseDefinition{{DisplayName: "Implementation"}},
			},
			wantError: "empty ID",
		},
		{
			name: "empty display name",
			generation: pipeline.DevelopmentSubphaseGeneration{
				Mode:      pipeline.DevelopmentSubphasesOverride,
				Subphases: []pipeline.DevelopmentSubphaseDefinition{{ID: "implement"}},
			},
			wantError: "display name must contain one to three words",
		},
		{
			name: "overlong display name",
			generation: pipeline.DevelopmentSubphaseGeneration{
				Mode:      pipeline.DevelopmentSubphasesOverride,
				Subphases: []pipeline.DevelopmentSubphaseDefinition{{ID: "implement", DisplayName: "Implement the requested feature"}},
			},
			wantError: "display name must contain one to three words",
		},
		{
			name: "empty override",
			generation: pipeline.DevelopmentSubphaseGeneration{
				Mode: pipeline.DevelopmentSubphasesOverride,
			},
			wantError: "override cannot be empty",
		},
		{
			name: "override with default mode",
			generation: pipeline.DevelopmentSubphaseGeneration{
				Subphases: []pipeline.DevelopmentSubphaseDefinition{{ID: "implement", DisplayName: "Implementation"}},
			},
			wantError: "default Development subphases cannot include overrides",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := pipeline.GenerateDevelopmentSubphases(test.generation)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Errorf("GenerateDevelopmentSubphases() error = %v, want error containing %q", err, test.wantError)
			}
		})
	}
}

func TestGenerateDevelopmentSubphasesReturnsIndependentSlices(t *testing.T) {
	t.Parallel()

	first, err := pipeline.GenerateDevelopmentSubphases(pipeline.DevelopmentSubphaseGeneration{})
	if err != nil {
		t.Fatalf("first GenerateDevelopmentSubphases() error = %v", err)
	}
	first[0] = pipeline.DevelopmentSubphase{}

	second, err := pipeline.GenerateDevelopmentSubphases(pipeline.DevelopmentSubphaseGeneration{})
	if err != nil {
		t.Fatalf("second GenerateDevelopmentSubphases() error = %v", err)
	}
	if second[0].ID() != pipeline.DevelopmentSubphaseImplementation {
		t.Errorf("first default subphase after mutating prior result = %q, want %q", second[0].ID(), pipeline.DevelopmentSubphaseImplementation)
	}
}
