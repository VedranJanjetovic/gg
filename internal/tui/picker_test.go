package tui

import (
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/VedranJanjetovic/gg/internal/config"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/muesli/cancelreader"
)

func assertResult(t *testing.T, got, want PickerResult) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("result = %#v, want %#v", got, want)
	}
}

func pickerCatalog() config.AgentCatalog {
	return config.NewAgentCatalog(
		config.AgentCatalogEntry{Agent: config.AgentClaude, Models: []string{"sonnet", "opus"}, ModelListStatus: config.ModelListAvailable},
		config.AgentCatalogEntry{Agent: config.AgentCodex, Models: []string{"gpt-5"}, ModelListStatus: config.ModelListAvailable},
	)
}

func updatePicker(t *testing.T, picker ConfigureWizard, msg tea.Msg) ConfigureWizard {
	t.Helper()
	updated, _ := picker.Update(msg)
	got, ok := updated.(ConfigureWizard)
	if !ok {
		t.Fatalf("Update returned %T, want ConfigureWizard", updated)
	}
	return got
}

func TestAgentModelPickerNavigatesAndSelectsScopedModel(t *testing.T) {
	picker := NewConfigureWizard(pickerCatalog(), WizardDefaults{})
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyDown})
	if picker.Cursor() != 1 || picker.Agents()[picker.Cursor()].Agent != config.AgentCodex {
		t.Fatalf("agent cursor = %d, agents = %#v; want codex", picker.Cursor(), picker.Agents())
	}
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEnter})
	if picker.Screen() != ModelPickerScreen || picker.Models()[0] != "gpt-5" {
		t.Fatalf("model screen = %q, models = %#v; want only codex models", picker.Screen(), picker.Models())
	}
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEnter})
	if picker.Screen() != EffortPickerScreen || picker.Cursor() != 1 {
		t.Fatalf("screen = %q, cursor = %d; want effort screen defaulting to medium", picker.Screen(), picker.Cursor())
	}
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyDown}) // medium → high
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEnter})
	if picker.Err() != nil {
		t.Fatalf("err = %v", picker.Err())
	}
	assertResult(t, picker.Result(), PickerResult{Agent: config.AgentCodex, Model: "gpt-5", Effort: config.EffortHigh})
}

func TestAgentModelPickerWrapsNavigation(t *testing.T) {
	picker := NewConfigureWizard(pickerCatalog(), WizardDefaults{})
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyUp})
	if picker.Cursor() != 1 {
		t.Fatalf("up from first cursor = %d, want 1", picker.Cursor())
	}
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyDown})
	if picker.Cursor() != 0 {
		t.Fatalf("down from last cursor = %d, want 0", picker.Cursor())
	}
}

func TestAgentModelPickerCancelAndEOF(t *testing.T) {
	for name, test := range map[string]struct {
		message tea.Msg
		want    error
	}{
		"escape":  {tea.KeyMsg{Type: tea.KeyEsc}, ErrPickerCancelled},
		"context": {CancelMsg{Err: context.Canceled}, context.Canceled},
		"eof":     {EOFMsg{}, io.EOF},
	} {
		t.Run(name, func(t *testing.T) {
			picker := updatePicker(t, NewConfigureWizard(pickerCatalog(), WizardDefaults{}), test.message)
			if !errors.Is(picker.Err(), test.want) {
				t.Fatalf("err = %v, want %v", picker.Err(), test.want)
			}
			if picker.View() == "" {
				t.Fatal("cancelled picker rendered no user-facing message")
			}
		})
	}
}

func TestAgentModelPickerUnavailableAndEmptyModelsOfferManualEntry(t *testing.T) {
	tests := []struct {
		name   string
		status config.ModelListAvailability
		text   string
	}{
		{"unavailable", config.ModelListUnavailable, "unavailable"},
		{"empty", config.ModelListAvailable, "No catalog models"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			catalog := config.NewAgentCatalog(config.AgentCatalogEntry{Agent: config.AgentClaude, ModelListStatus: tt.status})
			picker := updatePicker(t, NewConfigureWizard(catalog, WizardDefaults{}), tea.KeyMsg{Type: tea.KeyEnter})
			view := picker.View()
			if !strings.Contains(view, "Select a model") || !strings.Contains(strings.ToLower(view), strings.ToLower(tt.text)) || !strings.Contains(view, "manually") {
				t.Fatalf("model view = %q", view)
			}
			picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEnter})
			if picker.Screen() != ModelInputScreen || picker.Err() != nil {
				t.Fatalf("screen = %q, err = %v; want manual input screen", picker.Screen(), picker.Err())
			}
			picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("provider-model")})
			picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEnter})
			if picker.Screen() != EffortPickerScreen {
				t.Fatalf("screen = %q, want effort after manual model", picker.Screen())
			}
			picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEnter})
			assertResult(t, picker.Result(), PickerResult{Agent: config.AgentClaude, Model: "provider-model", Manual: true, Effort: config.EffortMedium})
		})
	}
}

