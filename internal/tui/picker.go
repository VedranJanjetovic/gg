package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/VedranJanjetovic/gg/internal/config"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/cancelreader"
)

type PickerScreen string

const (
	AgentPickerScreen  PickerScreen = "agents"
	ModelPickerScreen  PickerScreen = "models"
	ModelInputScreen   PickerScreen = "model-input"
	EffortPickerScreen PickerScreen = "effort"
	PhaseToggleScreen  PickerScreen = "phases"
)

// manualModelOption is the always-present final model row that switches to
// free-form model-name entry for models not in the static catalog.
const manualModelOption = "Enter model name manually…"

var (
	ErrPickerCancelled      = errors.New("configuration selection cancelled")
	ErrPickerNonInteractive = errors.New("configuration selection requires an interactive terminal")
	ErrNoAgents             = errors.New("no supported agents are available")
)

var wizardEfforts = [...]config.Effort{config.EffortLow, config.EffortMedium, config.EffortHigh}

var effortDescriptions = map[config.Effort]string{
	config.EffortLow:    "Fastest responses, lightest reasoning",
	config.EffortMedium: "Balanced speed and reasoning",
	config.EffortHigh:   "Most thorough reasoning",
}

type CancelMsg struct{ Err error }
type EOFMsg struct{}

// PhaseState is one pipeline phase shown on the phase screen. Locked phases
// cannot be toggled; rows without a config.Phase key are fixed pipeline
// context the cursor skips over. Agent/Model/Effort are per-phase override
// pins: an empty field inherits the wizard's global selection, so changing
// the global defaults immediately applies to every unpinned phase.
type PhaseState struct {
	Phase       config.Phase
	Name        string
	Enabled     bool
	Locked      bool
	Description string
	Agent       config.Agent
	Model       string
	Effort      config.Effort
	Manual      bool
}

func (s PhaseState) displayName() string {
	if s.Name != "" {
		return s.Name
	}
	return string(s.Phase)
}

// configurable reports whether the row maps to a configuration key and can
// therefore carry per-phase agent/model/effort overrides.
func (s PhaseState) configurable() bool { return s.Phase != "" }

// pinned reports whether the phase overrides any global setting.
func (s PhaseState) pinned() bool { return s.Agent != "" || s.Model != "" || s.Effort != "" }

// phaseSettingsLine renders the phase's effective settings: pinned fields as
// stored, everything else inherited from the wizard's current global picks.
func (m ConfigureWizard) phaseSettingsLine(phase PhaseState) string {
	if !phase.configurable() {
		return ""
	}
	agent, model, effort := phase.Agent, phase.Model, phase.Effort
	if agent == "" {
		agent = m.result.Agent
	}
	if model == "" {
		model = m.result.Model
	}
	if effort == "" {
		effort = m.result.Effort
	}
	parts := make([]string, 0, 3)
	if agent != "" {
		parts = append(parts, string(agent))
	}
	if model != "" {
		parts = append(parts, model)
	}
	if effort != "" {
		parts = append(parts, string(effort))
	}
	line := strings.Join(parts, " · ")
	if phase.pinned() {
		line += "  (custom)"
	}
	return line
}

// WizardDefaults prefills the configure wizard with the currently effective
// values so pressing Enter through every screen keeps the configuration.
// An empty Phases slice skips the phase-toggle screen entirely.
type WizardDefaults struct {
	Agent      config.Agent
	Model      string
	Effort     config.Effort
	FullTuples bool
	Manual     bool
	Phases     []PhaseState
}

type PickerResult struct {
	Agent config.Agent
	Model string
	// Manual reports that Model was typed by the user instead of chosen from
	// the catalog list, so callers must not validate it against the catalog.
	Manual bool
	Effort config.Effort
	Phases []PhaseState
}

type ConfigureWizard struct {
	catalog  config.AgentCatalog
	defaults WizardDefaults
	screen   PickerScreen
	agents   []config.AgentCatalogEntry
	models   []string
	manual   []rune
	phases   []PhaseState
	cursor   int
	selected config.Agent
	result   PickerResult
	// overridePhase indexes the phase row being configured by the per-phase
	// override sub-flow; -1 means the main agent/model/effort flow.
	overridePhase int
	err           error
	quit          bool
	width         int
	height        int
}

