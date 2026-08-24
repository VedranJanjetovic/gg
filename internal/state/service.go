// Package state owns durable project lifecycle state.
package state

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

type AgentStatus struct{ Name, Status string }
type Status struct{ Summary string }

type Clock interface{ Now() time.Time }
type realClock struct{}

func (realClock) Now() time.Time { return time.Now().UTC() }

type LifecycleService struct {
	store  Store
	clock  Clock
	locker ProjectLocker
}

// RunReservation is an owned reservation of a project for pipeline dispatch.
// The project is persisted as running before the lock is released, so terminal
// cleanup cannot remove its worktree between validation and dispatch.
type RunReservation struct {
	service  *LifecycleService
	slug     string
	previous ProjectState
	reserved ProjectState
}

// Rollback restores the state that preceded the reservation when no other
// lifecycle operation has changed the reserved running state.
func (r *RunReservation) Rollback(ctx context.Context) error {
	if r == nil || r.service == nil {
		return nil
	}
	return r.service.rollbackRun(ctx, r)
}

func NewLifecycleService(store Store, clock Clock, locker ProjectLocker) *LifecycleService {
	if clock == nil {
		clock = realClock{}
	}
	return &LifecycleService{store: store, clock: clock, locker: locker}
}
func NewProjectService(store Store, clock Clock, locker ProjectLocker) *LifecycleService {
	return NewLifecycleService(store, clock, locker)
}
func NewProjectStateService(store Store, clock Clock, locker ProjectLocker) *LifecycleService {
	return NewLifecycleService(store, clock, locker)
}

var ErrProjectExists = errors.New("project already exists")
var ErrRunNotActive = errors.New("project run is not active")
var ErrStaleStopRequest = errors.New("stop request does not match the active run")
var ErrStopRequested = errors.New("project run stop requested")

// RecoverIfStale converts a running project into a resumable stopped project
// when its owning process is no longer alive. Projects owned by a live
// process — including this one — are returned unchanged, so observers can
// call this safely on every refresh. A zero owner PID (legacy state) is
// treated as stale: no still-supported gg version leaves live runs without an
// owner record.
func (s *LifecycleService) RecoverIfStale(ctx context.Context, slug string) (ProjectState, bool, error) {
	if err := checkContext(ctx); err != nil {
		return ProjectState{}, false, err
	}
	if s.store == nil || s.locker == nil {
		return ProjectState{}, false, errors.New("lifecycle service requires store and locker")
	}
	project, err := s.store.Load(ctx, slug)
	if err != nil {
		return ProjectState{}, false, err
	}
	if project.Status != StatusRunning {
		return project, false, nil
	}
	if project.RunOwnerPID != 0 && processAlive(project.RunOwnerPID) {
		return project, false, nil
	}
	return s.RecoverStaleRun(ctx, slug)
}

// RewindForFeedback records user feedback as an acceptance criterion and
// moves the resume cursor so the next resume re-runs the pipeline from
// fromPhase ("pipeline" re-runs everything). Prior artifacts and history are
// preserved; QA loop cursors and run ownership are cleared.
// ReopenInterviewForBlockers re-opens the grooming interview with a blocked
// phase's open questions pending and rewinds the resume cursor to the
// pipeline start, so the next attach interviews the user and the rerun
// carries the answers. Run-status bookkeeping stays with the failing run.
func (s *LifecycleService) ReopenInterviewForBlockers(ctx context.Context, slug string, questions []string) (ProjectState, error) {
	if err := checkContext(ctx); err != nil {
		return ProjectState{}, err
	}
	var result ProjectState
	err := s.withProjectLock(ctx, slug, func(locked context.Context) error {
		project, err := s.store.Load(locked, slug)
		if err != nil {
			return err
		}
		if project.Interview == nil {
			project.Interview = &InterviewState{}
		}
		project.Interview.Done = false
		project.Interview.Rounds = 0
		for _, question := range questions {
			question = strings.TrimSpace(question)
			exists := question == ""
			for _, pending := range project.Interview.Pending {
				if pending == question {
					exists = true
					break
				}
			}
			if !exists {
				project.Interview.Pending = append(project.Interview.Pending, question)
			}
		}
		project.CurrentPhase, project.CurrentSubphase = "pipeline", ""
		project.UpdatedAt = s.clock.Now()
		if err := s.store.Save(locked, project); err != nil {
			return err
		}
		result = project
		return nil
	})
	return result, err
}

