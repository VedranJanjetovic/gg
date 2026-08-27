package tui

import (
	"context"
	"fmt"
	"time"

	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/state"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
)

type pollTickMsg struct{}
type projectLoadedMsg struct {
	project state.ProjectState
	err     error
}

type actionKind uint8

const (
	actionStart actionKind = iota
	actionStop
	actionResume
	actionSkip
	actionCode
	actionTerminal
)

type actionResultMsg struct {
	kind actionKind
	err  error
}

func (m Model) Init() tea.Cmd {
	commands := []tea.Cmd{m.spinner.Tick}
	if m.loader != nil {
		commands = append(commands, m.pollCmd())
	}
	if m.startPending {
		commands = append(commands, actionCmd(m.ctx, actionStart, m.actions.Start))
	}
	return tea.Batch(commands...)
}

func (m Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width = message.Width
		return m, nil
	case tea.KeyMsg:
		// A key press acknowledges a failed action: until one arrives the
		// failure stays on screen, so the poll loop can never wipe the only
		// explanation the user has.
		m.actionErr = nil
		if m.skipConfirm {
			switch message.String() {
			case "y", "enter":
				m.skipConfirm = false
				m.skipPending = true
				m.notice = "Skipping " + m.skipLabel + "…"
				return m, actionCmd(m.ctx, actionSkip, m.actions.Skip)
			case "n", "esc":
				m.skipConfirm = false
				m.notice = "Skip cancelled."
			}
			return m, nil
		}
		switch message.String() {
		case "b", "q", "ctrl+c":
			// Quitting always detaches. Production pipelines execute in a
			// detached background gg process, so the run survives this
			// process exiting entirely; re-attach from the project list to
			// watch it again. If the background process itself dies,
			// stale-run recovery marks the project stopped for resume.
			return m, tea.Quit
		case "g":
			if m.interviewOpen() && !m.actionPending() {
				m.groomingRequested = true
				return m, tea.Quit
			}
			m.notice = keyNotice("grooming", m.interviewOpen(), m.actionPending(), "another action is still running")
		case "d":
			m.showTokenDetail = !m.showTokenDetail
			return m, nil
		case "i":
			// The interactive session (QA chat or feedback loop) needs the
			// terminal exclusively but works whether or not the pipeline is
			// running: runs execute in a detached process, and the feedback
			// loop asks before stopping one.
			if !m.actionPending() {
				m.interactiveRequested = true
				return m, tea.Quit
			}
			m.notice = keyNotice("interactive", true, true, "another action is still running")
		case "s":
			if m.project.Status == state.StatusRunning && m.actions.Stop != nil && !m.stopPending {
				m.notice = "Stopping…"
				m.stopPending = true
				return m, actionCmd(m.ctx, actionStop, m.actions.Stop)
			}
			available, label := m.skipTarget()
			if m.project.Status == state.StatusFailed && m.actions.Skip != nil && available && label != "" && skipOccurrenceID(m.project) != "" && !m.skipPending {
				m.skipLabel = label
				m.skipOccurrenceID = skipOccurrenceID(m.project)
				m.skipConfirm = true
				m.notice = "Confirm skip of " + m.skipLabel + "?"
				return m, nil
			}
			if m.project.Status == state.StatusFailed {
				m.notice = keyNotice("skip", m.actions.Skip != nil && m.actions.SkipAvailable, true, "the failed execution is not eligible")
			} else {
				m.notice = keyNotice("stop", m.actions.Stop != nil, m.project.Status != state.StatusRunning, "the project is not running")
			}
		case "r":
			resumable := m.project.Status == state.StatusStopped || m.project.Status == state.StatusFailed
			if resumable && !m.actionPending() && m.actions.Resume != nil {
				m.notice = "Resuming…"
				m.resumePending = true
				return m, actionCmd(m.ctx, actionResume, m.actions.Resume)
			}
			m.notice = keyNotice("resume", m.actions.Resume != nil, !resumable, "the project is not stopped or failed")
		case "e":
			editable := m.project.Status == state.StatusFailed || m.project.Status == state.StatusStopped
			if editable && !m.actionPending() && m.actions.Configure != nil {
				m.configureRequested = true
				return m, tea.Quit
			}
			m.notice = keyNotice("configure", m.actions.Configure != nil, !editable, "the project is not stopped or failed")
		case "c":
			// Launches only conflict with other launches: a running
			// pipeline (start/resume pending) must not block them.
			if !m.launchPending() && m.actions.OpenCode != nil {
				m.notice = "Opening Visual Studio Code…"
				m.codePending = true
				return m, actionCmd(m.ctx, actionCode, m.actions.OpenCode)
			}
			m.notice = keyNotice("code", m.actions.OpenCode != nil, m.launchPending(), "another launch is still running")
		case "t":
			if !m.launchPending() && m.actions.OpenTerminal != nil {
				m.notice = "Opening terminal…"
				m.terminalPending = true
				return m, actionCmd(m.ctx, actionTerminal, m.actions.OpenTerminal)
			}
			m.notice = keyNotice("terminal", m.actions.OpenTerminal != nil, m.launchPending(), "another launch is still running")
		}
	case spinner.TickMsg:
		var command tea.Cmd
		m.spinner, command = m.spinner.Update(message)
		return m, command
	case pollTickMsg:
		if m.loader != nil {
			return m, loadProjectCmd(m.ctx, m.loader)
		}
	case projectLoadedMsg:
		if message.err != nil {
			m.lastErr = fmt.Errorf("refresh project: %w", message.err)
		} else {
			definitions, err := projectPipeline(message.project, pendingForDefinitions(m.definitions))
			if err != nil {
				m.lastErr = err
			} else {
				occurrenceID := skipOccurrenceID(message.project)
				m.project = message.project
				m.definitions = definitions
				m.phases = projectPhases(message.project, definitions)
				if m.skipConfirm && occurrenceID != m.skipOccurrenceID {
					m.skipConfirm = false
					m.skipLabel = ""
					m.skipOccurrenceID = ""
					m.notice = "Skip cancelled because the failed execution changed."
				}
				if m.skipResolved && occurrenceID != m.skipOccurrenceID {
					m.skipResolved = false
					m.skipOccurrenceID = occurrenceID
				}
				m.lastErr = nil
			}
		}
		if m.loader != nil {
			return m, m.pollCmd()
		}
	case actionResultMsg:
		switch message.kind {
		case actionStart:
			m.startPending = false
		case actionStop:
			m.stopPending = false
		case actionResume:
			m.resumePending = false
		case actionSkip:
			m.skipPending = false
			if message.err == nil {
				m.skipResolved = true
			}
		case actionCode:
			m.codePending = false
		case actionTerminal:
			m.terminalPending = false
		}
		if message.err != nil {
			m.actionErr = message.err
			m.notice = ""
		} else {
			m.actionErr = nil
			m.notice = actionDoneNotice(message.kind)
		}
	}
	return m, nil
}

