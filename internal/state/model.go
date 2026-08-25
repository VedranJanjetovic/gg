package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/VedranJanjetovic/gg/internal/proof"
)

// CurrentSchemaVersion is the version of ProjectState understood by this package.
const CurrentSchemaVersion = 1

// MaxVerificationRemediationAttempts is the fixed retry budget every
// verification boundary gets. It is deliberately a constant rather than a
// per-project setting: nothing configures it, and a boundary that could grant
// itself unlimited remediation would not be a boundary.
const MaxVerificationRemediationAttempts = 3

// VerificationAdapter identifies the parser used to turn a command result
// into stable verification findings. The adapters are deliberately a small
// closed set until a later phase adds execution and comparison behavior.
type VerificationAdapter string

const (
	VerificationAdapterGofmtEmpty   VerificationAdapter = "gofmt-empty"
	VerificationAdapterGoTest       VerificationAdapter = "go-test"
	VerificationAdapterGoDiagnostic VerificationAdapter = "go-diagnostic"
	VerificationAdapterGitDiffCheck VerificationAdapter = "git-diff-check"
)

func (adapter VerificationAdapter) IsValid() bool {
	switch adapter {
	case VerificationAdapterGofmtEmpty, VerificationAdapterGoTest, VerificationAdapterGoDiagnostic, VerificationAdapterGitDiffCheck:
		return true
	default:
		return false
	}
}

// VerificationStep is one directly executable, named planned check.
type VerificationStep struct {
	Name    string              `json:"name"`
	Command string              `json:"command"`
	Args    []string            `json:"args"`
	Env     map[string]string   `json:"env,omitempty"`
	Adapter VerificationAdapter `json:"adapter"`
}

// VerificationContract is the strict declaration produced by Planning. The
// remediation budget is not part of it; see MaxVerificationRemediationAttempts.
type VerificationContract struct {
	Steps      []VerificationStep `json:"steps"`
	RepairMode bool               `json:"repairMode"`
}

// Validate checks the executable contract without executing any external
// command. RepairMode is a value because its presence is enforced by the
// strict frontmatter parser before the contract reaches this model.
func (contract VerificationContract) Validate() error {
	if len(contract.Steps) == 0 {
		return errors.New("verification contract requires at least one step")
	}
	seen := make(map[string]struct{}, len(contract.Steps))
	for index, step := range contract.Steps {
		name := strings.TrimSpace(step.Name)
		if name == "" {
			return fmt.Errorf("verification step %d requires a name", index)
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("verification step %q is duplicated", name)
		}
		seen[name] = struct{}{}
		if strings.TrimSpace(step.Command) == "" {
			return fmt.Errorf("verification step %q requires a command", name)
		}
		if !step.Adapter.IsValid() {
			return fmt.Errorf("verification step %q has unsupported adapter %q", name, step.Adapter)
		}
		for key := range step.Env {
			if strings.TrimSpace(key) == "" || strings.ContainsAny(key, "=\x00") {
				return fmt.Errorf("verification step %q has invalid environment key", name)
			}
		}
	}
	return nil
}

// VerificationFinding is the concise durable form of one verification
// observation. Raw command output remains in the log referenced by LogPath.
type VerificationFinding struct {
	CheckName      string `json:"checkName"`
	Identity       string `json:"identity,omitempty"`
	Reason         string `json:"reason,omitempty"`
	Classification string `json:"classification,omitempty"`
	LogPath        string `json:"logPath,omitempty"`
	NextAction     string `json:"nextAction,omitempty"`
}

// VerificationCommandResult is the durable, bounded status of one planned
// verification command. Raw output remains in LogPath; retaining the command
// status lets resumed boundaries distinguish a repaired failure from a
// missing or unclassifiable result.
type VerificationCommandResult struct {
	CheckName      string                `json:"checkName"`
	Command        string                `json:"command,omitempty"`
	Args           []string              `json:"args,omitempty"`
	Status         string                `json:"status"`
	Failures       []VerificationFinding `json:"failures,omitempty"`
	LogPath        string                `json:"logPath,omitempty"`
	RetryCount     int                   `json:"retryCount,omitempty"`
	UnavailableErr string                `json:"unavailableErr,omitempty"`
}