// BeginFeedbackLoop records user feedback and rewinds the project for a
// feedback rerun: the feedback becomes an acceptance criterion and an
// interview clarification, the grooming interview re-opens so the next attach
// interviews the user about the feedback with full project knowledge, and the
// resume cursor moves to the pipeline start so acceptance criteria and
// grooming re-run. The plan state is deliberately untouched — planning
// UPDATES the existing plan and development re-runs only pending plan phases,
// so completed work is never redone.
func (s *LifecycleService) BeginFeedbackLoop(ctx context.Context, slug, feedback string) (ProjectState, error) {
	if err := checkContext(ctx); err != nil {
		return ProjectState{}, err
	}
	if s.store == nil || s.locker == nil {
		return ProjectState{}, errors.New("lifecycle service requires store and locker")
	}
	feedback = strings.TrimSpace(feedback)
	if feedback == "" {
		return ProjectState{}, errors.New("feedback is required")
	}
	var result ProjectState
	err := s.withProjectLock(ctx, slug, func(locked context.Context) error {
		project, err := s.store.Load(locked, slug)
		if err != nil {
			return err
		}
		if project.Status == StatusRunning {
			return errors.New("stop the running pipeline before recording feedback")
		}
		now := s.clock.Now()
		project.AcceptanceCriteria = append(project.AcceptanceCriteria, "User feedback — "+feedback)
		if project.Interview == nil {
			project.Interview = &InterviewState{}
		}
		project.Interview.Done = false
		project.Interview.Rounds = 0
		project.Interview.Clarifications = append(project.Interview.Clarifications, InterviewQA{Question: "User feedback (drives this rerun)", Answer: feedback})
		project.Status, project.StatusChangedAt, project.UpdatedAt = StatusStopped, now, now
		// The pipeline cursor: resume starts at the first phase; no synthetic
		// stopped-phase record is appended, so the progress view keeps
		// showing the real history instead of a phantom stopped phase.
		project.CurrentPhase, project.CurrentSubphase = "pipeline", ""
		project.Terminal = nil
		project.QALoopStage, project.QAFixNextSubphase = "", ""
		project.QACompletedAttempts = 0
		project.QAFeedbackArtifactPaths = nil
		project.RunOwnerPID = 0
		project.ActiveRunID, project.RunReservationToken = "", ""
		project.StopRequested, project.StopRequestID = false, ""
		if err := s.store.Save(locked, project); err != nil {
			return err
		}
		result = project
		return nil
	})
	return result, err
}

// RecoverStaleRun deterministically converts a running project left behind by
// a dead process into a resumable stopped project. It preserves the durable
// phase cursor and execution snapshot and clears all old ownership markers.
func (s *LifecycleService) RecoverStaleRun(ctx context.Context, slug string) (ProjectState, bool, error) {
	if err := checkContext(ctx); err != nil {
		return ProjectState{}, false, err
	}
	var result ProjectState
	recovered := false
	err := s.withProjectLock(ctx, slug, func(locked context.Context) error {
		project, err := s.store.Load(locked, slug)
		if err != nil {
			return err
		}
		if project.Status != StatusRunning {
			result = project
			return nil
		}
		now := s.clock.Now()
		project.Status, project.StatusChangedAt, project.UpdatedAt = StatusStopped, now, now
		project.RunReservationToken, project.ActiveRunID = "", ""
		project.DispatchClaimRunID, project.StopRequestID = "", ""
		project.StopRequested = false
		project.RunOwnerPID = 0
		if err := s.store.Save(locked, project); err != nil {
			return err
		}
		result, recovered = project, true
		return nil
	})
	return result, recovered, err
}

// SetInterview atomically persists grooming interview progress and appends
// any clarification-derived acceptance criteria in the same locked save, so
// answers are never recorded without becoming part of the requirements.
func (s *LifecycleService) SetInterview(ctx context.Context, slug string, interview InterviewState, appendCriteria []string) (ProjectState, error) {
	if err := checkContext(ctx); err != nil {
		return ProjectState{}, err
	}
	if s.store == nil || s.locker == nil {
		return ProjectState{}, errors.New("lifecycle service requires store and locker")
	}
	var result ProjectState
	err := s.withProjectLock(ctx, slug, func(locked context.Context) error {
		project, err := s.store.Load(locked, slug)
		if err != nil {
			return err
		}
		copied := interview
		copied.Pending = append([]string(nil), interview.Pending...)
		copied.Clarifications = append([]InterviewQA(nil), interview.Clarifications...)
		project.Interview = &copied
		project.AcceptanceCriteria = append(project.AcceptanceCriteria, appendCriteria...)
		project.UpdatedAt = s.clock.Now()
		if err := s.store.Save(locked, project); err != nil {
			return err
		}
		result = project
		return nil
	})
	return result, err
}

// RecordPlan atomically mirrors the plan's phase list and completion marks
// into project state. A non-empty phases argument replaces the stored list
// (planning re-runs redefine the plan); completed names merge without
// duplicates so development subphases can report progress incrementally.
func (s *LifecycleService) RecordPlan(ctx context.Context, slug string, phases, completed []string) (ProjectState, error) {
	if err := checkContext(ctx); err != nil {
		return ProjectState{}, err
	}
	if s.store == nil || s.locker == nil {
		return ProjectState{}, errors.New("lifecycle service requires store and locker")
	}
	var result ProjectState
	err := s.withProjectLock(ctx, slug, func(locked context.Context) error {
		project, err := s.store.Load(locked, slug)
		if err != nil {
			return err
		}
		plan := PlanState{}
		if project.Plan != nil {
			plan.Phases = append([]string(nil), project.Plan.Phases...)
			plan.Completed = append([]string(nil), project.Plan.Completed...)
		}
		if len(phases) > 0 {
			plan.Phases = append([]string(nil), phases...)
		}
		for _, name := range completed {
			seen := false
			for _, existing := range plan.Completed {
				if existing == name {
					seen = true
					break
				}
			}
			if !seen {
				plan.Completed = append(plan.Completed, name)
			}
		}
		project.Plan = &plan
		project.UpdatedAt = s.clock.Now()
		if err := s.store.Save(locked, project); err != nil {
			return err
		}
		result = project
		return nil
	})
	return result, err
}

