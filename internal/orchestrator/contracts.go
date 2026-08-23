// Package orchestrator owns the contracts that coordinate pipeline execution.
// It deliberately contains no execution, persistence, or CLI implementation.
package orchestrator

import (
	"context"
	"time"

	"github.com/VedranJanjetovic/gg/internal/agent"
	"github.com/VedranJanjetovic/gg/internal/ci"
	"github.com/VedranJanjetovic/gg/internal/config"
	"github.com/VedranJanjetovic/gg/internal/git"
	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/pr"
	"github.com/VedranJanjetovic/gg/internal/state"
)

// Request contains the already-resolved inputs for one project execution.
// Project, Pipeline, and agent settings remain owned by their respective
// packages; the orchestrator only coordinates them.
type Request struct {
	Project        state.ProjectState
	Pipeline       pipeline.ExecutablePipeline
	PhaseContracts map[pipeline.PhaseID]string
	Subphases      pipeline.DevelopmentSubphaseGeneration
	MaxIterations  int
	RunID          string
	GitOps         config.GitOpsConfig
	ArtifactRoot   string
	PullRequestURL string
	// AllowDevelopmentSubphaseWithoutCommit is an explicit compatibility escape
	// hatch for workflows whose contract does not require a development commit.
	// The default is enforcement when a verifier is configured.
	AllowDevelopmentSubphaseWithoutCommit bool
	// PRCIMonitor optionally owns post-pipeline PR merge/CI lifecycle monitoring.
	PRCIMonitor LifecycleMonitor
	// PlanScope confines one Development subphase run to a single plan phase;
	// nil means the run covers the whole worktree (no plan, or a QA feedback
	// fix pass). Set per-dispatch by the development loop, never persisted.
	PlanScope *PlanPhaseScope
	// PlanningRetry is transient context for a corrective Planning invocation.
	// It is never persisted as project state; the rejected artifact and exact
	// violations are quoted into the fresh standalone prompt.
	PlanningRetry *PlanningRetry
}

type PlanningRetry struct {
	Attempt    int
	Artifact   string
	Violations []string
}

// PlanPhaseScope names the plan phase a Development run is confined to.
type PlanPhaseScope struct {
	Name  string
	Index int // 1-based position in the plan's execution order
	Total int
}

// PhaseRunner is the existing agent execution contract consumed by a future
// orchestrator implementation.
type PhaseRunner = agent.Runner

// PhaseOutcome describes one phase or development-subphase execution. Result
// carries process-owned timing, status, artifacts, and logs from agent.Runner.
type PhaseOutcome struct {
	Result                   agent.RunResult
	Iteration                int
	FeedbackArtifactPaths    []string
	ConflictResolutionNeeded bool
	retryableSemanticFailure bool
}

// GitOpsRebaser is the narrow Git adapter required by the configured Rebase phase.
type GitOpsRebaser interface {
	FetchParent(context.Context, string) (git.FetchResult, error)
	RebaseProject(context.Context, git.RebaseRequest) (git.RebaseResult, error)
}

type PullRequestService interface {
	Create(context.Context, pr.Request) (pr.Result, error)
}

type CIService interface {
	Monitor(context.Context, ci.Config) (ci.Result, error)
}

// LifecycleMonitor observes a created PR until merge or a bounded failure.
type LifecycleMonitor interface {
	Monitor(context.Context, PRCIRequest) (PRCIResult, error)
}

// EventType identifies an orchestrator-owned lifecycle transition. Process
// output and process lifecycle events remain owned by agent.Event.
type EventType string

const (
	EventProjectCreated   EventType = "project_created"
	EventPhaseStarted     EventType = "phase_started"
	EventPhaseSucceeded   EventType = "phase_succeeded"
	EventPhaseFailed      EventType = "phase_failed"
	EventConflictDetected EventType = "conflict_detected"
	EventFeedbackCreated  EventType = "feedback_created"
	EventPhaseRetried     EventType = "phase_retried"
	EventProjectStopped   EventType = "project_stopped"
	EventProjectFinished  EventType = "project_finished"
)

// Event records an orchestrator lifecycle transition for TUI and persistence
// consumers. Process output must be published through agent.Event instead.
type Event struct {
	ProjectSlug string
	Phase       pipeline.PhaseID
	Subphase    string
	Type        EventType
	At          time.Time
	Outcome     *PhaseOutcome
	Error       error
}