// VerificationState stores the contract and lifecycle cursor needed by later
// verification phases. It is optional so schema-1 project state remains valid.
type VerificationState struct {
	PlannedSteps           []VerificationStep          `json:"plannedSteps"`
	RepairMode             bool                        `json:"repairMode"`
	ParentBaselineCaptured bool                        `json:"parentBaselineCaptured"`
	ParentBaseline         []VerificationFinding       `json:"parentBaseline,omitempty"`
	ParentResults          []VerificationCommandResult `json:"parentResults,omitempty"`
	PromotedRequiredGreen  []string                    `json:"promotedRequiredGreen,omitempty"`
	CurrentResults         []VerificationCommandResult `json:"currentResults,omitempty"`
	CurrentFindings        []VerificationFinding       `json:"currentFindings,omitempty"`
	Warnings               []VerificationFinding       `json:"warnings,omitempty"`
	BoundaryCursor         string                      `json:"boundaryCursor,omitempty"`
	RemediationAttempts    int                         `json:"remediationAttempts,omitempty"`
	NextAction             string                      `json:"nextAction,omitempty"`
}

// LifecycleStatus is the persisted lifecycle state of a project.
type LifecycleStatus string

const (
	StatusPending    LifecycleStatus = "pending"
	StatusRunning    LifecycleStatus = "running"
	StatusStopped    LifecycleStatus = "stopped"
	StatusFailed     LifecycleStatus = "failed"
	StatusFinished   LifecycleStatus = "finished"
	StatusTerminated LifecycleStatus = "terminated"
)

// IsValid reports whether status is one of the supported persisted lifecycle states.
func (status LifecycleStatus) IsValid() bool {
	switch status {
	case StatusPending, StatusRunning, StatusStopped, StatusFailed, StatusFinished, StatusTerminated:
		return true
	default:
		return false
	}
}

// IsTerminal reports whether a lifecycle status cannot transition further.
func (status LifecycleStatus) IsTerminal() bool {
	return status == StatusFinished || status == StatusTerminated
}

// IsActive reports whether a project is not in a terminal state.
func (status LifecycleStatus) IsActive() bool {
	return status.IsValid() && !status.IsTerminal()
}

// PipelineConfigSnapshot stores the exact pipeline configuration used by a run.
// Data is kept as JSON so this package does not own the pipeline configuration schema.
type PipelineConfigSnapshot struct {
	SchemaVersion int             `json:"schemaVersion"`
	Data          json.RawMessage `json:"data"`
}

// PhaseRecord records one visit to a pipeline phase or subphase.
type PhaseRecord struct {
	Phase         string          `json:"phase"`
	Subphase      string          `json:"subphase,omitempty"`
	Status        LifecycleStatus `json:"status"`
	StartedAt     time.Time       `json:"startedAt"`
	CompletedAt   *time.Time      `json:"completedAt,omitempty"`
	ArtifactPaths []string        `json:"artifactPaths,omitempty"`
	// DeferredChecks contains validated remote-only checks from this execution.
	// They are informational and do not claim that the checks passed.
	DeferredChecks []proof.DeferredCheck `json:"deferredChecks,omitempty"`
	// OccurrenceID identifies one execution occurrence. It is assigned when a
	// new execution starts and is intentionally never reused by a retry.
	OccurrenceID string `json:"occurrenceId,omitempty"`
	// Outcome is populated when an executable agent process has completed.
	// It is optional so state written by earlier versions remains compatible.
	Outcome *ExecutionOutcome `json:"outcome,omitempty"`
	// Skip records an explicit user-confirmed waiver for this failed occurrence.
	// The original Status and Outcome remain unchanged.
	Skip *SkipResolution `json:"skip,omitempty"`
}

// SkipCleanupStatus describes the cleanup result recorded with a confirmed
// skip. A missing status is invalid for new skip resolutions, while old phase
// records may omit the entire resolution.
type SkipCleanupStatus string

const (
	SkipCleanupSucceeded   SkipCleanupStatus = "succeeded"
	SkipCleanupNotRequired SkipCleanupStatus = "not_required"
)

