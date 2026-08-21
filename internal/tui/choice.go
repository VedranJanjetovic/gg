package tui

import (
	"context"
	"io"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// ChoiceOption is one selectable row of a single-choice prompt.
type ChoiceOption struct {
	Label       string
	Description string
}

type choiceModel struct {
	title   string
	options []ChoiceOption
	cursor  int
	chosen  int
	width   int
	err     error
}

func (m choiceModel) Init() tea.Cmd { return nil }

func (m choiceModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch message := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = message.Width
		return m, nil
	case CancelMsg, EOFMsg:
		m.err = ErrPickerCancelled
		return m, tea.Quit
	case tea.KeyMsg:
		switch message.String() {
		case "esc", "ctrl+c", "q":
			m.err = ErrPickerCancelled
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.options)-1 {
				m.cursor++
			}
		case "enter":
			m.chosen = m.cursor
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m choiceModel) View() string {
	styles := pickerStyles()
	width := m.width
	if width <= 0 {
		width = 80
	}
	var b strings.Builder
	b.WriteString(styles.title.Render(m.title))
	b.WriteString("\n\n")
	for i, option := range m.options {
		b.WriteString(renderPickerRow(width, i == m.cursor, option.Label, option.Description, styles))
	}
	b.WriteString("\n")
	b.WriteString(styles.footer.Render("↑/↓ move  Enter choose  Esc cancel"))
	return b.String() + "\n"
}

// RunChoicePrompt shows a full-screen single-choice picker and returns the
// chosen option index, or ErrPickerCancelled when the user backs out.
func RunChoicePrompt(ctx context.Context, title string, options []ChoiceOption, input io.Reader, output io.Writer) (int, error) {
	if !interactiveTerminal(input, output) {
		return -1, ErrPickerNonInteractive
	}
	model := choiceModel{title: title, options: options, chosen: -1}
	final, err := runAltScreenProgram(ctx, model, input, output)
	if err != nil {
		return -1, err
	}
	chosen, ok := final.(choiceModel)
	if !ok {
		return -1, ErrPickerCancelled
	}
	if chosen.err != nil {
		return -1, chosen.err
	}
	if chosen.chosen < 0 {
		return -1, ErrPickerCancelled
	}
	return chosen.chosen, nil
}
