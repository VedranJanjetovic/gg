package tui

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
)

type busyDoneMsg struct{ err error }

// BusyScreen renders a full-screen spinner and message while background work
// runs, in the same visual style as every other gg screen. Esc or Ctrl+C
// cancels the work's context; the screen still waits for the work to return
// so no background process outlives the session.
type BusyScreen struct {
	title     string
	message   string
	spinner   spinner.Model
	work      tea.Cmd
	cancel    context.CancelFunc
	cancelled bool
	err       error
	width     int
}

func NewBusyScreen(title, message string, cancel context.CancelFunc, work tea.Cmd) BusyScreen {
	return BusyScreen{title: title, message: message, spinner: spinner.New(), cancel: cancel, work: work, width: 80}
}

func (m BusyScreen) Init() tea.Cmd { return tea.Batch(m.spinner.Tick, m.work) }

func (m BusyScreen) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch message := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = message.Width
		return m, nil
	case busyDoneMsg:
		m.err = message.err
		return m, tea.Quit
	case tea.KeyMsg:
		switch message.Type {
		case tea.KeyEsc, tea.KeyCtrlC:
			m.cancelled = true
			if m.cancel != nil {
				m.cancel()
			}
		}
		return m, nil
	case CancelMsg, EOFMsg, tea.QuitMsg:
		m.cancelled = true
		if m.cancel != nil {
			m.cancel()
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.spinner, cmd = m.spinner.Update(msg)
	return m, cmd
}

func (m BusyScreen) View() string {
	styles := pickerStyles()
	var b strings.Builder
	b.WriteString(styles.title.Render(m.title))
	b.WriteString("\n\n")
	message := m.message
	if m.cancelled {
		message = "Cancelling…"
	}
	b.WriteString(m.spinner.View())
	b.WriteString(" ")
	b.WriteString(styles.context.Render(wrapToWidth(message, m.width-6)))
	b.WriteString("\n\n")
	b.WriteString(styles.footer.Render("Esc cancel"))
	return b.String() + "\n"
}

func (m BusyScreen) Err() error      { return m.err }
func (m BusyScreen) Cancelled() bool { return m.cancelled }

// RunBusy shows a full-screen wait indicator while work runs. It returns the
// work's error, or ErrPickerCancelled when the user cancelled; cancellation
// propagates to the work through its context before RunBusy returns.
func RunBusy(ctx context.Context, title, message string, input io.Reader, output io.Writer, work func(context.Context) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	model := NewBusyScreen(title, message, cancel, func() tea.Msg { return busyDoneMsg{err: work(workCtx)} })
	final, err := runAltScreenProgram(ctx, model, input, output)
	if err != nil {
		return err
	}
	screen, ok := final.(BusyScreen)
	if !ok {
		return errors.New("run busy screen: unexpected model")
	}
	if screen.Cancelled() {
		return ErrPickerCancelled
	}
	return screen.Err()
}