// EventSink receives orchestrator lifecycle transitions.
type EventSink interface {
	Publish(context.Context, Event) error
}

// StopRequest identifies the active project execution to stop.
type StopRequest struct {
	ProjectSlug string
	RunID       string
}

// ResumeRequest identifies a persisted project to resume from its state-owned
// current phase and subphase.
type ResumeRequest struct {
	ProjectSlug string
	RunID       string
	// Execution is required when resuming after an application restart. When
	// omitted, the controller reuses the request retained for RunID.
	Execution Request
}

// Controller is the application-facing orchestration boundary. Implementations
// own execution order and state transitions; callers provide resolved inputs.
type Controller interface {
	Execute(context.Context, Request) ([]PhaseOutcome, error)
	Stop(context.Context, StopRequest) error
	Resume(context.Context, ResumeRequest) ([]PhaseOutcome, error)
}

// LoopDecision describes how a feedback loop should proceed after an outcome.
type LoopDecision string

const (
	LoopComplete  LoopDecision = "complete"
	LoopRetry     LoopDecision = "retry"
	LoopExhausted LoopDecision = "exhausted"
)

// LoopState is the small amount of orchestration state needed between loop
// iterations. Feedback artifacts remain paths, not a second artifact model.
type LoopState struct {
	Iteration             int
	FeedbackArtifactPaths []string
}

// FeedbackLoop decides whether a phase should complete or be retried.
type FeedbackLoop interface {
	Decide(context.Context, LoopState, PhaseOutcome) (LoopDecision, error)
}

// Conflict identifies a rebase conflict that requires orchestration routing.
type Conflict struct {
	Phase         pipeline.PhaseID
	Subphase      string
	WorktreePath  string
	ArtifactPaths []string
	Err           error
}

// ConflictRoute identifies the next owner for a resolved conflict.
type ConflictRoute string

const (
	ConflictRouteQA       ConflictRoute = "qa"
	ConflictRouteTerminal ConflictRoute = "terminal"
)

// ConflictRouter maps a conflict outcome to the next pipeline owner. The
// default product flow routes a resolved rebase conflict back through QA.
type ConflictRouter interface {
	Route(context.Context, Conflict) (ConflictRoute, error)
}

// ConflictStateReader reports whether Git still has unmerged paths in a worktree.
type ConflictStateReader interface {
	HasUnresolvedConflicts(context.Context, string) (bool, error)
}

// DevelopmentCommitVerifier snapshots and verifies Git progress for each
// development subphase. Git-specific output parsing remains in the adapter.
type DevelopmentCommitVerifier interface {
	HeadCommit(context.Context, string) (string, error)
	VerifyUnsignedDevelopmentCommits(context.Context, string, string, bool) error
	// AutoCommitUncommittedChanges preserves work an agent left uncommitted
	// by staging and committing it unsigned; a clean worktree is a no-op.
	AutoCommitUncommittedChanges(context.Context, string, string) error
}

// ProjectObserver is the read-only global observation boundary. Implementations
// must load all projects from durable state and return deterministic results.
type ProjectObserver interface {
	ObserveAll(context.Context) ([]state.ProjectObservation, error)
}

// ResumeAllRequest identifies a restart-safe resume sweep. The sweep is not
// tied to a TUI attachment and may be retried after a process restart.
type ResumeAllRequest struct {
	RunID string
}

// ResumeResult records one project's independent resume outcome. A failed item
// must not prevent other projects from being attempted.
type ResumeResult struct {
	ProjectSlug string
	Kind        state.RerunKind
	Err         error
}

// ResumeCoordinator owns all-project resume policy while Controller remains the
// single-project execution boundary.
type ResumeCoordinator interface {
	ResumeAll(context.Context, ResumeAllRequest) ([]ResumeResult, error)
}

// NotificationKind identifies a lifecycle notification independent of whether
// a TUI is attached.
type NotificationKind string

const (
	NotificationProjectCompleted NotificationKind = "project_completed"
	NotificationProjectFailed    NotificationKind = "project_failed"
	NotificationProjectStopped   NotificationKind = "project_stopped"
)

// Notification is a transport-neutral completion event.
type Notification struct {
	Project      state.ProjectObservation
	Kind         NotificationKind
	At           time.Time
	TerminalKind state.TerminalKind
}

// NotificationSink receives notifications without requiring a foreground UI.
type NotificationSink interface {
	Notify(context.Context, Notification) error
}