func (s *LifecycleService) Create(ctx context.Context, input ProjectState) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if s.store == nil || s.locker == nil {
		return errors.New("lifecycle service requires store and locker")
	}
	now := s.clock.Now()
	if input.SchemaVersion == 0 {
		input.SchemaVersion = CurrentSchemaVersion
	}
	if input.Status == "" {
		input.Status = StatusPending
	}
	if input.CreatedAt.IsZero() {
		input.CreatedAt = now
	}
	if input.UpdatedAt.IsZero() {
		input.UpdatedAt = now
	}
	if input.StatusChangedAt.IsZero() {
		input.StatusChangedAt = input.CreatedAt
	}
	if len(input.PhaseHistory) == 0 {
		input.PhaseHistory = []PhaseRecord{{Phase: input.CurrentPhase, Subphase: input.CurrentSubphase, Status: input.Status, StartedAt: input.CreatedAt}}
	}
	return s.withProjectLock(ctx, input.Slug, func(locked context.Context) error {
		if _, err := s.store.Load(locked, input.Slug); err == nil {
			return fmt.Errorf("create project %q: %w", input.Slug, ErrProjectExists)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("check project %q: %w", input.Slug, err)
		}
		return s.store.Save(locked, input)
	})
}
func (s *LifecycleService) Load(ctx context.Context, slug string) (ProjectState, error) {
	if s.store == nil {
		return ProjectState{}, errors.New("lifecycle service requires store")
	}
	return s.store.Load(ctx, slug)
}
func (s *LifecycleService) Save(ctx context.Context, state ProjectState) error {
	if s.store == nil {
		return errors.New("lifecycle service requires store")
	}
	// Public saves are lifecycle mutations too. Never let callers preserve
	// or inject an active reservation token while writing running state.
	if state.Status == StatusRunning {
		state.RunReservationToken = ""
	}
	if s.locker == nil {
		// Preserve compatibility with stores that already own their locking.
		return s.store.Save(ctx, state)
	}
	return s.withProjectLock(ctx, state.Slug, func(locked context.Context) error {
		return s.store.Save(locked, state)
	})
}
func (s *LifecycleService) List(ctx context.Context) ([]ProjectState, error) {
	if s.store == nil {
		return nil, errors.New("lifecycle service requires store")
	}
	return s.store.List(ctx)
}

func (s *LifecycleService) Delete(ctx context.Context, slug string) error {
	if s.store == nil {
		return errors.New("lifecycle service requires store")
	}
	return s.store.Delete(ctx, slug)
}

// WithProjectLock reloads a project while holding its lock and invokes fn with
// the current state. It is the transaction seam for lifecycle operations that
// must coordinate durable state with external resources.
func (s *LifecycleService) WithProjectLock(ctx context.Context, slug string, fn func(context.Context, ProjectState) error) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if s.store == nil || s.locker == nil {
		return errors.New("lifecycle service requires store and locker")
	}
	return s.withProjectLock(ctx, slug, func(locked context.Context) error {
		project, err := s.store.Load(locked, slug)
		if err != nil {
			return err
		}
		return fn(locked, project)
	})
}

// ReserveRun validates and transitions a project to running in one locked
// transaction. validate runs while the project lock is held, but the lock is
// released before callers dispatch long-running external work.
func (s *LifecycleService) ReserveRun(ctx context.Context, slug string, validate func(context.Context, ProjectState) error) (ProjectState, *RunReservation, error) {
	return s.reserveRun(ctx, slug, nil, 0, validate)
}

// ReserveRunWithSnapshot atomically stores the exact execution snapshot and
// QA attempt maximum while acquiring run ownership. No observer can see a new
// reservation paired with stale execution configuration.
func (s *LifecycleService) ReserveRunWithSnapshot(ctx context.Context, slug string, snapshot PipelineConfigSnapshot, maxQAAttempts int, validate func(context.Context, ProjectState) error) (ProjectState, *RunReservation, error) {
	return s.reserveRun(ctx, slug, &snapshot, maxQAAttempts, validate)
}

func (s *LifecycleService) reserveRun(ctx context.Context, slug string, snapshot *PipelineConfigSnapshot, maxQAAttempts int, validate func(context.Context, ProjectState) error) (ProjectState, *RunReservation, error) {
	if err := checkContext(ctx); err != nil {
		return ProjectState{}, nil, err
	}
	if s.store == nil || s.locker == nil {
		return ProjectState{}, nil, errors.New("lifecycle service requires store and locker")
	}
	var previous, reserved ProjectState
	err := s.withProjectLock(ctx, slug, func(locked context.Context) error {
		current, err := s.store.Load(locked, slug)
		if err != nil {
			return err
		}
		if snapshot != nil {
			if snapshot.SchemaVersion <= 0 || len(snapshot.Data) == 0 {
				return errors.New("pipeline execution snapshot is required")
			}
			if maxQAAttempts <= 0 {
				return errors.New("maximum QA attempts must be positive")
			}
			if current.MaxQAAttempts != 0 &&
				current.QACompletedAttempts > 0 &&
				current.MaxQAAttempts != maxQAAttempts {
				return errors.New("cannot change maximum QA attempts while a QA loop is in progress")
			}
		}
		if validate != nil {
			if err := validate(locked, current); err != nil {
				return err
			}
		}
		if current.Status == StatusRunning || !canTransition(current.Status, StatusRunning) {
			return fmt.Errorf("invalid lifecycle transition %s -> %s", current.Status, StatusRunning)
		}
		previous = current
		now := s.clock.Now()
		reservationToken, err := newRunReservationToken()
		if err != nil {
			return fmt.Errorf("create run reservation token: %w", err)
		}
		current.ActiveRunID = ""
		current.StopRequested = false
		current.StopRequestID = ""
		current.Status, current.StatusChangedAt = StatusRunning, now
		current.RunOwnerPID = os.Getpid()
		if snapshot != nil {
			current.PipelineConfig.SchemaVersion = snapshot.SchemaVersion
			current.PipelineConfig.Data = append(current.PipelineConfig.Data[:0], snapshot.Data...)
			current.MaxQAAttempts = maxQAAttempts
		}
		current.UpdatedAt = now
		current.RunReservationToken = reservationToken
		if err := s.store.Save(locked, current); err != nil {
			return err
		}
		reserved = current
		return nil
	})
	if err != nil {
		return ProjectState{}, nil, err
	}
	return reserved, &RunReservation{service: s, slug: slug, previous: previous, reserved: reserved}, nil
}

