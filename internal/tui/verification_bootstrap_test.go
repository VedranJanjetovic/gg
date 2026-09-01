package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/state"
	tea "github.com/charmbracelet/bubbletea"
)

func parkedChecksProject(t *testing.T, paused bool) state.ProjectState {
	t.Helper()
	project := testProject(testConfiguredSnapshot(t), state.StatusStopped, string(pipeline.PhaseDevelopment), "", nil)
	status := "passed"
	if paused {
		status = "unavailable"
	}
	project.Verification = &state.VerificationState{
		PlannedSteps:   []state.VerificationStep{{Name: "affected-unit-tests", Command: "go", Args: []string{"test"}}},
		CurrentResults: []state.VerificationCommandResult{{CheckName: "affected-unit-tests", Command: "go", Args: []string{"test"}, Status: status, UnavailableErr: "docker is not running", LogPath: ".gg/logs/unit.log"}},
		NextAction:     "make every planned verification step executable, then resume",
	}
	return project
}

func checksActions() Actions {
	return Actions{
		ChecksPaused: true,
		SkipChecks:   func(context.Context) error { return nil },
		FixChecks:    func(context.Context) error { return nil },
	}
}

func TestTheFooterOffersSkipAndFixChecksOnlyWhileVerificationIsParked(t *testing.T) {
	tests := []struct {
		name   string
		paused bool
		want   bool
	}{
		{name: "parked on an unavailable check", paused: true, want: true},
		{name: "every check classified", paused: false, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model, err := NewModel(context.Background(), parkedChecksProject(t, test.paused), nil, checksActions(), WithColor(false))
			if err != nil {
				t.Fatal(err)
			}
			view := model.View()
			for _, legend := range []string{"k skip checks", "f fix checks"} {
				if got := strings.Contains(view, legend); got != test.want {
					t.Fatalf("legend %q shown = %t, want %t:\n%s", legend, got, test.want, view)
				}
			}
		})
	}
}

func TestPressingFixChecksWhileVerificationIsNotParkedProducesANotice(t *testing.T) {
	model, err := NewModel(context.Background(), parkedChecksProject(t, false), nil, checksActions(), WithColor(false))
	if err != nil {
		t.Fatal(err)
	}
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	if command != nil {
		t.Fatal("pressing f fired an action although verification is not parked")
	}
	next := updated.(Model)
	if next.fixChecksConfirm {
		t.Fatal("pressing f opened a confirmation although verification is not parked")
	}
	if !strings.Contains(next.notice, "verification is not parked") {
		t.Fatalf("notice = %q, want it to explain why f did nothing", next.notice)
	}
}

func TestSkipAndFixChecksBothConfirmBeforeTheyChangeDurableState(t *testing.T) {
	tests := []struct {
		name string
		key  string
	}{
		{name: "skip", key: "k"},
		{name: "fix", key: "f"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model, err := NewModel(context.Background(), parkedChecksProject(t, true), nil, checksActions(), WithColor(false))
			if err != nil {
				t.Fatal(err)
			}
			updated, command := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(test.key)})
			if command != nil {
				t.Fatal("the action fired before the user confirmed it")
			}
			confirming := updated.(Model)
			if !strings.Contains(confirming.notice, "Confirm") {
				t.Fatalf("notice = %q, want a confirmation prompt", confirming.notice)
			}
			if view := confirming.View(); !strings.Contains(view, "y/Enter confirm  n/Esc cancel") || strings.Contains(view, "f fix checks") {
				t.Fatalf("confirmation view must show the answer keys instead of the legend:\n%s", view)
			}
			cancelled, _ := confirming.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
			if next := cancelled.(Model); next.skipChecksConfirm || next.fixChecksConfirm || !strings.Contains(next.notice, "cancelled") {
				t.Fatalf("declining left confirm=%t/%t notice=%q", next.skipChecksConfirm, next.fixChecksConfirm, next.notice)
			}
			accepted, command := confirming.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
			if command == nil {
				t.Fatal("confirming did not dispatch the action")
			}
			if next := accepted.(Model); !next.skipChecksPending && !next.fixChecksPending {
				t.Fatal("confirming did not mark the action pending")
			}
		})
	}
}