// SkipCleanup is the durable result of cleanup for one skipped execution.
// Evidence is descriptive and may include paths, commands, or Git identity.
type SkipCleanup struct {
	Status   SkipCleanupStatus `json:"status"`
	Evidence []string          `json:"evidence,omitempty"`
}

// SkipResolution is attached to exactly one failed execution occurrence.
// NextPhase and NextSubphase are the cursor selected for the continuation;
// both are empty only when the skipped unit was the final execution unit.
type SkipResolution struct {
	ConfirmedAt      time.Time   `json:"confirmedAt"`
	Cleanup          SkipCleanup `json:"cleanup"`
	NextPhase        string      `json:"nextPhase,omitempty"`
	NextSubphase     string      `json:"nextSubphase,omitempty"`
	ExternalIdentity string      `json:"externalIdentity,omitempty"`
}

func (status SkipCleanupStatus) IsValid() bool {
	return status == SkipCleanupSucceeded || status == SkipCleanupNotRequired
}

// SkipCount returns the number of confirmed skipped executions for a phase
// and subphase. It is derived from history so a later successful retry cannot
// erase an earlier waiver.
func (record PhaseRecord) SkipCount() int {
	if record.Skip == nil {
		return 0
	}
	return 1
}

// SkipCount returns the sticky number of confirmed skipped executions for the
// selected phase and subphase.
func (state ProjectState) SkipCount(phase, subphase string) int {
	count := 0
	for _, record := range state.PhaseHistory {
		if record.Phase == phase && record.Subphase == subphase {
			count += record.SkipCount()
		}
	}
	return count
}

// ExecutionOutcome is the durable, machine-readable result of one phase
// process. Log and metadata paths are relative to the project state root when
// possible, making the record portable across application restarts.
type ExecutionOutcome struct {
	Runtime               string        `json:"runtime,omitempty"`
	Model                 string        `json:"model,omitempty"`
	Effort                string        `json:"effort,omitempty"`
	ExitCode              int           `json:"exitCode"`
	StartedAt             time.Time     `json:"startedAt"`
	FinishedAt            time.Time     `json:"finishedAt"`
	Duration              time.Duration `json:"duration"`
	LogPath               string        `json:"logPath,omitempty"`
	MetadataPath          string        `json:"metadataPath,omitempty"`
	Canceled              bool          `json:"canceled,omitempty"`
	DevelopmentBaseCommit string        `json:"developmentBaseCommit,omitempty"`
	// TokensUsed is the agent-reported total token count for this execution;
	// zero when the agent did not report usage.
	TokensUsed int64 `json:"tokensUsed,omitempty"`
	// CostUSD is the agent-reported cost of this execution in US dollars
	// (claude reports total_cost_usd); zero when the agent does not report
	// cost — gg never estimates.
	CostUSD float64 `json:"costUsd,omitempty"`
	// Error is the human-readable failure reason for a failed execution, so
	// attached screens can explain the failure without reading log files.
	Error string `json:"error,omitempty"`
	// ExternalIdentity retains an unavoidable GitOps resource identity, such as
	// a pull request URL, when the execution later fails or is skipped.
	ExternalIdentity string `json:"externalIdentity,omitempty"`
	// DeferredChecks carries validated remote-only checks into durable history.
	DeferredChecks []proof.DeferredCheck `json:"deferredChecks,omitempty"`
}

// InterviewQA is one answered grooming question.
type InterviewQA struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

// InterviewState is the durable grooming interview progress: questions still
// waiting for the user and the clarifications already collected.
type InterviewState struct {
	Pending        []string      `json:"pending,omitempty"`
	Clarifications []InterviewQA `json:"clarifications,omitempty"`
	Rounds         int           `json:"rounds,omitempty"`
	Done           bool          `json:"done"`
}

// PlanState mirrors the machine-readable implementation-phase list from the
// planning artifact and the phases development has completed, so plan
// progress is visible without parsing worktree artifacts.
type PlanState struct {
	Phases    []string `json:"phases,omitempty"`
	Completed []string `json:"completed,omitempty"`
}

