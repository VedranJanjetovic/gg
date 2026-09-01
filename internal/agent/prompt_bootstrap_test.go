package agent

import (
	"strings"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/state"
)

func bootstrapPromptInput(requested bool) PromptInput {
	return PromptInput{
		Phase:              pipeline.PhasePlanning,
		ProjectGoal:        "Add the fix-checks escape hatch",
		AcceptanceCriteria: []string{"The blocked checks become executable"},
		Project: state.ProjectState{
			Slug: "demo",
			Verification: &state.VerificationState{
				PlannedSteps:       []state.VerificationStep{{Name: "affected-unit-tests", Command: "go", Args: []string{"test"}}},
				BootstrapRequested: requested,
				CurrentResults: []state.VerificationCommandResult{
					{CheckName: "affected-unit-tests", Command: "go", Args: []string{"test", "./..."}, Status: "unavailable", UnavailableErr: "go: command not found", LogPath: ".gg/logs/unit.log"},
					{CheckName: "gofmt", Command: "gofmt", Args: []string{"-l", "."}, Status: "passed"},
				},
			},
		},
		PhaseContract:    "Write the plan artifact.",
		WorkingDirectory: "/tmp/worktree",
		RunID:            "run-1",
	}
}

func TestPlanningPromptDemandsOneLeadingRepairPhaseWhenABootstrapIsRequested(t *testing.T) {
	prompt, err := StandalonePromptBuilder{}.BuildPrompt(bootstrapPromptInput(true))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"## Verification bootstrap phase",
		"affected-unit-tests",
		"go test ./...",
		"go: command not found",
		".gg/logs/unit.log",
		"EXACTLY ONE new phase",
		"FIRST phase in execution order",
		"at most 10 phases in total",
		"Trivial plan must have exactly 1 phase",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("planning prompt is missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "\"gofmt\"") {
		t.Fatalf("planning prompt named the healthy check gofmt as blocked:\n%s", prompt)
	}
}

func TestPlanningPromptCarriesNoBootstrapSectionWithoutARequest(t *testing.T) {
	prompt, err := StandalonePromptBuilder{}.BuildPrompt(bootstrapPromptInput(false))
	if err != nil {
		t.Fatal(err)
	}
	for _, unwanted := range []string{"Verification bootstrap phase", "EXACTLY ONE new phase", "go: command not found"} {
		if strings.Contains(prompt, unwanted) {
			t.Fatalf("planning prompt contains %q without a bootstrap request:\n%s", unwanted, prompt)
		}
	}
}

func TestPlanningPromptCarriesNoBootstrapSectionWhenNoCheckIsBlocking(t *testing.T) {
	input := bootstrapPromptInput(true)
	input.Project.Verification.CurrentResults = []state.VerificationCommandResult{{CheckName: "gofmt", Command: "gofmt", Status: "passed"}}
	prompt, err := StandalonePromptBuilder{}.BuildPrompt(input)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(prompt, "Verification bootstrap phase") {
		t.Fatalf("planning prompt demanded a repair phase with nothing blocking:\n%s", prompt)
	}
}
