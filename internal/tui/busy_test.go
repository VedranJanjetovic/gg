package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestBusyScreenCompletesWithWorkError(t *testing.T) {
	wantErr := errors.New("generator failed")
	model := NewBusyScreen("gg grooming", "Checking the project description for open questions…", nil, func() tea.Msg {
		return busyDoneMsg{err: wantErr}
	})
	if view := model.View(); !strings.Contains(view, "gg grooming") || !strings.Contains(view, "Checking the project description") {
		t.Fatalf("view = %q", view)
	}
	updated, cmd := model.Update(busyDoneMsg{err: wantErr})
	screen := updated.(BusyScreen)
	if !errors.Is(screen.Err(), wantErr) || cmd == nil {
		t.Fatalf("err = %v, cmd = %v; want stored error and quit", screen.Err(), cmd)
	}
}

func TestBusyScreenEscCancelsWorkContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	model := NewBusyScreen("gg grooming", "working", cancel, nil)
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	screen := updated.(BusyScreen)
	if !screen.Cancelled() {
		t.Fatal("esc must mark the screen cancelled")
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("esc must cancel the work context")
	}
	if view := screen.View(); !strings.Contains(view, "Cancelling…") {
		t.Fatalf("view = %q, want cancelling feedback", view)
	}
}
