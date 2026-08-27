package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/VedranJanjetovic/gg/internal/state"
	tea "github.com/charmbracelet/bubbletea"
)

type globalTickMsg struct{}
type globalRefreshMsg struct {
	snapshot GlobalSnapshot
	err      error
}

type GlobalModel struct {
	ctx             context.Context
	controller      *GlobalController
	snapshot        GlobalSnapshot
	refreshInterval time.Duration
	lastErr         error
	cursor          int
	width           int
	attach          func(context.Context, state.ProjectState) error
	// selected is the project chosen for attachment. Selecting quits the
	// global program so the project session owns the terminal exclusively;
	// RunGlobal re-enters the global view when the session ends.
	selected *state.ProjectState
}

type GlobalOption func(*GlobalModel)

func WithGlobalRefreshInterval(interval time.Duration) GlobalOption {
	return func(m *GlobalModel) { m.refreshInterval = interval }
}

// WithGlobalProjectAttacher supplies the foreground project session opened by
// a numeric project selection. Returning from the attacher returns to the
// same global model and preserves its last good snapshot.
func WithGlobalProjectAttacher(attacher func(context.Context, state.ProjectState) error) GlobalOption {
	return func(m *GlobalModel) { m.attach = attacher }
}

func NewGlobalModel(ctx context.Context, controller *GlobalController, options ...GlobalOption) (GlobalModel, error) {
	if controller == nil {
		return GlobalModel{}, errors.New("global model requires controller")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m := GlobalModel{ctx: ctx, controller: controller, refreshInterval: DefaultGlobalRefreshInterval}
	for _, option := range options {
		option(&m)
	}
	if m.refreshInterval <= 0 {
		return GlobalModel{}, errors.New("global refresh interval must be positive")
	}
	return m, nil
}
func (m GlobalModel) Snapshot() GlobalSnapshot { return m.snapshot }
func (m GlobalModel) Init() tea.Cmd            { return refreshGlobalCmd(m.ctx, m.controller) }
func (m GlobalModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width = message.Width
		return m, nil
	case tea.KeyMsg:
		switch message.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
			return m, nil
		case "down", "j":
			if m.cursor < m.projectCount()-1 {
				m.cursor++
			}
			return m, nil
		case "enter":
			return m.attachAt(m.cursor)
		}
		if index, ok := numericProjectIndex(message.String()); ok {
			return m.attachAt(index)
		}
	case globalRefreshMsg:
		if message.err != nil {
			m.lastErr = message.err
		} else {
			m.snapshot, m.lastErr = message.snapshot, nil
			if count := m.projectCount(); m.cursor >= count && count > 0 {
				m.cursor = count - 1
			}
		}
		return m, tea.Tick(m.refreshInterval, func(time.Time) tea.Msg { return globalTickMsg{} })
	case globalTickMsg:
		return m, refreshGlobalCmd(m.ctx, m.controller)
	}
	return m, nil
}

// attachAt records the selection and quits the global program. The project
// session must not run nested inside this program: two Bubble Tea programs
// would compete for the same stdin.
func (m GlobalModel) attachAt(index int) (tea.Model, tea.Cmd) {
	project, ok := m.snapshot.ProjectAt(index)
	if !ok || m.attach == nil {
		return m, nil
	}
	m.selected = &project
	return m, tea.Quit
}

// Selected returns the project chosen for attachment, if any.
func (m GlobalModel) Selected() *state.ProjectState { return m.selected }

func (m GlobalModel) projectCount() int {
	count := 0
	for _, folder := range m.snapshot.Folders {
		count += len(folder.Projects)
	}
	return count
}
func refreshGlobalCmd(ctx context.Context, controller *GlobalController) tea.Cmd {
	return func() tea.Msg {
		snapshot, err := controller.Refresh(ctx)
		return globalRefreshMsg{snapshot: snapshot, err: err}
	}
}

