package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/state"
	tea "github.com/charmbracelet/bubbletea"
)

func TestProjectFooterAdvertisesUpdateOnlyWhenTheCheckReportsOne(t *testing.T) {
	tests := []struct {
		name    string
		message updateAvailableMsg
		want    bool
	}{
		{name: "available", message: updateAvailableMsg{available: true}, want: true},
		{name: "up to date", message: updateAvailableMsg{}, want: false},
		{name: "check failed", message: updateAvailableMsg{available: true, err: errors.New("no network")}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			project := testProject(testConfiguredSnapshot(t), state.StatusStopped, string(pipeline.PhaseDevelopment), "", nil)
			model, err := NewModel(context.Background(), project, nil, Actions{}, WithColor(false), WithUpdateChecker(func(context.Context) (bool, error) { return true, nil }))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(model.View(), updateNotice) {
				t.Fatalf("footer rendered before the check completed:\n%s", model.View())
			}
			updated, _ := model.Update(test.message)
			view := updated.(Model).View()
			if got := strings.Contains(view, updateNotice); got != test.want {
				t.Fatalf("footer shown = %t, want %t:\n%s", got, test.want, view)
			}
			if test.want && !strings.HasSuffix(view, updateNotice+"\n") {
				t.Fatalf("notice is not the last line:\n%s", view)
			}
			if test.want && strings.Count(view, updateNotice) != 1 {
				t.Fatalf("notice rendered %d times:\n%s", strings.Count(view, updateNotice), view)
			}
		})
	}
}

// A pending action short-circuits the body render; the footer must survive it,
// because that is exactly when a long-lived session sits on one screen.
func TestProjectFooterSurvivesPendingActionRender(t *testing.T) {
	project := testProject(testConfiguredSnapshot(t), state.StatusPending, string(pipeline.PhaseDevelopment), "", nil)
	model, err := NewModel(context.Background(), project, nil, Actions{Start: func(context.Context) error { return nil }}, WithColor(false))
	if err != nil {
		t.Fatal(err)
	}
	updated, _ := model.Update(updateAvailableMsg{available: true})
	view := updated.(Model).View()
	if !strings.Contains(view, "Starting pipeline…") || !strings.Contains(view, updateNotice) {
		t.Fatalf("pending-action view lost the update footer:\n%s", view)
	}
}

func TestProjectInitSchedulesTheUpdateCheck(t *testing.T) {
	project := testProject(testConfiguredSnapshot(t), state.StatusStopped, string(pipeline.PhaseDevelopment), "", nil)
	model, err := NewModel(context.Background(), project, nil, Actions{}, WithColor(false), WithUpdateChecker(func(context.Context) (bool, error) { return true, nil }))
	if err != nil {
		t.Fatal(err)
	}
	batch, ok := model.Init()().(tea.BatchMsg)
	if !ok {
		t.Fatal("Init did not batch commands")
	}
	scheduled := false
	for _, command := range batch {
		if message, ok := command().(updateAvailableMsg); ok && message.available {
			scheduled = true
		}
	}
	if !scheduled {
		t.Fatal("Init did not schedule the update check")
	}
}

func TestGlobalFooterAdvertisesUpdateWhenTheCheckReportsOne(t *testing.T) {
	controller, err := NewGlobalController(
		func(context.Context) ([]string, error) { return nil, nil },
		func(context.Context, string) ([]state.ProjectState, error) { return nil, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	model, err := NewGlobalModel(context.Background(), controller, WithGlobalRefreshInterval(time.Hour), WithGlobalUpdateChecker(func(context.Context) (bool, error) { return true, nil }))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(model.View(), updateNotice) {
		t.Fatalf("footer rendered before the check completed:\n%s", model.View())
	}
	updated, _ := model.Update(updateAvailableMsg{available: true})
	view := updated.(GlobalModel).View()
	if !strings.HasSuffix(view, updateNotice+"\n") {
		t.Fatalf("notice is not the last line:\n%s", view)
	}
	// A later failed check must not retract a notice already known to be true.
	failed, _ := updated.(GlobalModel).Update(updateAvailableMsg{err: errors.New("no network")})
	if !strings.Contains(failed.(GlobalModel).View(), updateNotice) {
		t.Fatalf("failed check retracted the footer:\n%s", failed.(GlobalModel).View())
	}
}
