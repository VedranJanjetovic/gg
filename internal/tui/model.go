// Package tui presents durable gg project progress without owning orchestration.
package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/state"
	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/muesli/termenv"
)

// DefaultPollInterval bounds refresh latency without continuously reading state.
const DefaultPollInterval = 250 * time.Millisecond

// Loader returns the latest durable state for the attached project.
type Loader func(context.Context) (state.ProjectState, error)

// Actions contains synchronous lifecycle operations. Bubble Tea owns command
// execution and cancellation; this package does not create background goroutines.
type Actions struct {
	Start         func(context.Context) error
	Stop          func(context.Context) error
	Resume        func(context.Context) error
	Skip          func(context.Context) error
	SkipAvailable bool
	SkipLabel     string
	// SkipTarget projects the current durable state into the exact skip
	// target so polling can replace an earlier occurrence with a new one.
	SkipTarget   func(state.ProjectState) (bool, string)
	OpenCode     func(context.Context) error
	OpenTerminal func(context.Context) error
}

// PhaseStatus is the presentation status of a configured phase or subphase.
type PhaseStatus string

const (
	PhasePending   PhaseStatus = "pending"
	PhaseRunning   PhaseStatus = "running"
	PhaseSucceeded PhaseStatus = "succeeded"
	PhaseFailed    PhaseStatus = "failed"
	PhaseSkipped   PhaseStatus = "skipped"
	PhaseStopped   PhaseStatus = "stopped"
)

// PhaseView is a read-only projection of one configured phase.
type PhaseView struct {
	ID        string
	Name      string
	Status    PhaseStatus
	SkipCount int
	Subphases []PhaseView
}

// PendingPipeline is the explicit display fallback used while a newly created
// pending project still has the legacy empty execution snapshot.
type PendingPipeline struct {
	Phases               []pipeline.PhaseID
	DevelopmentSubphases pipeline.DevelopmentSubphaseGeneration
}

// DefaultPendingPipeline returns the canonical initial pipeline. Callers must
// opt into this fallback with WithPendingPipeline.
func DefaultPendingPipeline() PendingPipeline {
	canonical := pipeline.DefaultPipeline().Phases()
	phases := make([]pipeline.PhaseID, 0, len(canonical))
	for _, phase := range canonical {
		phases = append(phases, phase.ID())
	}
	return PendingPipeline{Phases: phases, DevelopmentSubphases: pipeline.DevelopmentSubphaseGeneration{}}
}

type pollScheduler func(time.Duration, func(time.Time) tea.Msg) tea.Cmd

type modelOptions struct {
	pollInterval    time.Duration
	poll            pollScheduler
	pending         *PendingPipeline
	color           bool
	progressWidth   int
	initialNotice   string
	groomingPending bool
}

// WithInitialNotice shows a one-off message below the progress view when the
// session opens (for example why the grooming interview was skipped).
func WithInitialNotice(notice string) Option {
	return func(options *modelOptions) { options.initialNotice = notice }
}

// interviewOpen reports whether the grooming interview can be (re-)entered:
// either the attach flow flagged it, or live state shows an unfinished
// interview on a project that is not running and not finished.
func (m Model) interviewOpen() bool {
	if m.groomingPending {
		return true
	}
	if m.project.Interview == nil || m.project.Interview.Done {
		return false
	}
	return m.project.Status != state.StatusRunning && !m.project.Status.IsTerminal()
}

// WithGroomingPending marks the project as waiting for grooming answers: the
// view announces it and the g key quits the session with
// ErrGroomingRequested so the caller can re-open the interview.
func WithGroomingPending(pending bool) Option {
	return func(options *modelOptions) { options.groomingPending = pending }
}

// Option configures a progress model.
type Option func(*modelOptions)

// WithPollInterval sets how often durable project state is reloaded.
func WithPollInterval(interval time.Duration) Option {
	return func(options *modelOptions) { options.pollInterval = interval }
}

// WithPendingPipeline explicitly permits rendering a pending project's empty
// snapshot until its first run atomically persists the executable snapshot.
func WithPendingPipeline(fallback PendingPipeline) Option {
	return func(options *modelOptions) {
		copy := fallback
		copy.Phases = append([]pipeline.PhaseID(nil), fallback.Phases...)
		copy.DevelopmentSubphases.Subphases = append([]pipeline.DevelopmentSubphaseDefinition(nil), fallback.DevelopmentSubphases.Subphases...)
		options.pending = &copy
	}
}

// WithColor controls ANSI styling. Disable it for deterministic snapshots and
// non-interactive status output.
func WithColor(enabled bool) Option {
	return func(options *modelOptions) { options.color = enabled }
}

func withPollScheduler(scheduler pollScheduler) Option {
	return func(options *modelOptions) { options.poll = scheduler }
}

