package tui

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func updateProjectPrompt(t *testing.T, prompt ProjectPrompt, msg tea.Msg) ProjectPrompt {
	t.Helper()
	updated, _ := prompt.Update(msg)
	got, ok := updated.(ProjectPrompt)
	if !ok {
		t.Fatalf("Update returned %T, want ProjectPrompt", updated)
	}
	return got
}

func typeProjectText(t *testing.T, prompt ProjectPrompt, text string) ProjectPrompt {
	t.Helper()
	return updateProjectPrompt(t, prompt, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text)})
}

func TestProjectPromptEchoesTypingAndConfirmsWithProjectInfo(t *testing.T) {
	prompt := NewProjectPrompt(func(description string) string {
		return "Preview: " + strings.SplitN(description, "\n", 2)[0]
	})
	prompt = typeProjectText(t, prompt, "Build supermario in the browser.")
	if !strings.Contains(prompt.View(), "Build supermario in the browser.") {
		t.Fatalf("typed text is not echoed: %q", prompt.View())
	}
	prompt = updateProjectPrompt(t, prompt, tea.KeyMsg{Type: tea.KeyEnter})
	prompt = typeProjectText(t, prompt, "Use canvas rendering.")
	prompt = updateProjectPrompt(t, prompt, tea.KeyMsg{Type: tea.KeyEnter})
	// Second Enter on the now-empty line opens the confirmation screen.
	prompt = updateProjectPrompt(t, prompt, tea.KeyMsg{Type: tea.KeyEnter})
	if prompt.Screen() != ProjectConfirmScreen {
		t.Fatalf("screen = %q, want confirmation after empty-line Enter", prompt.Screen())
	}
	view := prompt.View()
	for _, want := range []string{"Create this project?", "Preview: Build supermario in the browser.", "Use canvas rendering.", "Enter create project", "Esc keep editing"} {
		if !strings.Contains(view, want) {
			t.Fatalf("confirmation view missing %q: %q", want, view)
		}
	}
	prompt = updateProjectPrompt(t, prompt, tea.KeyMsg{Type: tea.KeyEnter})
	if prompt.Err() != nil || prompt.Result() != "Build supermario in the browser.\nUse canvas rendering." {
		t.Fatalf("result = %q, err = %v", prompt.Result(), prompt.Err())
	}
}

func TestProjectPromptConfirmEscReturnsToEditing(t *testing.T) {
	prompt := NewProjectPrompt(nil)
	prompt = typeProjectText(t, prompt, "A tiny tool")
	prompt = updateProjectPrompt(t, prompt, tea.KeyMsg{Type: tea.KeyEnter})
	prompt = updateProjectPrompt(t, prompt, tea.KeyMsg{Type: tea.KeyEnter})
	if prompt.Screen() != ProjectConfirmScreen {
		t.Fatalf("screen = %q, want confirmation", prompt.Screen())
	}
	prompt = updateProjectPrompt(t, prompt, tea.KeyMsg{Type: tea.KeyEsc})
	if prompt.Screen() != ProjectDescribeScreen || prompt.Err() != nil {
		t.Fatalf("screen = %q, err = %v; want back on the editor", prompt.Screen(), prompt.Err())
	}
	prompt = typeProjectText(t, prompt, "with more detail")
	prompt = updateProjectPrompt(t, prompt, tea.KeyMsg{Type: tea.KeyEnter})
	prompt = updateProjectPrompt(t, prompt, tea.KeyMsg{Type: tea.KeyEnter})
	prompt = updateProjectPrompt(t, prompt, tea.KeyMsg{Type: tea.KeyEnter})
	if prompt.Result() != "A tiny tool\nwith more detail" {
		t.Fatalf("result = %q", prompt.Result())
	}
}

func TestProjectPromptEmptyDescriptionDoesNotAdvance(t *testing.T) {
	prompt := NewProjectPrompt(nil)
	prompt = updateProjectPrompt(t, prompt, tea.KeyMsg{Type: tea.KeyEnter})
	prompt = updateProjectPrompt(t, prompt, tea.KeyMsg{Type: tea.KeyEnter})
	if prompt.Screen() != ProjectDescribeScreen || prompt.Err() != nil {
		t.Fatalf("empty description advanced: screen=%q err=%v", prompt.Screen(), prompt.Err())
	}
}

func TestProjectPromptBackspaceJoinsPreviousLine(t *testing.T) {
	prompt := NewProjectPrompt(nil)
	prompt = typeProjectText(t, prompt, "abc")
	prompt = updateProjectPrompt(t, prompt, tea.KeyMsg{Type: tea.KeyEnter})
	prompt = updateProjectPrompt(t, prompt, tea.KeyMsg{Type: tea.KeyBackspace})
	prompt = updateProjectPrompt(t, prompt, tea.KeyMsg{Type: tea.KeyBackspace})
	if !strings.Contains(prompt.View(), "ab▏") {
		t.Fatalf("backspace did not rejoin the previous line: %q", prompt.View())
	}
}

func TestProjectPromptEscCancels(t *testing.T) {
	prompt := updateProjectPrompt(t, NewProjectPrompt(nil), tea.KeyMsg{Type: tea.KeyEsc})
	if !errors.Is(prompt.Err(), ErrPickerCancelled) {
		t.Fatalf("err = %v, want %v", prompt.Err(), ErrPickerCancelled)
	}
	if !strings.Contains(prompt.View(), "cancelled") {
		t.Fatalf("cancelled view = %q", prompt.View())
	}
}

func TestRunProjectPromptNonTTYIsSafe(t *testing.T) {
	_, err := RunProjectPrompt(context.Background(), nil, strings.NewReader(""), io.Discard)
	if !errors.Is(err, ErrPickerNonInteractive) {
		t.Fatalf("err = %v, want %v", err, ErrPickerNonInteractive)
	}
}