// ProjectState is the durable, versioned state for one project.
type ProjectState struct {
	SchemaVersion      int                    `json:"schemaVersion"`
	Name               string                 `json:"name"`
	Slug               string                 `json:"slug"`
	OriginalGoal       string                 `json:"originalGoal"`
	AcceptanceCriteria []string               `json:"acceptanceCriteria"`
	PipelineConfig     PipelineConfigSnapshot `json:"pipelineConfig"`
	CurrentPhase       string                 `json:"currentPhase"`
	CurrentSubphase    string                 `json:"currentSubphase,omitempty"`
	Status             LifecycleStatus        `json:"status"`
	// Terminal records the explicit terminal reason when a later integration
	// observes one. It is optional for backwards-compatible legacy state.
	Terminal *TerminalState `json:"terminal,omitempty"`
	// Interview persists the grooming interview so a session that exits
	// mid-interview re-enters it on the next attach. It is optional for
	// backwards-compatible legacy state (nil means no interview is owed).
	Interview *InterviewState `json:"interview,omitempty"`
	// Plan mirrors the planning artifact's phase list and development's
	// completion marks. It is optional display data for legacy state.
	Plan *PlanState `json:"plan,omitempty"`
	// Verification is the durable executable verification contract and its
	// later lifecycle cursor. It is absent in legacy project state until
	// Planning successfully declares the contract.
	Verification *VerificationState `json:"verification,omitempty"`
	WorktreePath string             `json:"worktreePath"`
	BranchName   string             `json:"branchName"`
	// GitDisabled marks projects whose configured folder is not a git
	// repository: they execute directly in that folder (no worktree, no
	// branch) and every git-dependent behavior — commit enforcement, proof
	// uncommitted checks, and the rebase/PR/CI phases — is skipped.
	GitDisabled  bool          `json:"gitDisabled,omitempty"`
	PhaseHistory []PhaseRecord `json:"phaseHistory,omitempty"`
	// PhaseConfigurationWarnings are sticky, phase-scoped notices explaining a
	// legacy tuple repair. They are removed only when that phase is explicitly
	// edited by the user.
	PhaseConfigurationWarnings map[string]string `json:"phaseConfigurationWarnings,omitempty"`
	ArtifactPaths              []string          `json:"artifactPaths,omitempty"`
	// DeferredChecks is the normalized project handoff for later PR disclosure.
	DeferredChecks []proof.DeferredCheck `json:"deferredChecks,omitempty"`
	// PullRequestURL is the created PR identity used by later GitOps phases.
	// It is optional so legacy state continues to use branch fallback.
	PullRequestURL  string    `json:"pullRequestUrl,omitempty"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
	StatusChangedAt time.Time `json:"statusChangedAt"`
	// RunReservationToken identifies the reservation that currently owns a
	// running state. It is optional for compatibility with legacy state files.
	RunReservationToken string `json:"runReservationToken,omitempty"`
	// ActiveRunID identifies the process-owned execution currently using the
	// project. Stop requests use it to reject stale requests from an older run.
	ActiveRunID string `json:"activeRunId,omitempty"`
	// RunOwnerPID is the OS process that owns the running state. Observers
	// use it to distinguish a live run from one left behind by a dead
	// process. Zero means unknown (legacy state).
	RunOwnerPID int `json:"runOwnerPid,omitempty"`
	// DispatchClaimRunID is the durable launch linearization point for the
	// current phase. A later stop request is ordered after this claim; the
	// phase result clears it.
	DispatchClaimRunID string `json:"dispatchClaimRunId,omitempty"`
	// StopRequested is a durable cancellation signal consumed by the active
	// runner. It is cleared by the next run or by terminal bookkeeping.
	StopRequested bool   `json:"stopRequested,omitempty"`
	StopRequestID string `json:"stopRequestId,omitempty"`
	// MaxQAAttempts and the QA loop cursor make the bounded feedback loop
	// restart-safe. They are optional for compatibility with existing state.
	MaxQAAttempts           int      `json:"maxQaAttempts,omitempty"`
	QACompletedAttempts     int      `json:"qaCompletedAttempts,omitempty"`
	QALoopStage             string   `json:"qaLoopStage,omitempty"`
	QAFeedbackArtifactPaths []string `json:"qaFeedbackArtifactPaths,omitempty"`
	// QAFixNextSubphase is the next configured Development fix subphase. It is
	// advanced after every durable fix result so resume never replays a
	// successful subphase.
	QAFixNextSubphase string `json:"qaFixNextSubphase,omitempty"`
	// PendingRebaseConflict distinguishes an actual persisted unmerged-index
	// failure from an ordinary Rebase phase failure.
	PendingRebaseConflict       bool     `json:"pendingRebaseConflict,omitempty"`
	RebaseConflictArtifactPaths []string `json:"rebaseConflictArtifactPaths,omitempty"`
	// PostRebaseContinuationPhase is recorded only after post-conflict QA has
	// succeeded. It bridges the restart window before the next phase starts.
	PostRebaseContinuationPhase string `json:"postRebaseContinuationPhase,omitempty"`
	// PRCIMonitor is the restart-safe cursor and terminal outcome for the
	// post-pipeline pull-request lifecycle monitor.
	PRCIMonitor *PRCIMonitorState `json:"prCiMonitor,omitempty"`
}

// PRCIMonitorState is provider-neutral. Cursor is an opaque provider cursor;
// Result is the last normalized observation.
type PRCIMonitorState struct {
	Cursor string `json:"cursor,omitempty"`
	Result string `json:"result,omitempty"`
	// LastRemediation is retained for decoding snapshots written before the
	// durable collection was introduced. New writes also include every key.
	LastRemediation string    `json:"lastRemediation,omitempty"`
	RemediationKeys []string  `json:"remediationKeys,omitempty"`
	Terminal        bool      `json:"terminal,omitempty"`
	Failed          bool      `json:"failed,omitempty"`
	Error           string    `json:"error,omitempty"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

// NewProjectState validates and returns a project state suitable for persistence.
// It copies caller-owned slices before returning so the persisted model is not
// accidentally changed through the constructor's input.
func NewProjectState(input ProjectState) (ProjectState, error) {
	state := input
	state.AcceptanceCriteria = append([]string(nil), input.AcceptanceCriteria...)
	state.PhaseHistory = append([]PhaseRecord(nil), input.PhaseHistory...)
	if input.PhaseConfigurationWarnings != nil {
		state.PhaseConfigurationWarnings = make(map[string]string, len(input.PhaseConfigurationWarnings))
		for phase, warning := range input.PhaseConfigurationWarnings {
			state.PhaseConfigurationWarnings[phase] = warning
		}
	}
	state.ArtifactPaths = append([]string(nil), input.ArtifactPaths...)
	state.DeferredChecks = append([]proof.DeferredCheck(nil), input.DeferredChecks...)
	state.QAFeedbackArtifactPaths = append([]string(nil), input.QAFeedbackArtifactPaths...)
	state.RebaseConflictArtifactPaths = append([]string(nil), input.RebaseConflictArtifactPaths...)
	state.PipelineConfig.Data = append(json.RawMessage(nil), input.PipelineConfig.Data...)
	if input.Verification != nil {
		verification := *input.Verification
		verification.PlannedSteps = cloneVerificationSteps(input.Verification.PlannedSteps)
		verification.ParentBaseline = cloneVerificationFindings(input.Verification.ParentBaseline)
		verification.ParentResults = cloneVerificationResults(input.Verification.ParentResults)
		verification.CurrentResults = cloneVerificationResults(input.Verification.CurrentResults)
		verification.CurrentFindings = cloneVerificationFindings(input.Verification.CurrentFindings)
		verification.Warnings = cloneVerificationFindings(input.Verification.Warnings)
		verification.PromotedRequiredGreen = append([]string(nil), input.Verification.PromotedRequiredGreen...)
		state.Verification = &verification
	}
	if input.PRCIMonitor != nil {
		monitor := *input.PRCIMonitor
		monitor.RemediationKeys = append([]string(nil), input.PRCIMonitor.RemediationKeys...)
		state.PRCIMonitor = &monitor
	}
	if input.Interview != nil {
		interview := *input.Interview
		interview.Pending = append([]string(nil), input.Interview.Pending...)
		interview.Clarifications = append([]InterviewQA(nil), input.Interview.Clarifications...)
		state.Interview = &interview
	}
	if input.Plan != nil {
		plan := PlanState{
			Phases:    append([]string(nil), input.Plan.Phases...),
			Completed: append([]string(nil), input.Plan.Completed...),
		}
		state.Plan = &plan
	}
	for i := range state.PhaseHistory {
		state.PhaseHistory[i].ArtifactPaths = append([]string(nil), input.PhaseHistory[i].ArtifactPaths...)
		state.PhaseHistory[i].DeferredChecks = append([]proof.DeferredCheck(nil), input.PhaseHistory[i].DeferredChecks...)
		if input.PhaseHistory[i].Outcome != nil {
			outcome := *input.PhaseHistory[i].Outcome
			outcome.DeferredChecks = append([]proof.DeferredCheck(nil), input.PhaseHistory[i].Outcome.DeferredChecks...)
			state.PhaseHistory[i].Outcome = &outcome
		}
		if input.PhaseHistory[i].Skip != nil {
			skip := *input.PhaseHistory[i].Skip
			skip.Cleanup.Evidence = append([]string(nil), input.PhaseHistory[i].Skip.Cleanup.Evidence...)
			state.PhaseHistory[i].Skip = &skip
		}
	}
	if state.SchemaVersion == 0 {
		state.SchemaVersion = CurrentSchemaVersion
	}
	if err := state.Validate(); err != nil {
		return ProjectState{}, err
	}
	return state, nil
}

// Validate checks the invariants required before a state can be persisted.
func (state ProjectState) Validate() error {
	if state.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("unsupported state schema version %d", state.SchemaVersion)
	}
	if state.Name == "" {
		return errors.New("project name is required")
	}
	if !validSlug(state.Slug) {
		return fmt.Errorf("invalid project slug %q", state.Slug)
	}
	if strings.TrimSpace(state.OriginalGoal) == "" {
		return errors.New("original goal is required")
	}
	if len(state.AcceptanceCriteria) == 0 {
		return errors.New("at least one acceptance criterion is required")
	}
	for i, criterion := range state.AcceptanceCriteria {
		if strings.TrimSpace(criterion) == "" {
			return fmt.Errorf("acceptance criterion %d is empty", i)
		}
	}
	if state.PipelineConfig.SchemaVersion <= 0 || !json.Valid(state.PipelineConfig.Data) {
		return errors.New("pipeline configuration snapshot must contain valid versioned JSON")
	}
	if strings.TrimSpace(state.CurrentPhase) == "" {
		return errors.New("current phase is required")
	}
	if !state.Status.IsValid() {
		return fmt.Errorf("invalid lifecycle status %q", state.Status)
	}
	if state.Terminal != nil {
		if !state.Terminal.Kind.IsValid() || state.Terminal.Kind == TerminalNone {
			return fmt.Errorf("invalid terminal kind %q", state.Terminal.Kind)
		}
		if !state.Status.IsTerminal() {
			return errors.New("terminal marker requires a terminal lifecycle status")
		}
		if state.Terminal.At.IsZero() {
			return errors.New("terminal marker requires a timestamp")
		}
	}
	if strings.TrimSpace(state.WorktreePath) == "" {
		return errors.New("worktree path is required")
	}
	if strings.TrimSpace(state.BranchName) == "" && !state.GitDisabled {
		// Git-disabled projects run directly in a non-git folder and own no
		// branch; every other project must name its worktree branch.
		return errors.New("branch name is required")
	}
	if state.CreatedAt.IsZero() || state.UpdatedAt.IsZero() || state.StatusChangedAt.IsZero() {
		return errors.New("created, updated, and status-changed timestamps are required")
	}
	if state.UpdatedAt.Before(state.CreatedAt) {
		return errors.New("updated timestamp precedes created timestamp")
	}
	occurrences := make(map[string]struct{}, len(state.PhaseHistory))
	for i, phase := range state.PhaseHistory {
		if strings.TrimSpace(phase.Phase) == "" || !phase.Status.IsValid() || phase.StartedAt.IsZero() {
			return fmt.Errorf("invalid phase history entry %d", i)
		}
		if phase.OccurrenceID != "" {
			if _, exists := occurrences[phase.OccurrenceID]; exists {
				return fmt.Errorf("duplicate phase occurrence ID %q", phase.OccurrenceID)
			}
			occurrences[phase.OccurrenceID] = struct{}{}
		}
		if phase.Skip != nil {
			if phase.OccurrenceID == "" {
				return fmt.Errorf("skipped phase history entry %d has no occurrence ID", i)
			}
			if phase.Status != StatusFailed || phase.CompletedAt == nil {
				return fmt.Errorf("skipped phase history entry %d is not a completed failure", i)
			}
			if phase.Skip.ConfirmedAt.IsZero() || !phase.Skip.Cleanup.Status.IsValid() {
				return fmt.Errorf("invalid skip resolution in phase history entry %d", i)
			}
			if phase.Skip.NextPhase == "" && phase.Skip.NextSubphase != "" {
				return fmt.Errorf("skip resolution entry %d has a subphase without a phase", i)
			}
		}
		for j, check := range phase.DeferredChecks {
			if err := check.Validate(); err != nil {
				return fmt.Errorf("invalid deferred check %d in phase history entry %d: %w", j+1, i, err)
			}
		}
		if phase.Outcome != nil {
			for j, check := range phase.Outcome.DeferredChecks {
				if err := check.Validate(); err != nil {
					return fmt.Errorf("invalid deferred check %d in phase outcome %d: %w", j+1, i, err)
				}
			}
		}
	}
	for phase, warning := range state.PhaseConfigurationWarnings {
		if strings.TrimSpace(phase) == "" || strings.TrimSpace(warning) == "" {
			return fmt.Errorf("invalid phase configuration warning for %q", phase)
		}
	}
	for i, check := range state.DeferredChecks {
		if err := check.Validate(); err != nil {
			return fmt.Errorf("invalid project deferred check %d: %w", i+1, err)
		}
	}
	if state.MaxQAAttempts < 0 || state.QACompletedAttempts < 0 {
		return errors.New("QA attempt counts cannot be negative")
	}
	if state.QACompletedAttempts > state.MaxQAAttempts {
		return errors.New("completed QA attempts exceed configured maximum")
	}
	if state.Verification != nil {
		contract := VerificationContract{
			Steps:      state.Verification.PlannedSteps,
			RepairMode: state.Verification.RepairMode,
		}
		if err := contract.Validate(); err != nil {
			return fmt.Errorf("verification contract: %w", err)
		}
		if state.Verification.RemediationAttempts < 0 || state.Verification.RemediationAttempts > MaxVerificationRemediationAttempts {
			return errors.New("verification remediation attempts are outside the configured maximum")
		}
		if err := validateVerificationFindings(state.Verification.ParentBaseline, "parent baseline"); err != nil {
			return err
		}
		if err := validateVerificationResults(state.Verification.ParentResults, "parent results"); err != nil {
			return err
		}
		if err := validateVerificationResults(state.Verification.CurrentResults, "current results"); err != nil {
			return err
		}
		if err := validateVerificationFindings(state.Verification.CurrentFindings, "current findings"); err != nil {
			return err
		}
		if err := validateVerificationFindings(state.Verification.Warnings, "warnings"); err != nil {
			return err
		}
		seen := make(map[string]struct{}, len(state.Verification.PromotedRequiredGreen))
		for _, identity := range state.Verification.PromotedRequiredGreen {
			identity = strings.TrimSpace(identity)
			if identity == "" {
				return errors.New("promoted required-green identity cannot be empty")
			}
			if _, exists := seen[identity]; exists {
				return fmt.Errorf("promoted required-green identity %q is duplicated", identity)
			}
			seen[identity] = struct{}{}
		}
	}
	switch state.QALoopStage {
	case "":
		if state.QACompletedAttempts != 0 {
			return errors.New("completed QA attempts require a loop stage")
		}
		if state.QAFixNextSubphase != "" {
			return errors.New("QA fix cursor requires the fix loop stage")
		}
	case "qa":
		if state.MaxQAAttempts == 0 {
			return errors.New("QA loop stage requires a configured maximum")
		}
		if state.QACompletedAttempts >= state.MaxQAAttempts {
			return errors.New("QA stage requires attempts remaining")
		}
		if state.QAFixNextSubphase != "" {
			return errors.New("QA stage cannot retain a fix subphase cursor")
		}
	case "fix":
		if state.MaxQAAttempts == 0 {
			return errors.New("QA loop stage requires a configured maximum")
		}
		if state.QACompletedAttempts == 0 || state.QACompletedAttempts >= state.MaxQAAttempts {
			return errors.New("QA fix stage requires a failed attempt and attempts remaining")
		}
		if strings.TrimSpace(state.QAFixNextSubphase) == "" {
			return errors.New("QA fix stage requires the next Development subphase")
		}
	case "exhausted":
		if state.MaxQAAttempts == 0 || state.QACompletedAttempts != state.MaxQAAttempts {
			return errors.New("exhausted QA stage requires all configured attempts")
		}
		if state.QAFixNextSubphase != "" {
			return errors.New("exhausted QA stage cannot retain a fix subphase cursor")
		}
	default:
		return fmt.Errorf("invalid QA loop stage %q", state.QALoopStage)
	}
	if state.PendingRebaseConflict && state.PostRebaseContinuationPhase != "" {
		return errors.New("pending rebase conflict cannot have a post-QA continuation")
	}
	if state.PostRebaseContinuationPhase == "rebase" {
		return errors.New("post-Rebase continuation cannot point to Rebase")
	}
	if state.RunReservationToken != "" && state.ActiveRunID != "" {
		return errors.New("run reservation and active run identity are mutually exclusive")
	}
	return nil
}