type phaseDefinition struct {
	id        pipeline.PhaseID
	name      string
	subphases []subphaseDefinition
}

type subphaseDefinition struct {
	id   string
	name string
}

// Model is the Bubble Tea project progress model.
type Model struct {
	ctx          context.Context
	project      state.ProjectState
	loader       Loader
	actions      Actions
	definitions  []phaseDefinition
	phases       []PhaseView
	styles       styles
	spinner      spinner.Model
	progress     progress.Model
	pollInterval time.Duration
	poll         pollScheduler
	lastErr      error
	notice       string
	width        int
	// groomingPending marks a project parked on unanswered grooming
	// questions; groomingRequested records that the user pressed g to
	// re-enter the interview (the session quits and the caller re-runs it).
	groomingPending      bool
	groomingRequested    bool
	interactiveRequested bool
	showTokenDetail      bool
	startPending         bool
	stopPending          bool
	resumePending        bool
	skipPending          bool
	skipConfirm          bool
	skipResolved         bool
	skipLabel            string
	skipOccurrenceID     string
	codePending          bool
	terminalPending      bool
}

// NewModel builds a progress model from authoritative persisted state.
func NewModel(ctx context.Context, project state.ProjectState, loader Loader, actions Actions, options ...Option) (Model, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	settings := modelOptions{
		pollInterval:  DefaultPollInterval,
		poll:          tea.Tick,
		color:         true,
		progressWidth: 30,
	}
	for _, option := range options {
		option(&settings)
	}
	if settings.pollInterval <= 0 {
		return Model{}, errors.New("poll interval must be positive")
	}
	definitions, err := projectPipeline(project, settings.pending)
	if err != nil {
		return Model{}, err
	}

	spin := spinner.New()
	spin.Spinner = spinner.MiniDot
	barOptions := []progress.Option{progress.WithWidth(settings.progressWidth), progress.WithSolidFill("#22c55e")}
	if !settings.color {
		barOptions = append(barOptions, progress.WithColorProfile(termenv.Ascii))
	}
	model := Model{
		ctx:          ctx,
		project:      project,
		loader:       loader,
		actions:      actions,
		definitions:  definitions,
		styles:       newStyles(settings.color),
		spinner:      spin,
		progress:     progress.New(barOptions...),
		pollInterval: settings.pollInterval,
		poll:         settings.poll,
	}
	model.spinner.Style = model.styles.running
	model.phases = projectPhases(project, definitions)
	model.startPending = project.Status == state.StatusPending && actions.Start != nil
	model.notice = settings.initialNotice
	model.groomingPending = settings.groomingPending
	return model, nil
}

// Project returns the latest durable state observed by the model.
func (m Model) Project() state.ProjectState { return m.project }

// Phases returns a copy of the current presentation projection.
func (m Model) Phases() []PhaseView { return clonePhaseViews(m.phases) }

// LastError returns the latest loader or lifecycle action error.
func (m Model) LastError() error { return m.lastErr }

func projectPipeline(project state.ProjectState, pending *PendingPipeline) ([]phaseDefinition, error) {
	plan, generation, _, err := pipeline.RestoreExecution(project.PipelineConfig)
	if err == nil {
		return definitionsFromExecution(plan, generation)
	}
	if project.Status != state.StatusPending || !bytes.Equal(bytes.TrimSpace(project.PipelineConfig.Data), []byte("{}")) {
		return nil, fmt.Errorf("restore project pipeline: %w", err)
	}
	if pending == nil {
		return nil, errors.New("pending project has an empty pipeline snapshot; provide an explicit pending pipeline")
	}
	return definitionsFromPending(*pending)
}

func definitionsFromExecution(plan pipeline.ExecutablePipeline, generation pipeline.DevelopmentSubphaseGeneration) ([]phaseDefinition, error) {
	ids := make([]pipeline.PhaseID, 0, len(plan.Phases()))
	for _, phase := range plan.Phases() {
		ids = append(ids, phase.Phase().ID())
	}
	// RestoreExecution has already validated the persisted schema-specific
	// order. Do not compare it with the ambient canonical order: legacy
	// snapshots intentionally retain QA before Rebase.
	return definitions(ids, generation, false)
}

func definitionsFromPending(pending PendingPipeline) ([]phaseDefinition, error) {
	return definitions(pending.Phases, pending.DevelopmentSubphases, true)
}