// ReactivateFinished changes a terminal successful project to running for the
// narrow finished-project GitOps rerun path. It is intentionally separate from
// generic lifecycle transitions so ordinary callers cannot restart all phases.
func (s *LifecycleService) ReactivateFinished(ctx context.Context, slug string) (ProjectState, error) {
	if err := checkContext(ctx); err != nil {
		return ProjectState{}, err
	}
	if s.store == nil || s.locker == nil {
		return ProjectState{}, errors.New("lifecycle service requires store and locker")
	}
	var result ProjectState
	err := s.withProjectLock(ctx, slug, func(locked context.Context) error {
		project, err := s.store.Load(locked, slug)
		if err != nil {
			return err
		}
		if project.Status != StatusFinished {
			return fmt.Errorf("invalid finished-project rerun status %s", project.Status)
		}
		project.Status = StatusRunning
		project.StatusChangedAt, project.UpdatedAt = s.clock.Now(), s.clock.Now()
		project.ActiveRunID, project.RunReservationToken = "", ""
		project.RunOwnerPID = os.Getpid()
		if err := s.store.Save(locked, project); err != nil {
			return err
		}
		result = project
		return nil
	})
	return result, err
}

// BeginRun records the process identity after reservation. A stop request for
// this exact run is retained; a request for an older run is discarded.
func (s *LifecycleService) BeginRun(ctx context.Context, slug, runID, reservationToken string) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if runID == "" {
		return errors.New("run ID is required")
	}
	return s.withProjectLock(ctx, slug, func(locked context.Context) error {
		project, err := s.store.Load(locked, slug)
		if err != nil {
			return err
		}
		if project.Status != StatusRunning {
			return fmt.Errorf("%w: status is %s", ErrRunNotActive, project.Status)
		}
		if project.ActiveRunID != "" && project.ActiveRunID != runID {
			return fmt.Errorf("project %q already has active run %q: %w", slug, project.ActiveRunID, ErrRunNotActive)
		}
		if project.RunReservationToken != reservationToken {
			return fmt.Errorf("project %q run reservation does not match: %w", slug, ErrRunNotActive)
		}
		if project.RunReservationToken == "" && project.ActiveRunID != "" {
			return fmt.Errorf("project %q has no claimable run reservation: %w", slug, ErrRunNotActive)
		}
		project.ActiveRunID = runID
		project.RunReservationToken = ""
		if project.StopRequested && project.StopRequestID != "" && project.StopRequestID != runID {
			project.StopRequested = false
			project.StopRequestID = ""
		}
		project.UpdatedAt = s.clock.Now()
		return s.store.Save(locked, project)
	})
}

// RequestStop atomically records a stop signal for the active run. Repeating
// the same request is idempotent; a request for another run cannot stop it.
func (s *LifecycleService) RequestStop(ctx context.Context, slug, runID string) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	return s.withProjectLock(ctx, slug, func(locked context.Context) error {
		project, err := s.store.Load(locked, slug)
		if err != nil {
			return err
		}
		if project.Status != StatusRunning {
			return fmt.Errorf("%w: status is %s", ErrRunNotActive, project.Status)
		}
		if project.ActiveRunID == "" {
			return fmt.Errorf("%w: no active run identity", ErrRunNotActive)
		}
		if runID == "" {
			runID = project.ActiveRunID
		}
		if project.ActiveRunID != runID {
			return ErrStaleStopRequest
		}
		if project.StopRequested && project.StopRequestID == runID {
			return nil
		}
		project.StopRequested = true
		project.StopRequestID = runID
		project.UpdatedAt = s.clock.Now()
		return s.store.Save(locked, project)
	})
}