func TestAgentModelPickerManualEntryEditsAndConfirms(t *testing.T) {
	picker := NewConfigureWizard(pickerCatalog(), WizardDefaults{})
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEnter}) // claude → model screen
	// Move past both catalog models to the manual row.
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyDown})
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyDown})
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEnter})
	if picker.Screen() != ModelInputScreen {
		t.Fatalf("screen = %q, want manual input", picker.Screen())
	}
	// Empty submit is ignored.
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEnter})
	if picker.Screen() != ModelInputScreen || picker.Err() != nil {
		t.Fatalf("empty submit changed state: screen=%q err=%v", picker.Screen(), picker.Err())
	}
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("claude-x")})
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyBackspace})
	if !strings.Contains(picker.View(), "claude-") {
		t.Fatalf("input view = %q", picker.View())
	}
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y-model")})
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEnter})
	if picker.Screen() != EffortPickerScreen || picker.Err() != nil {
		t.Fatalf("screen = %q, err = %v; want effort after manual model", picker.Screen(), picker.Err())
	}
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEnter})
	assertResult(t, picker.Result(), PickerResult{Agent: config.AgentClaude, Model: "claude-y-model", Manual: true, Effort: config.EffortMedium})
}

func TestConfigureWizardPrefillsDefaultsAndTogglesPhases(t *testing.T) {
	defaults := WizardDefaults{
		Agent:  config.AgentCodex,
		Model:  "gpt-5",
		Effort: config.EffortHigh,
		Phases: []PhaseState{
			{Phase: config.PhasePlanning, Enabled: true, Description: "plan"},
			{Phase: config.PhaseQA, Enabled: true, Description: "qa"},
			{Phase: config.PhaseCI, Enabled: false, Description: "ci"},
		},
	}
	picker := NewConfigureWizard(pickerCatalog(), defaults)
	if picker.Cursor() != 1 {
		t.Fatalf("agent cursor = %d, want prefilled codex", picker.Cursor())
	}
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEnter})
	if picker.Screen() != ModelPickerScreen || picker.Models()[picker.Cursor()] != "gpt-5" {
		t.Fatalf("model screen = %q, cursor = %d; want prefilled gpt-5", picker.Screen(), picker.Cursor())
	}
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEnter})
	if picker.Screen() != EffortPickerScreen || picker.Cursor() != 2 {
		t.Fatalf("screen = %q, cursor = %d; want effort prefilled to high", picker.Screen(), picker.Cursor())
	}
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEnter})
	if picker.Screen() != PhaseToggleScreen {
		t.Fatalf("screen = %q, want phase toggles", picker.Screen())
	}
	view := picker.View()
	for _, want := range []string{"Pipeline phases (in execution order)", "[x] planning", "[x] qa", "[ ] ci", "Space toggle", "Save configuration"} {
		if !strings.Contains(view, want) {
			t.Fatalf("phase view missing %q: %q", want, view)
		}
	}
	// Toggle qa off and ci on, then confirm on the save row.
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyDown})
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeySpace})
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeySpace})
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyDown})
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEnter})
	if picker.Err() != nil {
		t.Fatalf("err = %v", picker.Err())
	}
	assertResult(t, picker.Result(), PickerResult{
		Agent: config.AgentCodex, Model: "gpt-5", Effort: config.EffortHigh,
		Phases: []PhaseState{
			{Phase: config.PhasePlanning, Enabled: true, Description: "plan"},
			{Phase: config.PhaseQA, Enabled: false, Description: "qa"},
			{Phase: config.PhaseCI, Enabled: true, Description: "ci"},
		},
	})
}