func NewConfigureWizard(catalog config.AgentCatalog, defaults WizardDefaults) ConfigureWizard {
	entries := catalog.Entries()
	wizard := ConfigureWizard{
		catalog:       config.NewAgentCatalog(entries...),
		defaults:      defaults,
		screen:        AgentPickerScreen,
		agents:        entries,
		phases:        clonePhaseStates(defaults.Phases),
		overridePhase: -1,
		width:         80,
		height:        24,
	}
	for i, entry := range entries {
		if entry.Agent == defaults.Agent {
			wizard.cursor = i
			break
		}
	}
	return wizard
}

func clonePhaseStates(states []PhaseState) []PhaseState {
	return append([]PhaseState(nil), states...)
}

func (m ConfigureWizard) Init() tea.Cmd { return nil }

func (m ConfigureWizard) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch message := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = message.Width, message.Height
		return m, nil
	case tea.KeyMsg:
		if m.screen == ModelInputScreen {
			m.handleInputKey(message)
		} else {
			switch message.Type {
			case tea.KeyUp:
				m.move(-1)
			case tea.KeyDown:
				m.move(1)
			case tea.KeyEnter:
				m.selectCurrent()
			case tea.KeySpace:
				m.togglePhase()
			case tea.KeyEsc:
				if m.overridePhase >= 0 {
					m.exitOverride()
				} else {
					m.cancel(ErrPickerCancelled)
				}
			case tea.KeyCtrlC:
				m.cancel(ErrPickerCancelled)
			case tea.KeyRunes:
				if len(message.Runes) != 1 {
					break
				}
				switch message.Runes[0] {
				case 'k':
					m.move(-1)
				case 'j':
					m.move(1)
				case 'o':
					m.startPhaseOverride()
				}
			}
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

func (m ConfigureWizard) View() string {
	styles := pickerStyles()
	var b strings.Builder
	b.WriteString(styles.title.Render("gg configure"))
	b.WriteString("\n")
	if m.err != nil {
		b.WriteString(styles.error.Render(wrapToWidth(m.message(), m.width-2)))
		b.WriteString("\n\n")
		b.WriteString(styles.footer.Render("Esc cancel  ·  Enter acknowledge"))
		return b.String() + "\n"
	}

	switch m.screen {
	case AgentPickerScreen:
		subtitle := "Choose an agent to configure"
		if m.overridePhase >= 0 {
			subtitle = "Choose the agent for phase " + m.phases[m.overridePhase].displayName()
		}
		b.WriteString(styles.subtitle.Render(subtitle))
		b.WriteString("\n\n")
		if len(m.agents) == 0 {
			b.WriteString(styles.empty.Render("No supported agents are available."))
		} else {
			for i, entry := range m.agents {
				name := entry.DisplayName
				if name == "" {
					name = string(entry.Agent)
				}
				meta := pickerAgentMetadata(entry)
				if meta != "" {
					name += "  ·  " + meta
				}
				b.WriteString(renderPickerRow(m.width, i == m.cursor, name, entry.Description, styles))
			}
		}
	case ModelInputScreen:
		b.WriteString(styles.subtitle.Render("Enter a model name for " + m.selectedDisplayName()))
		b.WriteString("\n\n")
		b.WriteString(styles.row.Render(wrapToWidth("Model: "+string(m.manual)+"▏", m.width-4)))
		b.WriteString("\n\n")
		b.WriteString(styles.footer.Render("Type a model name  ·  Enter confirm  ·  Esc back"))
		return b.String() + "\n"
	case EffortPickerScreen:
		subtitle := "Select default effort"
		if m.overridePhase >= 0 {
			subtitle = "Select effort for phase " + m.phases[m.overridePhase].displayName()
		}
		b.WriteString(styles.subtitle.Render(subtitle))
		b.WriteString("\n\n")
		for i, effort := range wizardEfforts {
			b.WriteString(renderPickerRow(m.width, i == m.cursor, string(effort), effortDescriptions[effort], styles))
		}
	case PhaseToggleScreen:
		b.WriteString(styles.subtitle.Render("Pipeline phases (in execution order)"))
		b.WriteString("\n\n")
		for i, phase := range m.phases {
			marker := "[ ] "
			switch {
			case phase.Locked && phase.Enabled:
				marker = " ✓  "
			case phase.Locked:
				marker = " ✗  "
			case phase.Enabled:
				marker = "[x] "
			}
			description := phase.Description
			if settings := m.phaseSettingsLine(phase); settings != "" {
				description += "  —  " + settings
			}
			b.WriteString(renderPickerRow(m.width, i == m.cursor, marker+phase.displayName(), description, styles))
		}
		b.WriteString("\n")
		b.WriteString(renderPickerRow(m.width, m.cursor == m.saveRowIndex(), "Save configuration", "Write the configuration and finish", styles))
		b.WriteString("\n")
		b.WriteString(styles.footer.Render(wrapToWidth("↑/↓ or j/k navigate  ·  Space toggle on/off  ·  Enter change agent/model/effort  ·  Enter on Save configuration finishes  ·  Esc cancel", m.width-2)))
		return b.String() + "\n"
	default:
		entry, _ := m.catalog.Lookup(m.selected)
		subtitle := "Select a model for " + m.selectedDisplayName()
		if m.overridePhase >= 0 {
			subtitle += " (phase " + m.phases[m.overridePhase].displayName() + ")"
		}
		b.WriteString(styles.subtitle.Render(subtitle))
		b.WriteString("\n")
		if meta := pickerAgentMetadata(entry); meta != "" {
			b.WriteString(styles.context.Render(meta))
			b.WriteString("\n")
		}
		b.WriteString("\n")
		if len(m.models) == 0 {
			if entry.ModelListStatus == config.ModelListUnavailable {
				b.WriteString(styles.empty.Render("Model list unavailable for this agent; enter one manually."))
			} else {
				b.WriteString(styles.empty.Render("No catalog models for this agent; enter one manually."))
			}
			b.WriteString("\n")
		}
		for i, model := range m.models {
			b.WriteString(renderPickerRow(m.width, i == m.cursor, model, entry.ModelDescriptions[model], styles))
		}
		b.WriteString(renderPickerRow(m.width, m.cursor == len(m.models), manualModelOption, "Use a model that is not in this list", styles))
	}
	b.WriteString("\n")
	b.WriteString(styles.footer.Render(wrapToWidth("↑/↓ or j/k navigate  ·  Enter select  ·  Esc cancel", m.width-2)))
	return b.String() + "\n"
}

func (m ConfigureWizard) selectedDisplayName() string {
	entry, _ := m.catalog.Lookup(m.selected)
	if entry.DisplayName != "" {
		return entry.DisplayName
	}
	return string(m.selected)
}

type pickerStylesSet struct {
	title, subtitle, context, selected, selectedDesc, row, rowDesc, footer, empty, error, update lipgloss.Style
}

func pickerStyles() pickerStylesSet {
	return pickerStylesSet{
		title:        lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6")),
		subtitle:     lipgloss.NewStyle().Foreground(lipgloss.Color("7")),
		context:      lipgloss.NewStyle().Foreground(lipgloss.Color("6")),
		selected:     lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(lipgloss.Color("6")).Padding(0, 1),
		selectedDesc: lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color("6")),
		row:          lipgloss.NewStyle().PaddingLeft(2),
		rowDesc:      lipgloss.NewStyle().Foreground(lipgloss.Color("8")).PaddingLeft(6),
		footer:       lipgloss.NewStyle().Foreground(lipgloss.Color("8")),
		empty:        lipgloss.NewStyle().Foreground(lipgloss.Color("3")),
		error:        lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("1")),
		update:       lipgloss.NewStyle().Foreground(lipgloss.Color("3")),
	}
}

