package tui

import (
	"context"
	"errors"
	"io"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

const (
	ProjectDescribeScreen PickerScreen = "project-describe"
	ProjectConfirmScreen  PickerScreen = "project-confirm"
)

// ProjectPrompt is the full-screen new-project screen: a multi-line
// description editor followed by an explicit confirmation that previews the
// project before anything is created.
type ProjectPrompt struct {
	inferName func(string) string
	screen    PickerScreen
	lines     []string
	current   []rune
	result    string
	err       error
	quit      bool
	width     int
	height    int
}

// NewProjectPrompt constructs the new-project screen. inferName previews the
// project name that will be derived from the description; nil disables the
// preview line.
func NewProjectPrompt(inferName func(string) string) ProjectPrompt {
	if inferName == nil {
		inferName = func(string) string { return "" }
	}
	return ProjectPrompt{inferName: inferName, screen: ProjectDescribeScreen, width: 80, height: 24}
}

func (m ProjectPrompt) Init() tea.Cmd { return nil }

func (m ProjectPrompt) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch message := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = message.Width, message.Height
		return m, nil
	case tea.KeyMsg:
		if m.screen == ProjectConfirmScreen {
			m.handleConfirmKey(message)
		} else {
			m.handleDescribeKey(message)
		}
	case CancelMsg:
		err := message.Err
		if err == nil {
			err = context.Canceled
		}
		m.cancel(err)
	case EOFMsg, tea.QuitMsg:
		m.cancel(io.EOF)
	}
	if m.quit {
		return m, tea.Quit
	}
	return m, nil
}

func (m *ProjectPrompt) handleDescribeKey(message tea.KeyMsg) {
	switch message.Type {
	case tea.KeyEnter:
		if len(m.current) == 0 {
			// An empty line finishes the description once there is content.
			if m.description() != "" {
				m.screen = ProjectConfirmScreen
			}
			return
		}
		m.lines = append(m.lines, string(m.current))
		m.current = nil
	case tea.KeyBackspace:
		if len(m.current) > 0 {
			m.current = m.current[:len(m.current)-1]
			return
		}
		if len(m.lines) > 0 {
			m.current = []rune(m.lines[len(m.lines)-1])
			m.lines = m.lines[:len(m.lines)-1]
		}
	case tea.KeyEsc, tea.KeyCtrlC:
		m.cancel(ErrPickerCancelled)
	case tea.KeySpace:
		m.current = append(m.current, ' ')
	case tea.KeyRunes:
		m.current = append(m.current, message.Runes...)
	}
}

func (m *ProjectPrompt) handleConfirmKey(message tea.KeyMsg) {
	switch message.Type {
	case tea.KeyEnter:
		m.result = m.description()
		m.quit = true
	case tea.KeyEsc:
		m.screen = ProjectDescribeScreen
	case tea.KeyCtrlC:
		m.cancel(ErrPickerCancelled)
	}
}

func (m ProjectPrompt) description() string {
	parts := append(append([]string(nil), m.lines...), string(m.current))
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func (m ProjectPrompt) View() string {
	styles := pickerStyles()
	var b strings.Builder
	b.WriteString(styles.title.Render("gg new project"))
	b.WriteString("\n")
	if m.err != nil {
		b.WriteString(styles.error.Render(m.message()))
		b.WriteString("\n\n")
		b.WriteString(styles.footer.Render("Esc cancel  ·  Enter acknowledge"))
		return b.String() + "\n"
	}
	if m.screen == ProjectConfirmScreen {
		b.WriteString(styles.subtitle.Render("Create this project?"))
		b.WriteString("\n\n")
		if name := m.inferName(m.description()); name != "" {
			b.WriteString(styles.context.Render(wrapToWidth("Project name: "+name, m.width-2)))
			b.WriteString("\n\n")
		}
		for _, line := range strings.Split(m.description(), "\n") {
			b.WriteString(styles.row.Render(wrapToWidth(line, m.width-4)))
			b.WriteString("\n")
		}
		b.WriteString("\n")
		b.WriteString(styles.footer.Render("Enter create project  ·  Esc keep editing"))
		return b.String() + "\n"
	}
	b.WriteString(styles.subtitle.Render("Describe the project"))
	b.WriteString("\n")
	b.WriteString(styles.context.Render("Enter starts a new line; Enter on an empty line finishes."))
	b.WriteString("\n\n")
	for _, line := range m.lines {
		b.WriteString(styles.row.Render(wrapToWidth(line, m.width-4)))
		b.WriteString("\n")
	}
	b.WriteString(styles.row.Render(wrapToWidth(string(m.current)+"▏", m.width-4)))
	b.WriteString("\n\n")
	b.WriteString(styles.footer.Render(wrapToWidth("Type the description  ·  Enter twice to finish  ·  Esc cancel", m.width-2)))
	return b.String() + "\n"
}

func (m ProjectPrompt) message() string {
	switch {
	case errors.Is(m.err, io.EOF):
		return "Project creation cancelled: input ended before completion."
	case errors.Is(m.err, ErrPickerCancelled):
		return "Project creation cancelled."
	default:
		return m.err.Error()
	}
}

func (m *ProjectPrompt) cancel(err error) { m.err, m.quit = err, true }

func (m ProjectPrompt) Screen() PickerScreen { return m.screen }
func (m ProjectPrompt) Result() string       { return m.result }
func (m ProjectPrompt) Err() error           { return m.err }

// RunProjectPrompt drives the full-screen new-project description screen and
// returns the confirmed description. It reports ErrPickerNonInteractive when
// the terminal cannot host the screen so callers can fall back to line
// prompts.
func RunProjectPrompt(ctx context.Context, inferName func(string) string, input io.Reader, output io.Writer) (string, error) {
	final, err := runAltScreenProgram(ctx, NewProjectPrompt(inferName), input, output)
	if err != nil {
		return "", err
	}
	prompt, ok := final.(ProjectPrompt)
	if !ok {
		return "", errors.New("run project prompt: unexpected model")
	}
	if prompt.Err() != nil {
		return "", prompt.Err()
	}
	return prompt.Result(), nil
}
