package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/state"
)

// A running project whose only strict verification results belong to
// quarantined checks was rendered as running[paused] with the skip/fix keys
// still offered, although the pipeline was progressing. The retained preflight
// evidence of a quarantined check must not present the run as parked.
func TestRunningProjectWithQuarantinedChecksIsNotPresentedAsParked(t *testing.T) {
	project := testProject(testConfiguredSnapshot(t), state.StatusRunning, string(pipeline.PhaseDevelopment), "", nil)
	project.Verification = &state.VerificationState{
		CurrentResults: []state.VerificationCommandResult{{CheckName: "affected-unit-tests", Status: "unclassifiable"}},
		QuarantinedChecks: []state.VerificationQuarantine{
			{CheckName: "affected-unit-tests", BaselineStatus: "unclassifiable"},
		},
	}
	model, err := NewModel(context.Background(), project, nil, checksActions(), WithColor(false))
	if err != nil {
		t.Fatal(err)
	}
	view := model.View()
	if !strings.Contains(view, "Status: running") {
		t.Fatalf("view lost the running status word:\n%s", view)
	}
	if strings.Contains(view, "paused") {
		t.Fatalf("running project presented as paused:\n%s", view)
	}
	for _, legend := range []string{"k skip checks", "f fix checks"} {
		if strings.Contains(view, legend) {
			t.Fatalf("legend %q offered although the run is progressing:\n%s", legend, view)
		}
	}
}

// A run genuinely parked by verification closes as stopped; the view renders
// it as the single word "paused" instead of the contradictory two-part
// "stopped [paused]" display.
func TestParkedVerificationRendersThePausedStatusWord(t *testing.T) {
	model, err := NewModel(context.Background(), parkedChecksProject(t, true), nil, checksActions(), WithColor(false))
	if err != nil {
		t.Fatal(err)
	}
	view := model.View()
	if !strings.Contains(view, "Status: paused") {
		t.Fatalf("parked run did not render as paused:\n%s", view)
	}
	if strings.Contains(view, "Status: stopped") {
		t.Fatalf("parked run still rendered as stopped:\n%s", view)
	}
}
