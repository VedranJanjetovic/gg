package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestChoiceDefaultsToInheritAndNamesBothCreationPaths(t *testing.T) {
	model := choiceModel{title: "Configure this new project", options: []ChoiceOption{
		{Label: "Inherit folder configuration"},
		{Label: "Pick configuration for this project"},
	}, chosen: -1}
	if model.cursor != 0 {
		t.Fatalf("initial choice cursor = %d, want inherit at index 0", model.cursor)
	}
	view := model.View()
	for _, want := range []string{"Inherit folder configuration", "Pick configuration for this project"} {
		if !strings.Contains(view, want) {
			t.Fatalf("choice view missing %q: %q", want, view)
		}
	}
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := updated.(choiceModel).chosen; got != 0 {
		t.Fatalf("chosen index = %d, want default inherit index 0", got)
	}
}