func pickerAgentMetadata(entry config.AgentCatalogEntry) string {
	parts := make([]string, 0, 2)
	if entry.Provider != "" {
		parts = append(parts, "Provider: "+entry.Provider)
	}
	if entry.Harness != "" {
		parts = append(parts, "Harness: "+entry.Harness)
	}
	return strings.Join(parts, " · ")
}

func renderPickerRow(width int, selected bool, name, description string, styles pickerStylesSet) string {
	prefix := "  "
	nameStyle, descStyle := styles.row, styles.rowDesc
	if selected {
		prefix, nameStyle, descStyle = "▸ ", styles.selected, styles.selectedDesc
	}
	name = truncatePicker(name, width-4)
	description = wrapToWidth(description, width-8)
	var b strings.Builder
	b.WriteString(nameStyle.Render(prefix + name))
	if description != "" {
		b.WriteString("\n")
		b.WriteString(descStyle.Render(description))
	}
	return b.String() + "\n"
}

func truncatePicker(value string, width int) string {
	if width < 4 {
		width = 4
	}
	if len([]rune(value)) <= width {
		return value
	}
	return string([]rune(value)[:width-1]) + "…"
}

func (m ConfigureWizard) message() string {
	switch {
	case errors.Is(m.err, ErrNoAgents):
		return "No supported agents are available."
	case errors.Is(m.err, io.EOF):
		return "Configuration selection cancelled: input ended before completion."
	case errors.Is(m.err, ErrPickerCancelled):
		return "Configuration selection cancelled."
	default:
		return m.err.Error()
	}
}