func TestConfigureWizardLockedPhasesAreShownButNotToggleable(t *testing.T) {
	defaults := WizardDefaults{Phases: []PhaseState{
		{Name: "Acceptance criteria", Enabled: true, Locked: true, Description: "always runs"},
		{Phase: config.PhaseQA, Name: "QA", Enabled: true},
		{Name: "Rebase", Enabled: true, Locked: true},
	}}
	picker := NewConfigureWizard(pickerCatalog(), defaults)
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEnter}) // agent
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEnter}) // model
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEnter}) // effort
	if picker.Screen() != PhaseToggleScreen || picker.Cursor() != 1 {
		t.Fatalf("screen = %q, cursor = %d; want cursor on the first toggleable row", picker.Screen(), picker.Cursor())
	}
	view := picker.View()
	for _, want := range []string{" ✓  Acceptance criteria", "[x] QA", " ✓  Rebase"} {
		if !strings.Contains(view, want) {
			t.Fatalf("phase view missing %q: %q", want, view)
		}
	}
	// Navigation skips locked rows: down lands on the save row, up returns.
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyDown})
	if picker.Cursor() != 3 {
		t.Fatalf("cursor = %d after down, want the save row", picker.Cursor())
	}
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyUp})
	if picker.Cursor() != 1 {
		t.Fatalf("cursor = %d after up, want back on the only toggleable row", picker.Cursor())
	}
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeySpace})
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyDown}) // save row
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEnter})
	assertResult(t, picker.Result(), PickerResult{
		Agent: config.AgentClaude, Model: "sonnet", Effort: config.EffortMedium,
		Phases: []PhaseState{
			{Name: "Acceptance criteria", Enabled: true, Locked: true, Description: "always runs"},
			{Phase: config.PhaseQA, Name: "QA", Enabled: false},
			{Name: "Rebase", Enabled: true, Locked: true},
		},
	})
}

func TestConfigureWizardPhaseToggleDoesNotMutateDefaults(t *testing.T) {
	defaults := WizardDefaults{Phases: []PhaseState{{Phase: config.PhaseQA, Enabled: true}}}
	picker := NewConfigureWizard(pickerCatalog(), defaults)
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEnter}) // agent
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEnter}) // model
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEnter}) // effort
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeySpace})
	if !defaults.Phases[0].Enabled {
		t.Fatal("toggling mutated the caller-owned defaults slice")
	}
	if picker.PhaseStates()[0].Enabled {
		t.Fatal("toggle did not flip the wizard's own phase state")
	}
}

func TestAgentModelPickerManualEntryEscReturnsToModelList(t *testing.T) {
	picker := NewConfigureWizard(pickerCatalog(), WizardDefaults{})
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEnter})
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyUp}) // wrap to the manual row
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEnter})
	if picker.Screen() != ModelInputScreen {
		t.Fatalf("screen = %q, want manual input", picker.Screen())
	}
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEsc})
	if picker.Screen() != ModelPickerScreen || picker.Err() != nil {
		t.Fatalf("screen = %q, err = %v; want back on model list", picker.Screen(), picker.Err())
	}
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEsc})
	if !errors.Is(picker.Err(), ErrPickerCancelled) {
		t.Fatalf("err = %v, want cancellation from model list", picker.Err())
	}
}

func TestAgentModelPickerEmptyAgents(t *testing.T) {
	picker := updatePicker(t, NewConfigureWizard(config.NewAgentCatalog(), WizardDefaults{}), tea.KeyMsg{Type: tea.KeyEnter})
	if !errors.Is(picker.Err(), ErrNoAgents) || !strings.Contains(picker.View(), "No supported agents") {
		t.Fatalf("err = %v, view = %q", picker.Err(), picker.View())
	}
}

func TestAgentModelPickerAgentsDeepCopyModels(t *testing.T) {
	picker := NewConfigureWizard(pickerCatalog(), WizardDefaults{})
	entries := picker.Agents()
	entries[0].Models[0] = "mutated"
	entries[0].Models = append(entries[0].Models, "also-mutated")

	got := picker.Agents()
	if got[0].Models[0] != "sonnet" || len(got[0].Models) != 2 {
		t.Fatalf("picker agents changed through returned slice: %#v", got)
	}
}