// CancelRunReservation stops a project whose owner died after reserving the
// project but before claiming an active run ID. The reservation token is the
// ownership fence: once it is cleared, a stale owner cannot call BeginRun.
// Phase history is deliberately left unchanged because no phase was dispatched.
func (s *LifecycleService) CancelRunReservation(ctx context.Context, slug string) (bool, error) {
	if err := checkContext(ctx); err != nil {
		return false, err
	}
	canceled := false
	err := s.withProjectLock(ctx, slug, func(locked context.Context) error {
		project, err := s.store.Load(locked, slug)
		if err != nil {
			return err
		}
		if project.RunReservationToken == "" {
			return nil
		}
		if project.Status != StatusRunning || project.ActiveRunID != "" {
			return fmt.Errorf("project %q has an invalid run reservation state", slug)
		}
		now := s.clock.Now()
		project.Status = StatusStopped
		project.StatusChangedAt = now
		project.UpdatedAt = now
		project.RunReservationToken = ""
		project.DispatchClaimRunID = ""
		project.StopRequested = false
		project.StopRequestID = ""
		if err := s.store.Save(locked, project); err != nil {
			return err
		}
		canceled = true
		return nil
	})
	return canceled, err
}

// ClaimDispatch atomically checks that the active run may dispatch its next phase.
// A persisted stop request wins over any later asynchronous watcher poll.
func (s *LifecycleService) ClaimDispatch(ctx context.Context, slug, runID string) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	return s.withProjectLock(ctx, slug, func(locked context.Context) error {
		project, err := s.store.Load(locked, slug)
		if err != nil {
			return err
		}
		if project.Status != StatusRunning || project.ActiveRunID != runID {
			return fmt.Errorf("%w: run %q is not active (active %q, status %s)", ErrRunNotActive, runID, project.ActiveRunID, project.Status)
		}
		if project.StopRequested && project.StopRequestID == runID {
			return ErrStopRequested
		}
		// Persisting the claim while holding the same inter-process lock used by
		// RequestStop establishes the launch linearization point. A stop that
		// acquires the lock afterwards is ordered after this phase's launch.
		project.DispatchClaimRunID = runID
		project.UpdatedAt = s.clock.Now()
		if err := s.store.Save(locked, project); err != nil {
			return fmt.Errorf("persist dispatch claim: %w", err)
		}
		return nil
	})
}

// StopRequested reports the durable stop signal for an active run.
func (s *LifecycleService) StopRequested(ctx context.Context, slug, runID string) (bool, error) {
	project, err := s.Load(ctx, slug)
	if err != nil {
		return false, err
	}
	if !project.StopRequested {
		return false, nil
	}
	if project.StopRequestID != "" && project.StopRequestID != runID {
		return false, nil
	}
	return true, nil
}

func (s *LifecycleService) rollbackRun(ctx context.Context, reservation *RunReservation) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	return s.withProjectLock(ctx, reservation.slug, func(locked context.Context) error {
		current, err := s.store.Load(locked, reservation.slug)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if current.Status != StatusRunning || current.RunReservationToken == "" || current.RunReservationToken != reservation.reserved.RunReservationToken {
			return nil
		}
		// Restoring a snapshot must not resurrect its reservation ownership.
		// Otherwise an older reservation could roll back again after this
		// reservation has already relinquished ownership.
		previous := reservation.previous
		previous.RunReservationToken = ""
		return s.store.Save(locked, previous)
	})
}

func newRunReservationToken() (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(token[:]), nil
}

// PruneProject atomically reloads a project, rechecks terminal state, cleans
// its external resources, and deletes durable state while holding its lock.
// Cleanup must be safe to retry; state is deleted only after it succeeds.
func (s *LifecycleService) PruneProject(ctx context.Context, slug string, cleanup func(context.Context, ProjectState) error) error {
	return s.WithProjectLock(ctx, slug, func(locked context.Context, project ProjectState) error {
		// Any parked project may be pruned when the user explicitly selects
		// it (failed, stopped, pending, or terminal); only a live run is
		// protected.
		if project.Status == StatusRunning {
			return nil
		}
		if err := cleanup(locked, project); err != nil {
			return err
		}
		return s.store.Delete(locked, slug)
	})
}

// CloseRun transitions a running project to failed or stopped without adding a
// second phase-history record after the phase result is already complete.
func (s *LifecycleService) CloseRun(ctx context.Context, slug string, target LifecycleStatus) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if target != StatusFailed && target != StatusStopped {
		return fmt.Errorf("invalid run close status %q", target)
	}
	return s.withProjectLock(ctx, slug, func(locked context.Context) error {
		current, err := s.store.Load(locked, slug)
		if err != nil {
			return err
		}
		now := s.clock.Now()
		if current.Status != target {
			current.Status, current.StatusChangedAt = target, now
		}
		current.UpdatedAt = now
		current.RunReservationToken = ""
		current.DispatchClaimRunID = ""
		current.StopRequested = false
		current.StopRequestID = ""
		current.ActiveRunID = ""
		return s.store.Save(locked, current)
	})
}

// ConfigureOrchestration persists run-level limits before any phase dispatch.
// Existing QA progress is retained so resume cannot reset an exhausted budget.
func (s *LifecycleService) ConfigureOrchestration(ctx context.Context, slug string, maxQAAttempts int) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if maxQAAttempts <= 0 {
		return errors.New("maximum QA attempts must be positive")
	}
	return s.withProjectLock(ctx, slug, func(locked context.Context) error {
		project, err := s.store.Load(locked, slug)
		if err != nil {
			return err
		}
		if project.MaxQAAttempts != 0 && project.QACompletedAttempts > 0 && project.MaxQAAttempts != maxQAAttempts {
			return fmt.Errorf("cannot change maximum QA attempts while a QA loop is in progress")
		}
		project.MaxQAAttempts = maxQAAttempts
		project.UpdatedAt = s.clock.Now()
		return s.store.Save(locked, project)
	})
}