func validSlug(slug string) bool {
	if slug == "" || strings.HasPrefix(slug, "-") || strings.HasSuffix(slug, "-") || strings.Contains(slug, "--") {
		return false
	}
	for _, character := range slug {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
			return false
		}
	}
	return true
}

func cloneVerificationSteps(steps []VerificationStep) []VerificationStep {
	if steps == nil {
		return nil
	}
	cloned := make([]VerificationStep, len(steps))
	for index, step := range steps {
		cloned[index] = step
		cloned[index].Args = append([]string(nil), step.Args...)
		if step.Env != nil {
			cloned[index].Env = make(map[string]string, len(step.Env))
			for key, value := range step.Env {
				cloned[index].Env[key] = value
			}
		}
	}
	return cloned
}

func cloneVerificationFindings(findings []VerificationFinding) []VerificationFinding {
	return append([]VerificationFinding(nil), findings...)
}

func cloneVerificationResults(results []VerificationCommandResult) []VerificationCommandResult {
	if results == nil {
		return nil
	}
	cloned := make([]VerificationCommandResult, len(results))
	for index, result := range results {
		cloned[index] = result
		cloned[index].Args = append([]string(nil), result.Args...)
		cloned[index].Failures = cloneVerificationFindings(result.Failures)
	}
	return cloned
}

func validateVerificationFindings(findings []VerificationFinding, label string) error {
	for index, finding := range findings {
		if strings.TrimSpace(finding.CheckName) == "" {
			return fmt.Errorf("verification %s finding %d requires a check name", label, index)
		}
	}
	return nil
}

func validateVerificationResults(results []VerificationCommandResult, label string) error {
	seen := make(map[string]struct{}, len(results))
	for index, result := range results {
		if strings.TrimSpace(result.CheckName) == "" {
			return fmt.Errorf("verification %s result %d requires a check name", label, index)
		}
		if _, exists := seen[result.CheckName]; exists {
			return fmt.Errorf("verification %s contains duplicate check %q", label, result.CheckName)
		}
		seen[result.CheckName] = struct{}{}
		if strings.TrimSpace(result.Status) == "" {
			return fmt.Errorf("verification %s result %q requires a status", label, result.CheckName)
		}
		if result.RetryCount < 0 {
			return fmt.Errorf("verification %s result %q has negative retry count", label, result.CheckName)
		}
		if err := validateVerificationFindings(result.Failures, label+" failures"); err != nil {
			return err
		}
	}
	return nil
}