func TestEOFNotifyingReaderNotifiesOnceAndPreservesEOF(t *testing.T) {
	notifications := 0
	reader := &eofNotifyingReader{
		reader: strings.NewReader(""),
		notify: func() { notifications++ },
	}
	for range 2 {
		_, err := reader.Read(make([]byte, 1))
		if !errors.Is(err, io.EOF) {
			t.Fatalf("read error = %v, want io.EOF", err)
		}
	}
	if notifications != 1 {
		t.Fatalf("notifications = %d, want 1", notifications)
	}
}

func TestEOFNotifyingFilePreservesInterruptibleCancelReader(t *testing.T) {
	input, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer writer.Close()

	notifications := 0
	wrapped := &eofNotifyingFile{
		file:   input,
		reader: eofNotifyingReader{reader: input, notify: func() { notifications++ }},
	}
	if _, ok := any(wrapped).(cancelreader.File); !ok {
		t.Fatal("EOF wrapper no longer satisfies cancelreader.File")
	}
	reader, err := cancelreader.NewReader(wrapped)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	readDone := make(chan error, 1)
	go func() {
		_, readErr := reader.Read(make([]byte, 1))
		readDone <- readErr
	}()
	if !reader.Cancel() {
		t.Fatal("cancelreader failed to interrupt file-backed read")
	}
	select {
	case readErr := <-readDone:
		if !errors.Is(readErr, cancelreader.ErrCanceled) {
			t.Fatalf("read error = %v, want %v", readErr, cancelreader.ErrCanceled)
		}
	case <-time.After(time.Second):
		t.Fatal("file-backed read did not cancel within one second")
	}
	if notifications != 0 {
		t.Fatalf("EOF notifications = %d, want 0 after cancellation", notifications)
	}
}

func TestRunAgentModelPickerNonTTYIsSafe(t *testing.T) {
	_, err := RunConfigureWizard(context.Background(), pickerCatalog(), WizardDefaults{}, strings.NewReader(""), io.Discard)
	if !errors.Is(err, ErrPickerNonInteractive) {
		t.Fatalf("err = %v, want %v", err, ErrPickerNonInteractive)
	}
}

func TestPrepareRawModeNonFileIsNoop(t *testing.T) {
	restore, err := prepareRawMode(strings.NewReader("input"))
	if err != nil {
		t.Fatal(err)
	}
	if restore == nil {
		t.Fatal("restore callback is nil")
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareRawModeRejectsNonTerminalFile(t *testing.T) {
	input, output, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer output.Close()
	if _, err := prepareRawMode(input); err == nil {
		t.Fatal("prepareRawMode accepted a non-terminal file")
	}
}

func TestAgentModelPickerViewIsPolishedAndProviderAware(t *testing.T) {
	catalog := config.NewAgentCatalog(config.AgentCatalogEntry{
		Agent: config.AgentClaude, DisplayName: "Claude Code", Description: "Anthropic harness",
		Provider: "anthropic", Harness: "claude-code", Models: []string{"sonnet"}, ModelDescriptions: map[string]string{"sonnet": "Fast model"},
		ModelListStatus: config.ModelListAvailable,
	})
	picker := NewConfigureWizard(catalog, WizardDefaults{})
	view := picker.View()
	for _, want := range []string{"gg configure", "Choose an agent to configure", "Claude Code", "Provider: anthropic", "Harness: claude-code", "Anthropic harness", "j/k", "Enter select", "Esc cancel"} {
		if !strings.Contains(view, want) {
			t.Fatalf("agent view = %q, missing %q", view, want)
		}
	}
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEnter})
	view = picker.View()
	for _, want := range []string{"Select a model for Claude Code", "Provider: anthropic", "Harness: claude-code", "sonnet", "Fast model"} {
		if !strings.Contains(view, want) {
			t.Fatalf("model view = %q, missing %q", view, want)
		}
	}
}

func TestAgentModelPickerWindowSizeTruncatesRows(t *testing.T) {
	catalog := config.NewAgentCatalog(config.AgentCatalogEntry{Agent: config.AgentClaude, DisplayName: "A very long agent name", Description: "A very long description"})
	picker := updatePicker(t, NewConfigureWizard(catalog, WizardDefaults{}), tea.WindowSizeMsg{Width: 12, Height: 8})
	view := picker.View()
	if !strings.Contains(view, "…") {
		t.Fatalf("narrow view was not truncated: %q", view)
	}
	if picker.width != 12 || picker.height != 8 {
		t.Fatalf("size = %dx%d, want 12x8", picker.width, picker.height)
	}
}