// UpdateQALoop persists the exact next QA/fix cursor and consumed budget.
func (s *LifecycleService) UpdateQALoop(ctx context.Context, slug string, completed int, stage string, feedback []string) (ProjectState, error) {
	return s.updateQALoop(ctx, slug, completed, stage, "", feedback)
}

// UpdateQALoopWithFixCursor atomically enters or advances a fix stage with
// the exact next Development subphase.
func (s *LifecycleService) UpdateQALoopWithFixCursor(ctx context.Context, slug string, completed int, stage, fixSubphase string, feedback []string) (ProjectState, error) {
	return s.updateQALoop(ctx, slug, completed, stage, fixSubphase, feedback)
}

func (s *LifecycleService) updateQALoop(ctx context.Context, slug string, completed int, stage, fixSubphase string, feedback []string) (ProjectState, error) {
	if err := checkContext(ctx); err != nil {
		return ProjectState{}, err
	}
	if completed < 0 {
		return ProjectState{}, errors.New("completed QA attempts cannot be negative")
	}
	switch stage {
	case "qa", "fix", "exhausted":
	default:
		return ProjectState{}, fmt.Errorf("invalid QA loop stage %q", stage)
	}
	var result ProjectState
	err := s.withProjectLock(ctx, slug, func(locked context.Context) error {
		project, err := s.store.Load(locked, slug)
		if err != nil {
			return err
		}
		if project.MaxQAAttempts <= 0 {
			return errors.New("maximum QA attempts are not configured")
		}
		if completed > project.MaxQAAttempts {
			return errors.New("completed QA attempts exceed configured maximum")
		}
		project.QACompletedAttempts = completed
		project.QALoopStage = stage
		if stage == "fix" {
			if fixSubphase == "" {
				return errors.New("QA fix stage requires the next Development subphase")
			}
			project.QAFixNextSubphase = fixSubphase
		} else {
			project.QAFixNextSubphase = ""
		}
		project.QAFeedbackArtifactPaths = appendUnique(project.QAFeedbackArtifactPaths, feedback...)
		project.ArtifactPaths = appendUnique(project.ArtifactPaths, feedback...)
		project.UpdatedAt = s.clock.Now()
		if err := s.store.Save(locked, project); err != nil {
			return err
		}
		result = project
		return nil
	})
	return result, err
}

// SetQAFixNextSubphase advances the durable fix cursor after each successful
// Development subphase.
func (s *LifecycleService) SetQAFixNextSubphase(ctx context.Context, slug, subphase string) (ProjectState, error) {
	var result ProjectState
	err := s.withProjectLock(ctx, slug, func(locked context.Context) error {
		project, err := s.store.Load(locked, slug)
		if err != nil {
			return err
		}
		if project.QALoopStage != "fix" {
			return fmt.Errorf("QA fix cursor requires fix stage, got %q", project.QALoopStage)
		}
		if subphase == "" {
			return errors.New("next QA fix subphase is required")
		}
		project.QAFixNextSubphase = subphase
		project.UpdatedAt = s.clock.Now()
		if err := s.store.Save(locked, project); err != nil {
			return err
		}
		result = project
		return nil
	})
	return result, err
}

// ResetQALoop clears completed-loop state after a successful QA disposition.
func (s *LifecycleService) ResetQALoop(ctx context.Context, slug string) (ProjectState, error) {
	var result ProjectState
	err := s.withProjectLock(ctx, slug, func(locked context.Context) error {
		project, err := s.store.Load(locked, slug)
		if err != nil {
			return err
		}
		project.QACompletedAttempts = 0
		project.QALoopStage = ""
		project.QAFeedbackArtifactPaths = nil
		project.QAFixNextSubphase = ""
		project.UpdatedAt = s.clock.Now()
		if err := s.store.Save(locked, project); err != nil {
			return err
		}
		result = project
		return nil
	})
	return result, err
}

// SetRebaseConflict records or clears a known unmerged-index failure.
func (s *LifecycleService) SetRebaseConflict(ctx context.Context, slug string, pending bool, artifacts []string) (ProjectState, error) {
	var result ProjectState
	err := s.withProjectLock(ctx, slug, func(locked context.Context) error {
		project, err := s.store.Load(locked, slug)
		if err != nil {
			return err
		}
		project.PendingRebaseConflict = pending
		if pending {
			project.PostRebaseContinuationPhase = ""
			project.RebaseConflictArtifactPaths = appendUnique(project.RebaseConflictArtifactPaths, artifacts...)
			project.ArtifactPaths = appendUnique(project.ArtifactPaths, artifacts...)
		} else {
			project.RebaseConflictArtifactPaths = nil
		}
		project.UpdatedAt = s.clock.Now()
		if err := s.store.Save(locked, project); err != nil {
			return err
		}
		result = project
		return nil
	})
	return result, err
}

// SetPullRequestURL persists the external PR identity created during a run so
// a later phase, including a restarted resume, can monitor the same PR.
func (s *LifecycleService) SetPullRequestURL(ctx context.Context, slug, url string) (ProjectState, error) {
	if url == "" {
		return ProjectState{}, errors.New("pull request URL is required")
	}
	var result ProjectState
	err := s.withProjectLock(ctx, slug, func(locked context.Context) error {
		project, err := s.store.Load(locked, slug)
		if err != nil {
			return err
		}
		project.PullRequestURL = url
		project.UpdatedAt = s.clock.Now()
		if err := s.store.Save(locked, project); err != nil {
			return err
		}
		result = project
		return nil
	})
	return result, err
}