func definitions(ids []pipeline.PhaseID, generation pipeline.DevelopmentSubphaseGeneration, validateCanonicalOrder bool) ([]phaseDefinition, error) {
	canonical := pipeline.DefaultPipeline().Phases()
	byID := make(map[pipeline.PhaseID]pipeline.Phase, len(canonical))
	indexes := make(map[pipeline.PhaseID]int, len(canonical))
	for index, phase := range canonical {
		byID[phase.ID()] = phase
		indexes[phase.ID()] = index
	}
	if len(ids) == 0 {
		return nil, errors.New("display pipeline has no phases")
	}
	result := make([]phaseDefinition, 0, len(ids))
	previous := -1
	for _, id := range ids {
		phase, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("display pipeline contains unknown phase %q", id)
		}
		if validateCanonicalOrder && indexes[id] <= previous {
			return nil, fmt.Errorf("display pipeline phases are not in canonical order at %q", id)
		}
		previous = indexes[id]
		definition := phaseDefinition{id: id, name: phase.Metadata().DisplayName}
		if id == pipeline.PhaseDevelopment {
			subphases, err := pipeline.GenerateDevelopmentSubphases(generation)
			if err != nil {
				return nil, fmt.Errorf("generate Development subphases: %w", err)
			}
			if len(subphases) == 0 {
				return nil, errors.New("display pipeline requires at least one Development subphase")
			}
			for _, subphase := range subphases {
				definition.subphases = append(definition.subphases, subphaseDefinition{id: string(subphase.ID()), name: subphase.DisplayName()})
			}
		}
		result = append(result, definition)
	}
	return result, nil
}

func projectPhases(project state.ProjectState, definitions []phaseDefinition) []PhaseView {
	latest := make(map[string]state.PhaseRecord)
	for _, record := range project.PhaseHistory {
		latest[phaseKey(record.Phase, record.Subphase)] = record
	}

	phases := make([]PhaseView, 0, len(definitions))
	for _, definition := range definitions {
		phaseRecord := latest[phaseKey(string(definition.id), "")]
		phase := PhaseView{ID: string(definition.id), Name: definition.name, Status: phaseStatus(phaseRecord), SkipCount: project.SkipCount(string(definition.id), "")}
		for _, definition := range definition.subphases {
			record := latest[phaseKey(string(pipeline.PhaseDevelopment), definition.id)]
			phase.Subphases = append(phase.Subphases, PhaseView{
				ID: definition.id, Name: definition.name,
				Status: phaseStatus(record), SkipCount: project.SkipCount(string(pipeline.PhaseDevelopment), definition.id),
			})
		}
		if len(phase.Subphases) != 0 {
			phase.Status = aggregateSubphases(phase.Status, phase.Subphases)
		}
		phases = append(phases, phase)
	}

	if project.Status == state.StatusFinished {
		for index := range phases {
			phases[index].Status = PhaseSucceeded
			for subphase := range phases[index].Subphases {
				phases[index].Subphases[subphase].Status = PhaseSucceeded
			}
		}
		return phases
	}

	for index := range phases {
		if phases[index].ID != project.CurrentPhase {
			continue
		}
		if phases[index].Status != PhaseSkipped {
			phases[index].Status = statusFromLifecycle(project.Status)
		}
		for subphase := range phases[index].Subphases {
			if phases[index].Subphases[subphase].ID == project.CurrentSubphase {
				if phases[index].Subphases[subphase].Status != PhaseSkipped {
					phases[index].Subphases[subphase].Status = statusFromLifecycle(project.Status)
				}
			}
		}
		break
	}
	return phases
}

func phaseStatus(record state.PhaseRecord) PhaseStatus {
	if record.Skip != nil && record.Status == state.StatusFailed {
		return PhaseSkipped
	}
	return statusFromLifecycle(record.Status)
}

func aggregateSubphases(parent PhaseStatus, children []PhaseView) PhaseStatus {
	if parent != PhasePending {
		return parent
	}
	allSucceeded := len(children) != 0
	for _, child := range children {
		switch child.Status {
		case PhaseFailed:
			return PhaseFailed
		case PhaseStopped:
			return PhaseStopped
		case PhaseSkipped:
			// A confirmed skip waives the failed execution; it does not keep
			// the containing Development phase failed.
			continue
		case PhaseRunning:
			return PhaseRunning
		case PhasePending:
			allSucceeded = false
		}
	}
	if allSucceeded {
		return PhaseSucceeded
	}
	return PhasePending
}

func statusFromLifecycle(status state.LifecycleStatus) PhaseStatus {
	switch status {
	case state.StatusRunning:
		return PhaseRunning
	case state.StatusFinished:
		return PhaseSucceeded
	case state.StatusFailed, state.StatusTerminated:
		return PhaseFailed
	case state.StatusStopped:
		return PhaseStopped
	default:
		return PhasePending
	}
}

func phaseKey(phase, subphase string) string { return phase + "\x00" + subphase }

func clonePhaseViews(source []PhaseView) []PhaseView {
	result := make([]PhaseView, len(source))
	copy(result, source)
	for index := range result {
		result[index].Subphases = clonePhaseViews(source[index].Subphases)
	}
	return result
}