func TestAgentModelPickerUnavailableStateHasGuidance(t *testing.T) {
	picker := NewConfigureWizard(config.NewAgentCatalog(config.AgentCatalogEntry{Agent: config.AgentClaude, DisplayName: "Claude Code", Provider: "anthropic", ModelListStatus: config.ModelListUnavailable}), WizardDefaults{})
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEnter})
	view := picker.View()
	if !strings.Contains(view, "Model list unavailable") || !strings.Contains(view, "Esc cancel") {
		t.Fatalf("unavailable view = %q", view)
	}
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEnter})
	if picker.Screen() != ModelInputScreen || !strings.Contains(picker.View(), "Enter a model name for Claude Code") {
		t.Fatalf("state = screen %q, view %q", picker.Screen(), picker.View())
	}
}

func TestConfigureWizardPerPhaseOverrideSubFlow(t *testing.T) {
	defaults := WizardDefaults{
		Agent: config.AgentClaude, Model: "sonnet", Effort: config.EffortMedium,
		Phases: []PhaseState{
			{Phase: config.PhaseGrooming, Name: "Grooming", Enabled: true, Locked: true, Agent: config.AgentClaude, Model: "sonnet", Effort: config.EffortMedium},
			{Phase: config.PhaseQA, Name: "QA", Enabled: true, Description: "qa", Agent: config.AgentClaude, Model: "sonnet", Effort: config.EffortMedium},
		},
	}
	picker := NewConfigureWizard(pickerCatalog(), defaults)
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEnter}) // agent
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEnter}) // model
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEnter}) // effort
	if picker.Screen() != PhaseToggleScreen || picker.Cursor() != 0 {
		t.Fatalf("screen = %q, cursor = %d; want cursor on grooming (locked but overridable)", picker.Screen(), picker.Cursor())
	}
	if view := picker.View(); !strings.Contains(view, "claude · sonnet · medium") || !strings.Contains(view, "agent/model/effort") || !strings.Contains(view, "Save configuration") {
		t.Fatalf("phase view missing settings line, footer hint, or save row: %q", view)
	}

	// Esc inside the sub-flow returns to the phase screen without changes.
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("o")})
	if picker.Screen() != AgentPickerScreen || !strings.Contains(picker.View(), "Choose the agent for phase Grooming") {
		t.Fatalf("override start: screen = %q view = %q", picker.Screen(), picker.View())
	}
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEsc})
	if picker.Screen() != PhaseToggleScreen || picker.Err() != nil || picker.Cursor() != 0 {
		t.Fatalf("esc in sub-flow: screen = %q err = %v cursor = %d", picker.Screen(), picker.Err(), picker.Cursor())
	}

	// Override QA to codex/gpt-5/high.
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyDown})
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("o")})
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyDown})  // claude → codex
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEnter}) // agent codex
	if picker.Screen() != ModelPickerScreen || !strings.Contains(picker.View(), "(phase QA)") {
		t.Fatalf("override model screen: %q %q", picker.Screen(), picker.View())
	}
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEnter}) // model gpt-5
	if picker.Screen() != EffortPickerScreen || !strings.Contains(picker.View(), "Select effort for phase QA") {
		t.Fatalf("override effort screen: %q %q", picker.Screen(), picker.View())
	}
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyDown})  // medium → high
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEnter}) // effort high
	if picker.Screen() != PhaseToggleScreen || picker.Cursor() != 1 {
		t.Fatalf("after override: screen = %q cursor = %d; want back on QA", picker.Screen(), picker.Cursor())
	}
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyDown})  // save row
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEnter}) // confirm
	if picker.Err() != nil {
		t.Fatalf("err = %v", picker.Err())
	}
	assertResult(t, picker.Result(), PickerResult{
		Agent: config.AgentClaude, Model: "sonnet", Effort: config.EffortMedium,
		Phases: []PhaseState{
			{Phase: config.PhaseGrooming, Name: "Grooming", Enabled: true, Locked: true, Agent: config.AgentClaude, Model: "sonnet", Effort: config.EffortMedium},
			{Phase: config.PhaseQA, Name: "QA", Enabled: true, Description: "qa", Agent: config.AgentCodex, Model: "gpt-5", Effort: config.EffortHigh},
		},
	})
}