func (m *ConfigureWizard) move(delta int) {
	length := len(m.agents)
	switch m.screen {
	case ModelPickerScreen:
		length = len(m.models) + 1 // catalog models plus the manual-entry row
	case EffortPickerScreen:
		length = len(wizardEfforts)
	case PhaseToggleScreen:
		m.movePhaseCursor(delta)
		return
	}
	if length == 0 {
		return
	}
	m.cursor = (m.cursor + delta + length) % length
}

// saveRowIndex is the phase-screen cursor position of the final
// "Save configuration" row that confirms the wizard.
func (m ConfigureWizard) saveRowIndex() int { return len(m.phases) }

// movePhaseCursor steps over fixed context rows so the cursor only ever rests
// on a row the user can toggle or override, or on the save row.
func (m *ConfigureWizard) movePhaseCursor(delta int) {
	length := len(m.phases) + 1 // phases plus the save row
	cursor := m.cursor
	for range length {
		cursor = (cursor + delta + length) % length
		if cursor == m.saveRowIndex() || m.phases[cursor].configurable() {
			m.cursor = cursor
			return
		}
	}
}

func (m ConfigureWizard) firstToggleablePhase() int {
	for i, phase := range m.phases {
		if phase.configurable() {
			return i
		}
	}
	return m.saveRowIndex()
}

// startPhaseOverride enters the per-phase agent/model/effort sub-flow for the
// phase under the cursor, prefilled with the phase's effective settings.
func (m *ConfigureWizard) startPhaseOverride() {
	if m.screen != PhaseToggleScreen || m.cursor >= len(m.phases) || !m.phases[m.cursor].configurable() {
		return
	}
	m.overridePhase = m.cursor
	agent := m.phases[m.cursor].Agent
	if agent == "" {
		agent = m.result.Agent
	}
	m.cursor = 0
	for i, entry := range m.agents {
		if entry.Agent == agent {
			m.cursor = i
			break
		}
	}
	m.screen = AgentPickerScreen
}

// exitOverride returns from the override sub-flow to the phase screen with
// the cursor back on the phase that was being configured.
func (m *ConfigureWizard) exitOverride() {
	m.cursor = m.overridePhase
	m.overridePhase = -1
	m.screen = PhaseToggleScreen
}

