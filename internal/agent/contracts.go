package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/VedranJanjetovic/gg/internal/config"
	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/state"
)

// Runner executes one pipeline phase in an isolated agent process.
// Implementations own agent-specific command construction and process
// orchestration; callers provide the already-resolved project and settings.
type Runner interface {
	Run(context.Context, RunRequest) (RunResult, error)
}

// RunRequest is the narrow input contract for one phase execution.
// Project contains the canonical goal, acceptance criteria, and worktree
// metadata. Settings is the existing resolved configuration contract; agent
// runners must not resolve configuration themselves.
type RunRequest struct {
	Project          state.ProjectState
	Phase            pipeline.PhaseID
	Subphase         string
	Settings         config.AgentSettings
	Prompt           string
	WorkingDirectory string
	ArtifactPaths    []string
	RunID            string
}

// Disposition is the semantic outcome declared by an agent in the canonical
// phase artifact. Process success and semantic success are separate: a phase
// may complete normally while reporting failed or blocked work.
type Disposition string

const (
	DispositionPassed   Disposition = "passed"
	DispositionFailed   Disposition = "failed"
	DispositionBlocked  Disposition = "blocked"
	DispositionFeedback Disposition = "feedback"
)

// IsValid reports whether the disposition is part of the phase-result
// protocol.
func (disposition Disposition) IsValid() bool {
	switch disposition {
	case DispositionPassed, DispositionFailed, DispositionBlocked, DispositionFeedback:
		return true
	default:
		return false
	}
}

// SemanticFailureError reports a valid, non-passing disposition from the
// canonical phase artifact. It is distinct from process, protocol, and
// persistence failures so callers can retry only a pure semantic failure.
type SemanticFailureError struct {
	Phase       pipeline.PhaseID
	Disposition Disposition
	Details     string
}

func (e *SemanticFailureError) Error() string {
	if e == nil {
		return "phase reported a semantic failure"
	}
	if e.Details != "" {
		return fmt.Sprintf("phase %q reported semantic disposition %q: %s", e.Phase, e.Disposition, e.Details)
	}
	return fmt.Sprintf("phase %q reported semantic disposition %q", e.Phase, e.Disposition)
}

// IsSemanticFailure reports whether err consists exclusively of semantic
// failures. A semantic failure joined with any operational error is not pure
// and must not be treated as retryable.
func IsSemanticFailure(err error) bool {
	if err == nil {
		return false
	}
	return containsOnlySemanticFailures(err)
}

func containsOnlySemanticFailures(err error) bool {
	switch current := err.(type) {
	case interface{ Unwrap() []error }:
		children := current.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if child == nil || !containsOnlySemanticFailures(child) {
				return false
			}
		}
		return true
	case interface{ Unwrap() error }:
		child := current.Unwrap()
		return child != nil && containsOnlySemanticFailures(child)
	default:
		semantic, ok := err.(*SemanticFailureError)
		return ok && semantic != nil && semantic.Disposition.IsValid() && semantic.Disposition != DispositionPassed
	}
}

// RunResult records the process outcome without owning project lifecycle state.
// The orchestrator can translate Status and the artifact paths into states
// existing ProjectState and PhaseRecord models.
type RunResult struct {
	ProjectSlug   string
	Phase         pipeline.PhaseID
	Subphase      string
	Status        state.LifecycleStatus
	Disposition   Disposition
	ExitCode      int
	StartedAt     time.Time
	FinishedAt    time.Time
	Duration      time.Duration
	ArtifactPaths []string
	// LogPaths contains runner-owned raw log and metadata files. These are
	// intentionally separate from project-produced artifacts.
	LogPaths []string
	// TokensUsed is the total token count the agent reported for this run;
	// zero when the agent did not report usage.
	TokensUsed int64
	// ExternalIdentity carries a created GitOps resource such as a pull-request URL.
	ExternalIdentity string
	// Error is the human-readable failure reason for a failed run, persisted
	// so attached screens can explain the failure without log digging.
	Error string
	// CostUSD is the agent-reported cost of this run in US dollars; zero
	// when the agent does not report cost.
	CostUSD float64
}

// ProcessSpec describes an executable invocation. Args are passed directly
// without a shell. WorkingDirectory is required and is the project worktree.
// Env contains KEY=VALUE overrides merged with the inherited environment.
type ProcessSpec struct {
	Command          string
	Args             []string
	WorkingDirectory string
	Env              []string
	// EventSink and RawLogWriter override factory defaults for this invocation.
	// They make concurrent phase runs independently observable.
	Events EventSink
	Logs   RawLogWriter
}

// Process is the lifecycle seam for one started child process.
type Process interface {
	Wait() (ProcessResult, error)
	Cancel() error
}

// ProcessResult contains process-owned completion data. ExitCode is meaningful
// when the process started successfully; a non-nil Wait error describes a
// start, wait, or cancellation failure.
type ProcessResult struct {
	ExitCode   int
	StartedAt  time.Time
	FinishedAt time.Time
	Duration   time.Duration
}

// ProcessFactory starts processes from provider-independent specifications.
type ProcessFactory interface {
	Start(context.Context, ProcessSpec) (Process, error)
}

// EventType identifies an observable runner lifecycle event.
type EventType string

const (
	EventStarted   EventType = "started"
	EventOutput    EventType = "output"
	EventCompleted EventType = "completed"
	EventFailed    EventType = "failed"
	EventCanceled  EventType = "canceled"
)

// Event is the stream contract shared by TUI and persistence consumers.
// Payload is raw process output for EventOutput and is empty for lifecycle
// events. Consumers must copy Payload if they retain it after Publish returns.
type Event struct {
	ProjectSlug string
	Phase       pipeline.PhaseID
	Subphase    string
	Type        EventType
	Stream      string
	Payload     []byte
	At          time.Time
	Result      *RunResult
}

// EventSink receives runner events for live consumers such as the TUI.
type EventSink interface {
	Publish(context.Context, Event) error
}

// EventStore persists runner events, including raw output events used for
// phase logs. It deliberately accepts the event contract rather than a second
// log model.
type EventStore interface {
	Append(context.Context, Event) error
}

// ResultStore persists completed runner results. Lifecycle state remains owned
// by the state package and is updated by the orchestrator using this result.
type ResultStore interface {
	Save(context.Context, RunResult) error
}

// Persistence combines the two durable runner sinks for callers that want one
// dependency while keeping event and result storage independently testable.
type Persistence interface {
	EventStore
	ResultStore
}