// CompleteRebaseConflict records the phase that follows Rebase at the same
// durable boundary that clears the conflict. A restart observes either the
// pending conflict or the exact continuation, never an ambiguous gap.
func (s *LifecycleService) CompleteRebaseConflict(ctx context.Context, slug, nextPhase string) (ProjectState, error) {
	if nextPhase == "" {
		return ProjectState{}, errors.New("post-Rebase continuation phase is required")
	}
	var result ProjectState
	err := s.withProjectLock(ctx, slug, func(locked context.Context) error {
		project, err := s.store.Load(locked, slug)
		if err != nil {
			return err
		}
		if !project.PendingRebaseConflict {
			return errors.New("project has no pending rebase conflict")
		}
		project.PendingRebaseConflict = false
		project.RebaseConflictArtifactPaths = nil
		project.PostRebaseContinuationPhase = nextPhase
		project.UpdatedAt = s.clock.Now()
		if err := s.store.Save(locked, project); err != nil {
			return err
		}
		result = project
		return nil
	})
	return result, err
}

func (s *LifecycleService) Transition(ctx context.Context, slug string, target LifecycleStatus, phase, subphase string, artifacts []string) (ProjectState, error) {
	if err := checkContext(ctx); err != nil {
		return ProjectState{}, err
	}
	if !target.IsValid() {
		return ProjectState{}, fmt.Errorf("invalid lifecycle status %q", target)
	}
	if s.store == nil || s.locker == nil {
		return ProjectState{}, errors.New("lifecycle service requires store and locker")
	}
	var result ProjectState
	err := s.withProjectLock(ctx, slug, func(locked context.Context) error {
		state, err := s.store.Load(locked, slug)
		if err != nil {
			return err
		}
		if !canTransition(state.Status, target) {
			return fmt.Errorf("invalid lifecycle transition %s -> %s", state.Status, target)
		}
		now := s.clock.Now()
		if phase == "" {
			phase = state.CurrentPhase
			if subphase == "" {
				subphase = state.CurrentSubphase
			}
		}
		occurrenceID := ""
		if target == StatusRunning {
			var occurrenceErr error
			occurrenceID, occurrenceErr = newOccurrenceID()
			if occurrenceErr != nil {
				return fmt.Errorf("create phase occurrence ID: %w", occurrenceErr)
			}
		}
		state.CurrentPhase, state.CurrentSubphase = phase, subphase
		state.PhaseHistory = updatePhaseHistory(state.PhaseHistory, phase, subphase, target, now, artifacts, occurrenceID)
		state.ArtifactPaths = appendUnique(state.ArtifactPaths, artifacts...)
		if state.Status != target {
			state.Status, state.StatusChangedAt = target, now
		}
		state.UpdatedAt = now
		// Transition is a lifecycle mutation, including same-status active
		// transitions. It invalidates any reservation that may have preceded it.
		state.RunReservationToken = ""
		state.DispatchClaimRunID = ""
		if target != StatusRunning {
			state.StopRequested = false
			state.StopRequestID = ""
			state.ActiveRunID = ""
		}
		if err := s.store.Save(locked, state); err != nil {
			return err
		}
		result = state
		return nil
	})
	return result, err
}
func (s *LifecycleService) IsActive(status LifecycleStatus) bool { return status.IsActive() }

// SetStatus is a concise compatibility wrapper for status-only transitions.
func (s *LifecycleService) SetStatus(ctx context.Context, slug string, target LifecycleStatus, phase, subphase string) (ProjectState, error) {
	return s.Transition(ctx, slug, target, phase, subphase, nil)
}

// TransitionStatus is an explicit alias for Transition.
func (s *LifecycleService) TransitionStatus(ctx context.Context, slug string, target LifecycleStatus, phase, subphase string, artifacts []string) (ProjectState, error) {
	return s.Transition(ctx, slug, target, phase, subphase, artifacts)
}

// RecordPhase persists a phase-level transition without changing the
// project's overall lifecycle status. Phase records use running for started,
// finished for succeeded, and failed for unsuccessful execution.
func (s *LifecycleService) RecordPhase(ctx context.Context, slug, phase, subphase string, status LifecycleStatus, outcome *ExecutionOutcome, artifacts []string) (ProjectState, error) {
	if err := checkContext(ctx); err != nil {
		return ProjectState{}, err
	}
	if status != StatusRunning && status != StatusFinished && status != StatusFailed && status != StatusStopped {
		return ProjectState{}, fmt.Errorf("invalid phase status %q", status)
	}
	if s.store == nil || s.locker == nil {
		return ProjectState{}, errors.New("lifecycle service requires store and locker")
	}
	var result ProjectState
	err := s.withProjectLock(ctx, slug, func(locked context.Context) error {
		current, err := s.store.Load(locked, slug)
		if err != nil {
			return err
		}
		now := s.clock.Now()
		if phase == "" {
			phase = current.CurrentPhase
			if subphase == "" {
				subphase = current.CurrentSubphase
			}
		}
		occurrenceID := ""
		if status == StatusRunning || status == StatusFailed || status == StatusFinished || status == StatusStopped {
			var occurrenceErr error
			occurrenceID, occurrenceErr = newOccurrenceID()
			if occurrenceErr != nil {
				return fmt.Errorf("create phase occurrence ID: %w", occurrenceErr)
			}
		}
		current.CurrentPhase, current.CurrentSubphase = phase, subphase
		if status == StatusRunning && current.PostRebaseContinuationPhase == phase {
			current.PostRebaseContinuationPhase = ""
		}
		current.PhaseHistory = updatePhaseHistory(current.PhaseHistory, phase, subphase, status, now, artifacts, occurrenceID)
		current.ArtifactPaths = appendUnique(current.ArtifactPaths, artifacts...)
		if outcome != nil && len(current.PhaseHistory) > 0 {
			copy := *outcome
			current.PhaseHistory[len(current.PhaseHistory)-1].Outcome = &copy
		}
		current.UpdatedAt = now
		current.RunReservationToken = ""
		current.DispatchClaimRunID = ""
		if err := s.store.Save(locked, current); err != nil {
			return err
		}
		result = current
		return nil
	})
	return result, err
}