func (m *ConfigureWizard) selectCurrent() {
	switch m.screen {
	case AgentPickerScreen:
		if len(m.agents) == 0 {
			m.fail(ErrNoAgents)
			return
		}
		m.selected = m.agents[m.cursor].Agent
		entry, ok := m.catalog.Lookup(m.selected)
		if !ok {
			m.fail(ErrNoAgents)
			return
		}
		m.models = append([]string(nil), entry.Models...)
		m.cursor = 0
		preferred := m.defaults.Model
		if m.overridePhase >= 0 {
			preferred = m.phases[m.overridePhase].Model
			if preferred == "" && m.selected == m.result.Agent {
				preferred = m.result.Model
			}
		} else if m.selected != m.defaults.Agent {
			preferred = ""
		}
		for i, model := range m.models {
			if model == preferred {
				m.cursor = i
				break
			}
		}
		m.screen = ModelPickerScreen
	case ModelPickerScreen:
		if m.cursor == len(m.models) {
			m.manual = nil
			m.screen = ModelInputScreen
			return
		}
		if m.overridePhase >= 0 {
			m.setPhaseAgentModelPin(m.selected, m.models[m.cursor], false)
		} else {
			m.result.Agent, m.result.Model, m.result.Manual = m.selected, m.models[m.cursor], false
		}
		m.enterEffortScreen()
	case EffortPickerScreen:
		if m.overridePhase >= 0 {
			effort := wizardEfforts[m.cursor]
			if effort == m.result.Effort && !m.defaults.FullTuples {
				effort = "" // matches the global default: inherit
			}
			m.phases[m.overridePhase].Effort = effort
			m.exitOverride()
			return
		}
		m.result.Effort = wizardEfforts[m.cursor]
		if len(m.phases) == 0 {
			m.finish()
			return
		}
		m.cursor = m.firstToggleablePhase()
		m.screen = PhaseToggleScreen
	case PhaseToggleScreen:
		if m.cursor == m.saveRowIndex() {
			m.finish()
			return
		}
		// Enter on a phase row opens its agent/model/effort editor; only the
		// explicit save row confirms the wizard.
		m.startPhaseOverride()
	}
}

// setPhaseAgentModelPin stores the agent/model choice for the override phase.
// Choices matching the wizard's global selection are stored as empty pins so
// the phase keeps inheriting; a different agent pins the model with it
// because a model name is only meaningful for its agent.
func (m *ConfigureWizard) setPhaseAgentModelPin(agent config.Agent, model string, manual bool) {
	phase := &m.phases[m.overridePhase]
	if m.defaults.FullTuples {
		phase.Agent, phase.Model, phase.Manual = agent, model, manual
		return
	}
	switch {
	case agent == m.result.Agent && model == m.result.Model:
		phase.Agent, phase.Model, phase.Manual = "", "", false
	case agent == m.result.Agent:
		phase.Agent, phase.Model, phase.Manual = "", model, manual
	default:
		phase.Agent, phase.Model, phase.Manual = agent, model, manual
	}
}

func (m *ConfigureWizard) enterEffortScreen() {
	preferred := m.defaults.Effort
	if m.overridePhase >= 0 {
		preferred = m.phases[m.overridePhase].Effort
		if preferred == "" {
			preferred = m.result.Effort
		}
	}
	m.cursor = 1 // medium
	for i, effort := range wizardEfforts {
		if effort == preferred {
			m.cursor = i
			break
		}
	}
	m.screen = EffortPickerScreen
}

func (m *ConfigureWizard) togglePhase() {
	if m.screen != PhaseToggleScreen || m.cursor >= len(m.phases) || m.phases[m.cursor].Locked {
		return
	}
	m.phases[m.cursor].Enabled = !m.phases[m.cursor].Enabled
}

func (m *ConfigureWizard) finish() {
	m.result.Phases = clonePhaseStates(m.phases)
	m.quit = true
}

func (m *ConfigureWizard) handleInputKey(message tea.KeyMsg) {
	switch message.Type {
	case tea.KeyEnter:
		model := strings.TrimSpace(string(m.manual))
		if model == "" {
			return
		}
		if m.overridePhase >= 0 {
			m.setPhaseAgentModelPin(m.selected, model, true)
		} else {
			m.result.Agent, m.result.Model, m.result.Manual = m.selected, model, true
		}
		m.enterEffortScreen()
	case tea.KeyEsc:
		m.screen = ModelPickerScreen
	case tea.KeyCtrlC:
		m.cancel(ErrPickerCancelled)
	case tea.KeyBackspace:
		if len(m.manual) > 0 {
			m.manual = m.manual[:len(m.manual)-1]
		}
	case tea.KeySpace:
		m.manual = append(m.manual, ' ')
	case tea.KeyRunes:
		m.manual = append(m.manual, message.Runes...)
	}
}

func (m *ConfigureWizard) cancel(err error) { m.err, m.quit = err, true }
func (m *ConfigureWizard) fail(err error)   { m.err, m.quit = err, true }