// keyNotice explains why a key press had no effect so the TUI never appears
// to swallow input silently.
func keyNotice(action string, available, wrongState bool, reason string) string {
	if !available {
		return action + " is not available in this session"
	}
	if wrongState {
		return action + ": " + reason
	}
	return ""
}

func actionDoneNotice(kind actionKind) string {
	switch kind {
	case actionStop:
		return "Stop requested."
	case actionResume:
		return "Resume completed."
	case actionSkip:
		return "Skip confirmed; continuation started."
	case actionCode:
		return "Opened Visual Studio Code."
	case actionTerminal:
		return "Opened terminal."
	default:
		return ""
	}
}

func (m Model) foregroundOwned() bool {
	return m.startPending || m.resumePending || m.skipPending || m.codePending || m.terminalPending
}

func (m Model) actionPending() bool { return m.foregroundOwned() || m.stopPending }

func (m Model) skipTarget() (bool, string) {
	if m.skipResolved {
		return false, ""
	}
	if m.actions.SkipTarget != nil {
		return m.actions.SkipTarget(m.project)
	}
	return m.actions.SkipAvailable, m.actions.SkipLabel
}

func skipOccurrenceID(project state.ProjectState) string {
	if len(project.PhaseHistory) == 0 {
		return ""
	}
	return project.PhaseHistory[len(project.PhaseHistory)-1].OccurrenceID
}

// launchPending reports whether an external tool launch (editor, terminal) is
// still starting. Lifecycle actions are deliberately excluded: a pipeline run
// blocking the foreground must not prevent opening tools on the worktree.
func (m Model) launchPending() bool { return m.codePending || m.terminalPending }

func (m Model) pollCmd() tea.Cmd {
	return m.poll(m.pollInterval, func(time.Time) tea.Msg { return pollTickMsg{} })
}

func loadProjectCmd(ctx context.Context, loader Loader) tea.Cmd {
	return func() tea.Msg {
		project, err := loader(ctx)
		return projectLoadedMsg{project: project, err: err}
	}
}

func actionCmd(ctx context.Context, kind actionKind, action func(context.Context) error) tea.Cmd {
	return func() tea.Msg { return actionResultMsg{kind: kind, err: action(ctx)} }
}

func pendingForDefinitions(definitions []phaseDefinition) *PendingPipeline {
	pending := PendingPipeline{DevelopmentSubphases: pipelineGeneration(definitions)}
	for _, definition := range definitions {
		pending.Phases = append(pending.Phases, definition.id)
	}
	return &pending
}

func pipelineGeneration(definitions []phaseDefinition) pipeline.DevelopmentSubphaseGeneration {
	for _, definition := range definitions {
		if definition.id != pipeline.PhaseDevelopment {
			continue
		}
		generation := pipeline.DevelopmentSubphaseGeneration{Mode: pipeline.DevelopmentSubphasesOverride}
		for _, subphase := range definition.subphases {
			generation.Subphases = append(generation.Subphases, pipeline.DevelopmentSubphaseDefinition{ID: pipeline.DevelopmentSubphaseID(subphase.id), DisplayName: subphase.name})
		}
		return generation
	}
	return pipeline.DevelopmentSubphaseGeneration{}
}