// RecordExecution is retained as a compatibility wrapper for callers that
// record process metadata without changing the phase status.
// RecordExecution attaches process metadata while preserving the compatibility
// behavior that the project remains running.
func (s *LifecycleService) RecordExecution(ctx context.Context, slug, phase, subphase string, outcome ExecutionOutcome, artifacts []string) (ProjectState, error) {
	return s.RecordPhase(ctx, slug, phase, subphase, StatusRunning, &outcome, artifacts)
}
func (s *LifecycleService) Active(ctx context.Context) ([]ProjectState, error) {
	states, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	active := states[:0]
	for _, state := range states {
		if state.Status.IsActive() {
			active = append(active, state)
		}
	}
	return active, nil
}

// Terminal returns projects that have reached an irreversible lifecycle state.
func (s *LifecycleService) Terminal(ctx context.Context) ([]ProjectState, error) {
	states, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	terminal := states[:0]
	for _, project := range states {
		if project.Status.IsTerminal() {
			terminal = append(terminal, project)
		}
	}
	return terminal, nil
}

func (s *LifecycleService) withProjectLock(ctx context.Context, slug string, fn func(context.Context) error) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	lock, err := s.locker.Lock(ctx, slug)
	if err != nil {
		return err
	}
	defer lock.Close()
	return fn(context.WithValue(ctx, projectLockContextKey{}, slug))
}

type projectLockContextKey struct{}

func projectLockHeld(ctx context.Context, slug string) bool {
	value, ok := ctx.Value(projectLockContextKey{}).(string)
	return ok && value == slug
}
func canTransition(from, to LifecycleStatus) bool {
	if from == to {
		return from.IsActive()
	}
	switch from {
	case StatusPending:
		return to == StatusRunning || to == StatusTerminated
	case StatusRunning:
		return to == StatusStopped || to == StatusFailed || to == StatusFinished || to == StatusTerminated
	case StatusStopped, StatusFailed:
		return to == StatusRunning || to == StatusTerminated
	default:
		return false
	}
}
func updatePhaseHistory(history []PhaseRecord, phase, subphase string, status LifecycleStatus, now time.Time, artifacts []string, occurrenceID string) []PhaseRecord {
	history = append([]PhaseRecord(nil), history...)
	if len(history) == 0 {
		return append(history, PhaseRecord{Phase: phase, Subphase: subphase, Status: status, StartedAt: now, CompletedAt: completionTime(status, now), ArtifactPaths: appendUnique(nil, artifacts...), OccurrenceID: occurrenceID})
	}
	last := &history[len(history)-1]
	if last.Phase != phase || last.Subphase != subphase || last.CompletedAt != nil {
		completed := now
		last.CompletedAt = &completed
		return append(history, PhaseRecord{Phase: phase, Subphase: subphase, Status: status, StartedAt: now, CompletedAt: completionTime(status, now), ArtifactPaths: appendUnique(nil, artifacts...), OccurrenceID: occurrenceID})
	}
	last.Status, last.ArtifactPaths = status, appendUnique(last.ArtifactPaths, artifacts...)
	if last.OccurrenceID == "" {
		last.OccurrenceID = occurrenceID
	}
	last.CompletedAt = completionTime(status, now)
	return history
}
func completionTime(status LifecycleStatus, now time.Time) *time.Time {
	if status == StatusStopped || status == StatusFailed || status == StatusFinished || status == StatusTerminated {
		copy := now
		return &copy
	}
	return nil
}
func appendUnique(existing []string, additions ...string) []string {
	result := append([]string(nil), existing...)
	for _, addition := range additions {
		if addition == "" {
			continue
		}
		found := false
		for _, value := range result {
			if value == addition {
				found = true
				break
			}
		}
		if !found {
			result = append(result, addition)
		}
	}
	return result
}

type Service struct{}

func NewService() *Service { return &Service{} }
func (s *Service) List(ctx context.Context) ([]AgentStatus, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	return nil, nil
}
func (s *Service) Status(ctx context.Context) (Status, error) {
	if err := checkContext(ctx); err != nil {
		return Status{}, err
	}
	return Status{Summary: "no active runs"}, nil
}

var _ ProjectService = (*LifecycleService)(nil)