func (m ConfigureWizard) Screen() PickerScreen { return m.screen }
func (m ConfigureWizard) Cursor() int          { return m.cursor }
func (m ConfigureWizard) Agents() []config.AgentCatalogEntry {
	entries := make([]config.AgentCatalogEntry, len(m.agents))
	for i, entry := range m.agents {
		entries[i] = entry
		entries[i].Models = append([]string(nil), entry.Models...)
	}
	return entries
}
func (m ConfigureWizard) Models() []string          { return append([]string(nil), m.models...) }
func (m ConfigureWizard) PhaseStates() []PhaseState { return clonePhaseStates(m.phases) }
func (m ConfigureWizard) Result() PickerResult      { return m.result }
func (m ConfigureWizard) Err() error                { return m.err }

// eofNotifyingReader preserves the reader contract while surfacing EOF as a
// message. Bubble Tea intentionally suppresses io.EOF from its input loop, but
// a closed or detached terminal must still terminate the picker.
type eofNotifyingReader struct {
	reader io.Reader
	notify func()
	once   sync.Once
}

func (r *eofNotifyingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if errors.Is(err, io.EOF) {
		r.once.Do(r.notify)
	}
	return n, err
}

// eofNotifyingFile keeps the cancelreader.File contract visible to Bubble Tea.
// In particular, hiding an *os.File behind an io.Reader makes cancelreader use
// its non-interruptible fallback implementation.
type eofNotifyingFile struct {
	file   cancelreader.File
	reader eofNotifyingReader
}

func (f *eofNotifyingFile) Read(p []byte) (int, error)  { return f.reader.Read(p) }
func (f *eofNotifyingFile) Write(p []byte) (int, error) { return f.file.Write(p) }
func (f *eofNotifyingFile) Close() error                { return f.file.Close() }
func (f *eofNotifyingFile) Fd() uintptr                 { return f.file.Fd() }
func (f *eofNotifyingFile) Name() string                { return f.file.Name() }

// runAltScreenProgram runs a full-screen Bubble Tea model with the shared
// terminal handling used by every gg interactive screen: TTY detection, raw
// mode, EOF surfacing, and alternate-screen setup/teardown.
func runAltScreenProgram(ctx context.Context, model tea.Model, input io.Reader, output io.Writer) (tea.Model, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if input == nil {
		input = os.Stdin
	}
	if output == nil {
		output = os.Stdout
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !interactiveTerminal(input, output) {
		return nil, ErrPickerNonInteractive
	}
	restoreRawMode, err := prepareRawMode(input)
	if err != nil {
		return nil, fmt.Errorf("enter raw mode for interactive screen: %w", err)
	}
	var program *tea.Program
	notifyEOF := func() { program.Send(EOFMsg{}) }
	if file, ok := input.(cancelreader.File); ok {
		input = &eofNotifyingFile{
			file:   file,
			reader: eofNotifyingReader{reader: file, notify: notifyEOF},
		}
	} else {
		input = &eofNotifyingReader{reader: input, notify: notifyEOF}
	}
	program = tea.NewProgram(model, tea.WithContext(ctx), tea.WithInput(input), tea.WithOutput(output), tea.WithAltScreen())
	final, err := program.Run()
	if restoreErr := restoreRawMode(); restoreErr != nil {
		err = errors.Join(err, fmt.Errorf("restore terminal raw mode: %w", restoreErr))
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("run interactive screen: %w", err)
	}
	return final, nil
}

// RunConfigureWizard drives the full-screen configuration wizard: agent,
// model (with manual entry), effort, then per-phase enable checkboxes.
func RunConfigureWizard(ctx context.Context, catalog config.AgentCatalog, defaults WizardDefaults, input io.Reader, output io.Writer) (PickerResult, error) {
	final, err := runAltScreenProgram(ctx, NewConfigureWizard(catalog, defaults), input, output)
	if err != nil {
		return PickerResult{}, err
	}
	picked, ok := final.(ConfigureWizard)
	if !ok {
		return PickerResult{}, errors.New("run configuration picker: unexpected model")
	}
	if picked.Err() != nil {
		return PickerResult{}, picked.Err()
	}
	return picked.Result(), nil
}