// RunGlobal opens the global observation view. Non-TTY callers get one stable
// snapshot. Selecting a project quits the view, runs the project session in
// the foreground (owning the terminal exclusively), and returns to the view
// when the session ends.
func RunGlobal(ctx context.Context, controller *GlobalController, input io.Reader, output io.Writer, options ...GlobalOption) error {
	if input == nil {
		input = os.Stdin
	}
	if output == nil {
		output = os.Stdout
	}
	model, err := NewGlobalModel(ctx, controller, options...)
	if err != nil {
		return err
	}
	if !interactiveTerminal(input, output) {
		snapshot, err := controller.Refresh(ctx)
		if err != nil {
			return fmt.Errorf("refresh global status: %w", err)
		}
		model.snapshot = snapshot
		_, err = io.WriteString(output, model.View())
		return err
	}
	var sessionErr error
	for {
		model.selected = nil
		model.lastErr = sessionErr
		final, err := tea.NewProgram(model, tea.WithContext(ctx), tea.WithInput(input), tea.WithOutput(output), tea.WithAltScreen()).Run()
		if err != nil {
			return fmt.Errorf("run global TUI: %w", err)
		}
		finalModel, ok := final.(GlobalModel)
		if !ok {
			return errors.New("run global TUI: unexpected model")
		}
		selected := finalModel.Selected()
		if selected == nil || finalModel.attach == nil {
			if finalModel.lastErr != nil {
				return finalModel.lastErr
			}
			return nil
		}
		// Keep the refreshed snapshot and cursor for the next round.
		model = finalModel
		sessionErr = finalModel.attach(ctx, *selected)
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

func (m GlobalModel) View() string {
	styles := pickerStyles()
	width := m.width
	if width <= 0 {
		width = 80
	}
	var b strings.Builder
	b.WriteString(styles.title.Render("gg · projects"))
	b.WriteString("\n\n")
	if len(m.snapshot.Folders) == 0 {
		b.WriteString(styles.empty.Render("No configured folders."))
		b.WriteString("\n")
	}
	for _, folder := range m.snapshot.Folders {
		b.WriteString(styles.context.Render(folder.Folder))
		b.WriteString("\n")
		if len(folder.Projects) == 0 {
			b.WriteString(styles.footer.Render("  (empty)"))
			b.WriteString("\n")
			continue
		}
		for _, observation := range folder.Projects {
			index := m.snapshot.projectIndex(observation.Project.Slug)
			label := fmt.Sprintf("%d) %s%s  %s", index, statusMarker(observation.Project.Status), verificationMarker(observation.Project), observation.Project.Name)
			if phase := strings.TrimSpace(observation.Project.CurrentPhase); phase != "" && phase != "pipeline" {
				label += "  ·  " + phase
			}
			b.WriteString(renderPickerRow(width, index-1 == m.cursor, label, "", styles))
		}
	}
	if m.lastErr != nil {
		b.WriteString("\n")
		b.WriteString(styles.error.Render(wrapToWidth("Error: "+m.lastErr.Error(), width-2)))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(styles.footer.Render("↑/↓ or j/k move  Enter attach  [number] attach  q quit"))
	b.WriteString("\n")
	return b.String()
}

func numericProjectIndex(value string) (int, bool) {
	if len(value) != 1 || value[0] < '1' || value[0] > '9' {
		return 0, false
	}
	return int(value[0] - '1'), true
}

func statusMarker(status state.LifecycleStatus) string {
	switch status {
	case state.StatusRunning:
		return "running"
	case state.StatusStopped:
		return "stopped"
	case state.StatusFailed, state.StatusTerminated:
		return "failed"
	case state.StatusFinished:
		return "finished"
	default:
		return "empty"
	}
}

func verificationMarker(project state.ProjectState) string {
	if state.VerificationIsPaused(project) {
		return " [paused]"
	}
	if state.VerificationHasWarnings(project) {
		return " [warning]"
	}
	return ""
}