func TestConfigureWizardOverrideManualModelEntry(t *testing.T) {
	defaults := WizardDefaults{
		Agent: config.AgentClaude, Model: "sonnet", Effort: config.EffortMedium,
		Phases: []PhaseState{{Phase: config.PhaseQA, Name: "QA", Enabled: true, Agent: config.AgentClaude, Model: "sonnet", Effort: config.EffortMedium}},
	}
	picker := NewConfigureWizard(pickerCatalog(), defaults)
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEnter}) // agent
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEnter}) // model
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEnter}) // effort
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("o")})
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEnter}) // keep claude
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyUp})    // wrap to manual entry row
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEnter})
	if picker.Screen() != ModelInputScreen {
		t.Fatalf("screen = %q, want manual model input", picker.Screen())
	}
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("claude-custom")})
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEnter})
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEnter}) // effort medium
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyDown})  // save row
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEnter}) // confirm phases
	if picker.Err() != nil {
		t.Fatalf("err = %v", picker.Err())
	}
	result := picker.Result()
	if result.Manual || result.Model != "sonnet" {
		t.Fatalf("global result changed by phase override: %#v", result)
	}
	qa := result.Phases[0]
	// Agent and effort match the global selection, so only the model is
	// pinned; the empty fields keep inheriting the global defaults.
	if qa.Agent != "" || qa.Model != "claude-custom" || qa.Effort != "" {
		t.Fatalf("qa override = %#v, want only the model pinned", qa)
	}
}

func TestViewsWrapLongTextAtWordBoundaries(t *testing.T) {
	longDescription := strings.Repeat("meaningful ", 12) + "end"
	defaults := WizardDefaults{Phases: []PhaseState{{Phase: config.PhaseQA, Name: "QA", Enabled: true, Description: longDescription}}}
	picker := NewConfigureWizard(pickerCatalog(), defaults)
	picker = updatePicker(t, picker, tea.WindowSizeMsg{Width: 40, Height: 24})
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEnter}) // agent
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEnter}) // model
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEnter}) // effort
	for _, line := range strings.Split(picker.View(), "\n") {
		if strings.Contains(line, "meaningful") && len([]rune(line)) > 40 {
			t.Fatalf("phase description line exceeds the terminal width: %q", line)
		}
	}
	if !strings.Contains(picker.View(), "meaningful") {
		t.Fatalf("wrapped description dropped content: %q", picker.View())
	}

	question := NewQuestionPrompt([]string{strings.Repeat("clarify ", 15) + "?"})
	updated, _ := question.Update(tea.WindowSizeMsg{Width: 40, Height: 24})
	view := updated.(QuestionPrompt).View()
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "clarify") && len([]rune(line)) > 40 {
			t.Fatalf("question line exceeds the terminal width: %q", line)
		}
	}
}

func TestConfigureWizardPhasesFollowNewlyPickedGlobalDefaults(t *testing.T) {
	defaults := WizardDefaults{
		Agent: config.AgentClaude, Model: "sonnet", Effort: config.EffortMedium,
		Phases: []PhaseState{{Phase: config.PhaseQA, Name: "QA", Enabled: true}}, // no pins: inherits globals
	}
	picker := NewConfigureWizard(pickerCatalog(), defaults)
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEnter}) // claude
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyDown})  // sonnet → opus
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEnter}) // opus
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyUp})    // medium → low
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEnter}) // low
	view := picker.View()
	if !strings.Contains(view, "claude · opus · low") || strings.Contains(view, "medium") {
		t.Fatalf("unpinned phase must show the freshly picked globals, not stale values: %q", view)
	}
	// The override editor prefills with the new globals too.
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEnter}) // edit QA
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEnter}) // keep claude
	if picker.Models()[picker.Cursor()] != "opus" {
		t.Fatalf("model prefill = %q, want the newly picked global model", picker.Models()[picker.Cursor()])
	}
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEnter}) // keep opus
	if picker.Screen() != EffortPickerScreen || wizardEfforts[picker.Cursor()] != config.EffortLow {
		t.Fatalf("effort prefill = %q, want the newly picked global effort", wizardEfforts[picker.Cursor()])
	}
	picker = updatePicker(t, picker, tea.KeyMsg{Type: tea.KeyEnter}) // keep low
	if qa := picker.PhaseStates()[0]; qa.pinned() {
		t.Fatalf("re-picking the globals must leave the phase unpinned: %#v", qa)
	}
}
