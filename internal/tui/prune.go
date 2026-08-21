package tui

import (
	"context"
	"errors"
	"io"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// PruneItem is one prunable project row; Selected defaults to true.
type PruneItem struct {
	Slug     string
	Name     string
	Status   string
	Selected bool
}

// PrunePrompt is the full-screen multi-select confirmation for gg prune.
type PrunePrompt struct {
	items     []PruneItem
	cursor    int
	confirmed bool
	err       error
	quit      bool
	width     int
}

func NewPrunePrompt(items []PruneItem) PrunePrompt {
	copied := append([]PruneItem(nil), items...)
	return PrunePrompt{items: copied, width: 80}
}

func (m PrunePrompt) Init() tea.Cmd { return nil }

func (m PrunePrompt) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch message := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = message.Width
		return m, nil
	case tea.KeyMsg:
		switch message.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.items)-1 {
				m.cursor++
			}
		case " ":
			if len(m.items) > 0 {
				m.items[m.cursor].Selected = !m.items[m.cursor].Selected
			}
		case "enter":
			m.confirmed, m.quit = true, true
			return m, tea.Quit
		case "esc", "q":
			m.quit = true
			return m, tea.Quit
		case "ctrl+c":
			m.err, m.quit = ErrPickerCancelled, true
			return m, tea.Quit
		}
	case CancelMsg:
		err := message.Err
		if err == nil {
			err = context.Canceled
		}
		m.err, m.quit = err, true
		return m, tea.Quit
	case EOFMsg, tea.QuitMsg:
		m.err, m.quit = io.EOF, true
		return m, tea.Quit
	}
	return m, nil
}

func (m PrunePrompt) View() string {
	styles := pickerStyles()
	var b strings.Builder
	b.WriteString(styles.title.Render("gg prune"))
	b.WriteString("\n")
	b.WriteString(styles.subtitle.Render("Select finished projects to remove — their worktrees and branches are deleted, including uncommitted leftovers"))
	b.WriteString("\n\n")
	if len(m.items) == 0 {
		b.WriteString(styles.empty.Render("No finished or terminated projects to prune."))
		b.WriteString("\n")
	}
	for i, item := range m.items {
		marker := "[ ] "
		if item.Selected {
			marker = "[x] "
		}
		name := item.Name
		if name == "" {
			name = item.Slug
		}
		b.WriteString(renderPickerRow(m.width, i == m.cursor, marker+name, item.Status, styles))
	}
	b.WriteString("\n")
	b.WriteString(styles.footer.Render(wrapToWidth("↑/↓ or j/k navigate  ·  Space toggle  ·  Enter prune selected  ·  Esc cancel", m.width-2)))
	return b.String() + "\n"
}

func (m PrunePrompt) Selected() []string {
	slugs := make([]string, 0, len(m.items))
	for _, item := range m.items {
		if item.Selected {
			slugs = append(slugs, item.Slug)
		}
	}
	return slugs
}

// RunPrunePrompt shows the prune selection screen and returns the selected
// slugs; a nil slice with nil error means the user cancelled.
func RunPrunePrompt(ctx context.Context, items []PruneItem, input io.Reader, output io.Writer) ([]string, error) {
	if len(items) == 0 {
		return nil, nil
	}
	final, err := runAltScreenProgram(ctx, NewPrunePrompt(items), input, output)
	if err != nil {
		return nil, err
	}
	prompt, ok := final.(PrunePrompt)
	if !ok {
		return nil, errors.New("run prune prompt: unexpected model")
	}
	if prompt.err != nil {
		return nil, prompt.err
	}
	if !prompt.confirmed {
		return nil, nil
	}
	return prompt.Selected(), nil
}
