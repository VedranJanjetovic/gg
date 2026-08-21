package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// QuestionPrompt asks grooming questions one at a time in the shared
// full-screen style. Every answer is a free-form multi-line text; an empty
// answer skips the question and Esc finishes the interview early, keeping the
// answers collected so far.
type QuestionPrompt struct {
	questions []string
	answers   []string
	index     int
	lines     []string
	current   []rune
	err       error
	quit      bool
	width     int
	height    int
}

func NewQuestionPrompt(questions []string) QuestionPrompt {
	return QuestionPrompt{
		questions: append([]string(nil), questions...),
		answers:   make([]string, len(questions)),
		width:     80,
		height:    24,
	}
}

func (m QuestionPrompt) Init() tea.Cmd { return nil }

func (m QuestionPrompt) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch message := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = message.Width, message.Height
		return m, nil
	case tea.KeyMsg:
		switch message.Type {
		case tea.KeyEnter:
			if len(m.current) == 0 {
				// Empty line submits the answer (possibly empty = skipped).
				m.answers[m.index] = strings.TrimSpace(strings.Join(m.lines, "\n"))
				m.lines, m.current = nil, nil
				m.index++
				if m.index >= len(m.questions) {
					m.quit = true
				}
				return m.done()
			}
			m.lines = append(m.lines, string(m.current))
			m.current = nil
		case tea.KeyBackspace:
			if len(m.current) > 0 {
				m.current = m.current[:len(m.current)-1]
			} else if len(m.lines) > 0 {
				m.current = []rune(m.lines[len(m.lines)-1])
				m.lines = m.lines[:len(m.lines)-1]
			}
		case tea.KeyEsc:
			// Pause: answers so far are kept, the rest stay pending.
			m.quit = true
		case tea.KeyCtrlC:
			m.err, m.quit = ErrPickerCancelled, true
		case tea.KeySpace:
			m.current = append(m.current, ' ')
		case tea.KeyRunes:
			m.current = append(m.current, message.Runes...)
		}
	case CancelMsg:
		err := message.Err
		if err == nil {
			err = context.Canceled
		}
		m.err, m.quit = err, true
	case EOFMsg, tea.QuitMsg:
		m.err, m.quit = io.EOF, true
	}
	return m.done()
}

func (m QuestionPrompt) done() (tea.Model, tea.Cmd) {
	if m.quit {
		return m, tea.Quit
	}
	return m, nil
}

func (m QuestionPrompt) View() string {
	styles := pickerStyles()
	var b strings.Builder
	b.WriteString(styles.title.Render("gg grooming"))
	b.WriteString("\n")
	if m.err != nil {
		b.WriteString(styles.error.Render("Grooming interview cancelled."))
		return b.String() + "\n"
	}
	if m.index >= len(m.questions) {
		return b.String()
	}
	b.WriteString(styles.subtitle.Render(fmt.Sprintf("Question %d of %d", m.index+1, len(m.questions))))
	b.WriteString("\n\n")
	b.WriteString(styles.context.Render(wrapToWidth(m.questions[m.index], m.width-2)))
	b.WriteString("\n\n")
	for _, line := range m.lines {
		b.WriteString(styles.row.Render(wrapToWidth(line, m.width-4)))
		b.WriteString("\n")
	}
	b.WriteString(styles.row.Render(wrapToWidth(string(m.current)+"▏", m.width-4)))
	b.WriteString("\n\n")
	b.WriteString(styles.footer.Render(wrapToWidth("Type your answer  ·  Enter twice to submit  ·  empty answer skips this question  ·  Esc pause (answers so far are kept; press g on the project screen to resume)", m.width-2)))
	return b.String() + "\n"
}

func (m QuestionPrompt) Answers() []string { return append([]string(nil), m.answers...) }
func (m QuestionPrompt) Index() int        { return m.index }
func (m QuestionPrompt) Err() error        { return m.err }

// RunQuestionPrompt asks each question in a full-screen session. It returns
// the answers aligned with the questions (empty string = deliberately
// skipped) and how many questions the user progressed through: Esc pauses the
// interview early, so progressed < len(questions) means the remaining
// questions were never seen and must be asked again later.
func RunQuestionPrompt(ctx context.Context, questions []string, input io.Reader, output io.Writer) ([]string, int, error) {
	if len(questions) == 0 {
		return nil, 0, nil
	}
	final, err := runAltScreenProgram(ctx, NewQuestionPrompt(questions), input, output)
	if err != nil {
		return nil, 0, err
	}
	prompt, ok := final.(QuestionPrompt)
	if !ok {
		return nil, 0, errors.New("run question prompt: unexpected model")
	}
	if prompt.Err() != nil {
		return nil, 0, prompt.Err()
	}
	return prompt.Answers(), prompt.Index(), nil
}
