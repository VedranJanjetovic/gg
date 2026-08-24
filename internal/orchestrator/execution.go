package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/VedranJanjetovic/gg/internal/agent"
	"github.com/VedranJanjetovic/gg/internal/ci"
	"github.com/VedranJanjetovic/gg/internal/config"
	"github.com/VedranJanjetovic/gg/internal/git"
	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/pr"
	"github.com/VedranJanjetovic/gg/internal/state"
)

var errRebaseHasUnmergedPaths = errors.New("Rebase completed with unmerged Git paths")

type PhaseState interface {
	RecordPhase(context.Context, string, string, string, state.LifecycleStatus, *state.ExecutionOutcome, []string) (state.ProjectState, error)
}

// ResumeState is the narrow persistence seam needed to reload a stopped
// project and transition it back to running.
type ResumeState interface {
	PhaseState
	Load(context.Context, string) (state.ProjectState, error)
	Transition(context.Context, string, state.LifecycleStatus, string, string, []string) (state.ProjectState, error)
}

type durableStopState interface {
	BeginRun(context.Context, string, string, string) error
	RequestStop(context.Context, string, string) error
	StopRequested(context.Context, string, string) (bool, error)
}

type durableRunReservationCanceler interface {
	CancelRunReservation(context.Context, string) (bool, error)
}

type durableDispatchState interface {
	ClaimDispatch(context.Context, string, string) error
}

type durableRunCloser interface {
	CloseRun(context.Context, string, state.LifecycleStatus) error
}

type durableOrchestrationState interface {
	ConfigureOrchestration(context.Context, string, int) error
	UpdateQALoop(context.Context, string, int, string, []string) (state.ProjectState, error)
	ResetQALoop(context.Context, string) (state.ProjectState, error)
	SetRebaseConflict(context.Context, string, bool, []string) (state.ProjectState, error)
}

type durableRebaseCompletionState interface {
	CompleteRebaseConflict(context.Context, string, string) (state.ProjectState, error)
}

type durableQAFixCursorState interface {
	UpdateQALoopWithFixCursor(context.Context, string, int, string, string, []string) (state.ProjectState, error)
	SetQAFixNextSubphase(context.Context, string, string) (state.ProjectState, error)
}

type durablePullRequestIdentityState interface {
	SetPullRequestURL(context.Context, string, string) (state.ProjectState, error)
}

type durableResumeReserver interface {
	ReserveRun(context.Context, string, func(context.Context, state.ProjectState) error) (state.ProjectState, *state.RunReservation, error)
}

func (c *sequentialController) markProjectStopped(ctx context.Context, slug, phase, subphase string) error {
	lifecycle, ok := c.state.(interface {
		Transition(context.Context, string, state.LifecycleStatus, string, string, []string) (state.ProjectState, error)
	})
	if !ok {
		return nil
	}
	_, err := lifecycle.Transition(ctx, slug, state.StatusStopped, phase, subphase, nil)
	return err
}

type terminalConflictRouter struct{}

func (terminalConflictRouter) Route(context.Context, Conflict) (ConflictRoute, error) {
	// A conflict is unresolved unless an injected resolver explicitly says that
	// it is ready for QA. The safe default is terminal: never dispatch work on
	// the basis of an implicit conflict resolution.
	return ConflictRouteTerminal, nil
}

type gitConflictRouter struct{ git ConflictStateReader }

// NewConflictRouter creates the production conflict router. A resolved Git
// worktree is sent back through QA; a worktree with unmerged paths remains
// terminal. A nil reader preserves conservative compatibility behavior.
func NewConflictRouter(reader ConflictStateReader) ConflictRouter {
	return gitConflictRouter{git: reader}
}

func (r gitConflictRouter) Route(ctx context.Context, conflict Conflict) (ConflictRoute, error) {
	if r.git == nil || conflict.WorktreePath == "" {
		return ConflictRouteTerminal, nil
	}
	unresolved, err := r.git.HasUnresolvedConflicts(ctx, conflict.WorktreePath)
	if err != nil {
		return ConflictRouteTerminal, fmt.Errorf("inspect rebase conflict worktree: %w", err)
	}
	if unresolved {
		return ConflictRouteTerminal, nil
	}
	return ConflictRouteQA, nil
}

type ControllerOption func(*sequentialController)

func WithRunner(runner PhaseRunner) ControllerOption {
	return func(c *sequentialController) { c.runner = runner }
}
func WithPhaseState(store PhaseState) ControllerOption {
	return func(c *sequentialController) { c.state = store }
}
func WithEventSink(sink EventSink) ControllerOption {
	return func(c *sequentialController) { c.events = sink }
}

// WithCompletionNotificationSubscriber injects an independent completion
// consumer. Its failures are isolated from the durable lifecycle event stream.
func WithCompletionNotificationSubscriber(subscriber EventSink) ControllerOption {
	return func(c *sequentialController) { c.notifications = subscriber }
}
func WithPromptBuilder(builder agent.PromptBuilder) ControllerOption {
	return func(c *sequentialController) { c.prompts = builder }
}
func WithConflictRouter(router ConflictRouter) ControllerOption {
	return func(c *sequentialController) { c.conflicts = router }
}
func WithDevelopmentCommitVerifier(verifier DevelopmentCommitVerifier) ControllerOption {
	return func(c *sequentialController) { c.developmentCommits = verifier }
}
func WithConflictStateReader(reader ConflictStateReader) ControllerOption {
	return func(c *sequentialController) { c.conflictState = reader }
}
func WithGitOpsServices(rebaser GitOpsRebaser, pullRequests PullRequestService, checks CIService) ControllerOption {
	return func(c *sequentialController) { c.rebaser, c.pullRequests, c.checks = rebaser, pullRequests, checks }
}
func WithRebaseAgent(rebaseAgent RebaseAgent) ControllerOption {
	return func(c *sequentialController) { c.rebaseAgent = rebaseAgent }
}
func WithPRCILifecycleMonitor(monitor LifecycleMonitor) ControllerOption {
	return func(c *sequentialController) { c.lifecycleMonitor = monitor }
}

type sequentialController struct {
	runner             PhaseRunner
	state              PhaseState
	events             EventSink
	notifications      EventSink
	prompts            agent.PromptBuilder
	conflicts          ConflictRouter
	conflictState      ConflictStateReader
	developmentCommits DevelopmentCommitVerifier
	rebaser            GitOpsRebaser
	rebaseAgent        RebaseAgent
	pullRequests       PullRequestService
	checks             CIService
	lifecycleMonitor   LifecycleMonitor
	mu                 sync.Mutex
	active             map[string]context.CancelFunc
	requests           map[string]Request
}

// NewProductionController constructs the controller with the Git-backed conflict route used by the executable composition root.
func NewProductionController(runner PhaseRunner, state PhaseState, reader ConflictStateReader, options ...ControllerOption) Controller {
	options = append(options, WithRunner(runner), WithPhaseState(state), WithConflictRouter(NewConflictRouter(reader)), WithConflictStateReader(reader))
	if verifier, ok := reader.(DevelopmentCommitVerifier); ok {
		options = append(options, WithDevelopmentCommitVerifier(verifier))
	}
	return NewController(options...)
}

func NewController(options ...ControllerOption) Controller {
	controller := &sequentialController{prompts: agent.StandalonePromptBuilder{}, conflicts: terminalConflictRouter{}, active: make(map[string]context.CancelFunc), requests: make(map[string]Request)}
	for _, option := range options {
		option(controller)
	}
	return controller
}

// Execute runs enabled phases in canonical order. MaxIterations is the maximum
// total number of QA attempts; zero uses the product default of three.
func (c *sequentialController) Execute(ctx context.Context, request Request) ([]PhaseOutcome, error) {
	return c.execute(ctx, request, "", "", false, false)
}

func (c *sequentialController) execute(ctx context.Context, request Request, resumePhase, resumeSubphase string, resolvedConflict, finalizeOnly bool) (outcomes []PhaseOutcome, returnErr error) {
	if c == nil || c.runner == nil {
		return nil, errors.New("orchestrator phase runner is required")
	}
	if c.state == nil {
		return nil, errors.New("orchestrator phase state is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.prompts == nil {
		return nil, errors.New("orchestrator prompt builder is required")
	}
	if request.MaxIterations < 0 {
		return nil, errors.New("orchestrator max iterations cannot be negative")
	}
	maxAttempts := request.MaxIterations
	if maxAttempts == 0 {
		maxAttempts = 3
	}
	request.MaxIterations = maxAttempts

	runID := request.RunID
	if runID == "" {
		runID = fmt.Sprintf("%s-%d", request.Project.Slug, time.Now().UnixNano())
	}
	request.RunID = runID
	if request.PullRequestURL == "" {
		request.PullRequestURL = request.Project.PullRequestURL
	}
	ctx, cancel := context.WithCancel(ctx)
	if err := c.registerRun(runID, cancel, request); err != nil {
		cancel()
		return nil, err
	}
	defer c.unregisterRun(runID)
	var stopWatchDone chan struct{}
	ownsDurableRun := false
	if durable, ok := c.state.(durableStopState); ok {
		if err := durable.BeginRun(ctx, request.Project.Slug, runID, request.Project.RunReservationToken); err != nil {
			cancel()
			return nil, fmt.Errorf("begin durable run %q: %w", runID, err)
		}
		request.Project.RunReservationToken = ""
		ownsDurableRun = true
		defer func() {
			if ownsDurableRun && returnErr != nil {
				returnErr = c.closeFailedRun(request.Project.Slug, returnErr)
			}
		}()
		stopWatchDone = make(chan struct{})
		go c.watchDurableStop(ctx, cancel, durable, request.Project.Slug, runID, stopWatchDone)
		defer func() { cancel(); <-stopWatchDone }()
	}
	if durable, ok := c.state.(durableOrchestrationState); ok {
		if err := durable.ConfigureOrchestration(ctx, request.Project.Slug, maxAttempts); err != nil {
			cancel()
			return nil, fmt.Errorf("persist orchestration configuration: %w", err)
		}
		request.Project.MaxQAAttempts = maxAttempts
	}

	outcomes = make([]PhaseOutcome, 0, len(request.Pipeline.Phases()))
	if finalizeOnly {
		if err := c.finalizeProject(context.WithoutCancel(ctx), request.Project.Slug); err != nil {
			return outcomes, err
		}
		return outcomes, nil
	}
	if resolvedConflict {
		request.Project.ArtifactPaths = appendUnique(request.Project.ArtifactPaths, request.Project.RebaseConflictArtifactPaths...)
		qa, ok := qaExecutable(request.Pipeline)
		if !ok {
			return outcomes, errors.New("resolved rebase conflict requires an enabled QA phase")
		}
		qaOutcomes, err := c.executeQAFeedbackLoop(ctx, &request, qa, maxAttempts, "")
		outcomes = append(outcomes, qaOutcomes...)
		if err != nil {
			return outcomes, err
		}
		continuationTarget := pipeline.PhaseRebase
		if _, rebaseBeforeQA := rebaseBeforeQAExecutable(request.Pipeline); rebaseBeforeQA {
			continuationTarget = pipeline.PhaseQA
		}
		resumePhase, resumeSubphase = nextResumePhase(request.Pipeline, string(continuationTarget))
		if resumePhase == "" {
			return outcomes, errors.New("resolved rebase conflict has no phase after Rebase")
		}
		if durable, ok := c.state.(durableRebaseCompletionState); ok {
			project, persistErr := durable.CompleteRebaseConflict(context.WithoutCancel(ctx), request.Project.Slug, resumePhase)
			if persistErr != nil {
				return outcomes, fmt.Errorf("persist post-Rebase continuation: %w", persistErr)
			}
			request.Project = project
		}
	}
	resuming := resumePhase == ""
	for _, executable := range request.Pipeline.Phases() {
		phase := executable.Phase().ID()
		if !resuming {
			if string(phase) != resumePhase {
				continue
			}
			resuming = true
		}
		if phase == pipeline.PhasePlanning {
			planningOutcomes, err := c.executePlanningLoop(ctx, &request, executable)
			outcomes = append(outcomes, planningOutcomes...)
			request.PlanningRetry = nil
			if err != nil {
				return outcomes, err
			}
			continue
		}
		if phase == pipeline.PhaseQA {
			if err := ctx.Err(); err != nil {
				return outcomes, c.stopBeforeDispatch(request, phase, "", err)
			}
			qaOutcomes, err := c.executeQAFeedbackLoop(ctx, &request, executable, maxAttempts, resumeSubphase)
			outcomes = append(outcomes, qaOutcomes...)
			if err != nil {
				return outcomes, err
			}
			continue
		}
		if phase == pipeline.PhaseDevelopment {
			devOutcomes, err := c.executeDevelopmentLoop(ctx, &request, executable, resumePhase, resumeSubphase)
			outcomes = append(outcomes, devOutcomes...)
			resumePhase, resumeSubphase = "", ""
			if err != nil {
				return outcomes, err
			}
			continue
		}
		subphases, err := c.subphases(phase, request.Subphases)
		if err != nil {
			return outcomes, fmt.Errorf("generate %s subphases: %w", phase, err)
		}
		for _, subphase := range subphases {
			if resumePhase == string(phase) && resumeSubphase != "" && subphase != resumeSubphase {
				continue
			}
			if err := ctx.Err(); err != nil {
				return outcomes, c.stopBeforeDispatch(request, phase, subphase, err)
			}
			resumePhase, resumeSubphase = "", ""
			outcome, err := c.executePhase(ctx, request, executable, subphase, 0, nil)
			outcomes = append(outcomes, outcome)
			request.Project.ArtifactPaths = appendUnique(request.Project.ArtifactPaths, outcome.Result.ArtifactPaths...)
			if outcome.Result.ExternalIdentity != "" {
				request.PullRequestURL = outcome.Result.ExternalIdentity
				if durable, ok := c.state.(durablePullRequestIdentityState); ok {
					project, persistErr := durable.SetPullRequestURL(context.WithoutCancel(ctx), request.Project.Slug, request.PullRequestURL)
					if persistErr != nil {
						return outcomes, fmt.Errorf("persist pull request identity: %w", persistErr)
					}
					request.Project = project
				}
			}
			if err != nil && phase == pipeline.PhaseCI && request.GitOps.Configured && c.checks != nil {
				retryOutcomes, retryOutcome, retryErr := c.retryCIFailure(ctx, &request, executable, maxAttempts, outcome.Result.ArtifactPaths)
				outcomes = append(outcomes, retryOutcomes...)
				if retryErr == nil {
					outcome, err = retryOutcome, nil
					request.Project.ArtifactPaths = appendUnique(request.Project.ArtifactPaths, retryOutcome.Result.ArtifactPaths...)
				}
			}
			if err != nil {
				if phase == pipeline.PhaseRebase {
					if conflictErr := c.recordRebaseConflict(context.WithoutCancel(ctx), &request, &outcome, err); conflictErr != nil {
						return outcomes, errors.Join(err, conflictErr)
					}
					outcomes[len(outcomes)-1].ConflictResolutionNeeded = outcome.ConflictResolutionNeeded
				}
				return outcomes, err
			}
		}
	}
	// The final phase result and success event are already durable at this
	// point. Completion wins over cancellation arriving in the small window
	// before project finalization: use a non-cancelable context for the durable
	// status transition and its terminal event, while preserving real errors.
	monitor := request.PRCIMonitor
	if monitor == nil {
		monitor = c.lifecycleMonitor
	}
	if monitor != nil && request.PullRequestURL != "" {
		monitorResult, err := monitor.Monitor(context.WithoutCancel(ctx), PRCIRequest{ProjectSlug: request.Project.Slug, PullRequestURL: request.PullRequestURL, MaxPolls: maxAttempts})
		if err != nil {
			return outcomes, err
		}
		if monitorResult.Merged {
			if err := c.publish(context.WithoutCancel(ctx), Event{ProjectSlug: request.Project.Slug, Type: EventProjectFinished, At: time.Now().UTC()}); err != nil {
				return outcomes, fmt.Errorf("publish project finished: %w", err)
			}
			return outcomes, nil
		}
		// An open PR after bounded polling is resumable, not terminal. The
		// monitor has already persisted its cursor and idempotence state; only
		// a merged result is allowed to finish the project.
		return outcomes, nil
	}
	if err := c.finalizeProject(context.WithoutCancel(ctx), request.Project.Slug); err != nil {
		return outcomes, err
	}
	return outcomes, nil
}

func (c *sequentialController) finalizeProject(ctx context.Context, slug string) error {
	if err := c.markProjectFinished(ctx, slug); err != nil {
		return err
	}
	if err := c.publish(ctx, Event{ProjectSlug: slug, Type: EventProjectFinished, At: time.Now().UTC()}); err != nil {
		return fmt.Errorf("publish project finished: %w", err)
	}
	return nil
}

func (c *sequentialController) closeFailedRun(slug string, cause error) error {
	if cause == nil {
		return nil
	}
	closer, ok := c.state.(durableRunCloser)
	if !ok {
		return cause
	}
	target := state.StatusFailed
	if isCancellation(cause) {
		target = state.StatusStopped
	}
	if err := closer.CloseRun(context.Background(), slug, target); err != nil {
		return errors.Join(cause, fmt.Errorf("close project run: %w", err))
	}
	return cause
}

func (c *sequentialController) isRebaseConflict(ctx context.Context, worktree string) (bool, error) {
	if c.conflictState == nil {
		// Compatibility for custom routers. Production always classifies the
		// failed worktree using Git's unmerged index.
		return true, nil
	}
	unresolved, err := c.conflictState.HasUnresolvedConflicts(ctx, worktree)
	if err != nil {
		return false, fmt.Errorf("inspect failed Rebase worktree: %w", err)
	}
	return unresolved, nil
}

func (c *sequentialController) markProjectFinished(ctx context.Context, slug string) error {
	lifecycle, ok := c.state.(interface {
		Transition(context.Context, string, state.LifecycleStatus, string, string, []string) (state.ProjectState, error)
	})
	if !ok {
		return nil
	}
	if _, err := lifecycle.Transition(ctx, slug, state.StatusFinished, "", "", nil); err != nil {
		return fmt.Errorf("persist project %q finished: %w", slug, err)
	}
	return nil
}

func nextResumePhase(plan pipeline.ExecutablePipeline, current string) (string, string) {
	phases := plan.Phases()
	for i, phase := range phases {
		if string(phase.Phase().ID()) == current && i+1 < len(phases) {
			return string(phases[i+1].Phase().ID()), ""
		}
	}
	return "", ""
}

func (c *sequentialController) registerRun(id string, cancel context.CancelFunc, request Request) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.active[id]; exists {
		return fmt.Errorf("run %q is already active", id)
	}
	// requests is also the in-process resume cache, so it intentionally retains
	// completed runs. Only active IDs participate in the same-project guard.
	for activeID := range c.active {
		if saved, ok := c.requests[activeID]; ok && saved.Project.Slug == request.Project.Slug {
			return fmt.Errorf("project %q already has an active local run", request.Project.Slug)
		}
	}
	c.active[id] = cancel
	c.requests[id] = request
	return nil
}

func (c *sequentialController) unregisterRun(id string) {
	c.mu.Lock()
	delete(c.active, id)
	c.mu.Unlock()
}

func (c *sequentialController) executeQAFeedbackLoop(ctx context.Context, request *Request, executable pipeline.ExecutablePhase, maxAttempts int, resumeFixSubphase string) ([]PhaseOutcome, error) {
	var outcomes []PhaseOutcome
	completed := request.Project.QACompletedAttempts
	feedback := append([]string(nil), request.Project.QAFeedbackArtifactPaths...)
	stage := request.Project.QALoopStage
	if stage == "exhausted" || completed >= maxAttempts {
		return outcomes, fmt.Errorf("QA feedback loop exhausted after %d attempt(s)", maxAttempts)
	}
	if stage == "" {
		stage = "qa"
		if err := c.persistQALoop(ctx, request, completed, stage, "", feedback); err != nil {
			return outcomes, err
		}
	}

	runFixes := func(iteration int, resumeSubphase string) error {
		fixSubphases, err := c.subphases(pipeline.PhaseDevelopment, request.Subphases)
		if err != nil {
			return fmt.Errorf("generate Development fix subphases: %w", err)
		}
		development, ok := developmentExecutable(request.Pipeline)
		if !ok {
			return errors.New("QA feedback requires an enabled Development phase")
		}
		cursor := request.Project.QAFixNextSubphase
		if cursor == "" {
			cursor = resumeSubphase
		}
		if cursor == "" && len(fixSubphases) > 0 {
			cursor = fixSubphases[0]
			if err := c.persistQALoop(context.WithoutCancel(ctx), request, completed, "fix", cursor, feedback); err != nil {
				return err
			}
		}
		resuming := cursor != ""
		for index, subphase := range fixSubphases {
			if resuming && subphase != cursor {
				continue
			}
			resuming = false
			fixOutcome, fixErr := c.executePhase(ctx, *request, development, subphase, iteration, feedback)
			outcomes = append(outcomes, fixOutcome)
			request.Project.ArtifactPaths = appendUnique(request.Project.ArtifactPaths, fixOutcome.Result.ArtifactPaths...)
			if fixErr != nil {
				return fixErr
			}
			if index+1 < len(fixSubphases) {
				next := fixSubphases[index+1]
				if durable, ok := c.state.(durableQAFixCursorState); ok {
					project, cursorErr := durable.SetQAFixNextSubphase(context.WithoutCancel(ctx), request.Project.Slug, next)
					if cursorErr != nil {
						return fmt.Errorf("persist next QA fix subphase: %w", cursorErr)
					}
					request.Project = project
				} else {
					request.Project.QAFixNextSubphase = next
				}
			}
		}
		if resuming {
			return fmt.Errorf("resume Development fix subphase %q is not configured", cursor)
		}
		rebaseOutcome, rebaseErr := c.executeRebaseBeforeQA(ctx, request, iteration, feedback)
		if rebaseOutcome.Result.Phase != "" {
			outcomes = append(outcomes, rebaseOutcome)
		}
		if rebaseErr != nil {
			if conflictErr := c.recordRebaseConflict(context.WithoutCancel(ctx), request, &rebaseOutcome, rebaseErr); conflictErr != nil {
				if rebaseOutcome.Result.Phase != "" {
					outcomes[len(outcomes)-1] = rebaseOutcome
				}
				return errors.Join(rebaseErr, conflictErr)
			}
			if rebaseOutcome.Result.Phase != "" {
				outcomes[len(outcomes)-1] = rebaseOutcome
			}
			return rebaseErr
		}
		stage = "qa"
		return c.persistQALoop(context.WithoutCancel(ctx), request, completed, stage, "", feedback)
	}

	if stage == "fix" {
		if err := runFixes(completed, resumeFixSubphase); err != nil {
			return outcomes, err
		}
	}
	for completed < maxAttempts {
		if err := ctx.Err(); err != nil {
			return outcomes, err
		}
		attempt := completed + 1
		outcome, err := c.executePhase(ctx, *request, executable, "", attempt, feedback)
		outcomes = append(outcomes, outcome)
		request.Project.ArtifactPaths = appendUnique(request.Project.ArtifactPaths, outcome.Result.ArtifactPaths...)
		if err == nil {
			if durable, ok := c.state.(durableOrchestrationState); ok {
				project, resetErr := durable.ResetQALoop(ctx, request.Project.Slug)
				if resetErr != nil {
					return outcomes, fmt.Errorf("reset successful QA loop: %w", resetErr)
				}
				request.Project = project
			} else {
				request.Project.QACompletedAttempts = 0
				request.Project.QALoopStage = ""
				request.Project.QAFeedbackArtifactPaths = nil
			}
			return outcomes, nil
		}
		if isCancellation(err) {
			return outcomes, err
		}
		if (outcome.Result.Disposition != agent.DispositionFailed && outcome.Result.Disposition != agent.DispositionFeedback) || !outcome.retryableSemanticFailure {
			return outcomes, err
		}
		feedback = appendUnique(feedback, outcome.Result.ArtifactPaths...)
		completed = attempt
		stage = "fix"
		if completed >= maxAttempts {
			stage = "exhausted"
		}
		fixCursor := ""
		if stage == "fix" {
			fixSubphases, subphaseErr := c.subphases(pipeline.PhaseDevelopment, request.Subphases)
			if subphaseErr != nil || len(fixSubphases) == 0 {
				return outcomes, errors.Join(err, subphaseErr, errors.New("QA feedback requires at least one Development fix subphase"))
			}
			fixCursor = fixSubphases[0]
		}
		if persistErr := c.persistQALoop(context.WithoutCancel(ctx), request, completed, stage, fixCursor, feedback); persistErr != nil {
			return outcomes, errors.Join(err, persistErr)
		}
		outcome.FeedbackArtifactPaths = append([]string(nil), feedback...)
		if persistErr := c.publishFeedback(ctx, request.Project.Slug, outcome); persistErr != nil {
			return outcomes, errors.Join(err, persistErr)
		}
		if completed >= maxAttempts {
			return outcomes, fmt.Errorf("QA feedback loop exhausted after %d attempt(s): %w", maxAttempts, err)
		}
		if err := c.publish(ctx, Event{ProjectSlug: request.Project.Slug, Phase: pipeline.PhaseQA, Type: EventPhaseRetried, At: time.Now().UTC(), Outcome: &outcome}); err != nil {
			return outcomes, fmt.Errorf("publish QA retry: %w", err)
		}
		if err := runFixes(attempt, ""); err != nil {
			return outcomes, err
		}
	}
	return outcomes, fmt.Errorf("QA feedback loop exhausted after %d attempt(s)", maxAttempts)
}

func (c *sequentialController) persistQALoop(ctx context.Context, request *Request, completed int, stage, fixSubphase string, feedback []string) error {
	request.Project.QACompletedAttempts = completed
	request.Project.QALoopStage = stage
	request.Project.QAFixNextSubphase = fixSubphase
	request.Project.QAFeedbackArtifactPaths = append([]string(nil), feedback...)
	request.Project.ArtifactPaths = appendUnique(request.Project.ArtifactPaths, feedback...)
	if stage == "fix" {
		durable, ok := c.state.(durableQAFixCursorState)
		if !ok {
			return nil
		}
		project, err := durable.UpdateQALoopWithFixCursor(ctx, request.Project.Slug, completed, stage, fixSubphase, feedback)
		if err != nil {
			return fmt.Errorf("persist QA loop cursor: %w", err)
		}
		request.Project = project
		return nil
	}
	if durable, ok := c.state.(durableOrchestrationState); ok {
		project, err := durable.UpdateQALoop(ctx, request.Project.Slug, completed, stage, feedback)
		if err != nil {
			return fmt.Errorf("persist QA loop cursor: %w", err)
		}
		request.Project = project
	}
	return nil
}

func developmentExecutable(plan pipeline.ExecutablePipeline) (pipeline.ExecutablePhase, bool) {
	for _, phase := range plan.Phases() {
		if phase.Phase().ID() == pipeline.PhaseDevelopment {
			return phase, true
		}
	}
	return pipeline.ExecutablePhase{}, false
}

func qaExecutable(plan pipeline.ExecutablePipeline) (pipeline.ExecutablePhase, bool) {
	for _, phase := range plan.Phases() {
		if phase.Phase().ID() == pipeline.PhaseQA {
			return phase, true
		}
	}
	return pipeline.ExecutablePhase{}, false
}

// executeRebaseBeforeQA preserves the new pipeline invariant for feedback
// loops while leaving legacy snapshots with QA-before-Rebase semantics alone.
func (c *sequentialController) executeRebaseBeforeQA(ctx context.Context, request *Request, iteration int, feedback []string) (PhaseOutcome, error) {
	rebase, ok := rebaseBeforeQAExecutable(request.Pipeline)
	if !ok {
		return PhaseOutcome{}, nil
	}
	outcome, err := c.executePhase(ctx, *request, rebase, "", iteration, feedback)
	request.Project.ArtifactPaths = appendUnique(request.Project.ArtifactPaths, outcome.Result.ArtifactPaths...)
	return outcome, err
}

func (c *sequentialController) recordRebaseConflict(ctx context.Context, request *Request, outcome *PhaseOutcome, phaseErr error) error {
	if outcome == nil || outcome.Result.Status != state.StatusFailed {
		return nil
	}
	conflict, err := c.isRebaseConflict(ctx, request.Project.WorktreePath)
	if err != nil {
		return err
	}
	if !conflict {
		return nil
	}
	outcome.ConflictResolutionNeeded = true
	if durable, ok := c.state.(durableOrchestrationState); ok {
		project, persistErr := durable.SetRebaseConflict(ctx, request.Project.Slug, true, outcome.Result.ArtifactPaths)
		if persistErr != nil {
			return persistErr
		}
		request.Project = project
	}
	_, err = c.routeConflict(ctx, *request, *outcome, phaseErr)
	return err
}

func rebaseBeforeQAExecutable(plan pipeline.ExecutablePipeline) (pipeline.ExecutablePhase, bool) {
	phases := plan.Phases()
	for index, executable := range phases {
		if executable.Phase().ID() != pipeline.PhaseQA || index == 0 {
			continue
		}
		previous := phases[index-1]
		if previous.Phase().ID() == pipeline.PhaseRebase {
			return previous, true
		}
	}
	return pipeline.ExecutablePhase{}, false
}

func (c *sequentialController) routeConflict(ctx context.Context, request Request, outcome PhaseOutcome, phaseErr error) (bool, error) {
	conflict := Conflict{
		Phase:         pipeline.PhaseRebase,
		Subphase:      outcome.Result.Subphase,
		WorktreePath:  request.Project.WorktreePath,
		ArtifactPaths: append([]string(nil), outcome.Result.ArtifactPaths...),
		Err:           phaseErr,
	}
	outcome.ConflictResolutionNeeded = true
	if err := c.publish(ctx, Event{ProjectSlug: request.Project.Slug, Phase: pipeline.PhaseRebase, Subphase: outcome.Result.Subphase, Type: EventConflictDetected, At: time.Now().UTC(), Outcome: &outcome, Error: phaseErr}); err != nil {
		return false, fmt.Errorf("publish rebase conflict: %w", err)
	}
	router := c.conflicts
	if router == nil {
		router = terminalConflictRouter{}
	}
	route, err := router.Route(ctx, conflict)
	if err != nil {
		return false, fmt.Errorf("route rebase conflict: %w", err)
	}
	switch route {
	case ConflictRouteQA:
		return true, nil
	case ConflictRouteTerminal:
		return false, nil
	default:
		return false, fmt.Errorf("unsupported rebase conflict route %q", route)
	}
}

func (c *sequentialController) retryCIFailure(ctx context.Context, request *Request, ciPhase pipeline.ExecutablePhase, maxAttempts int, feedback []string) ([]PhaseOutcome, PhaseOutcome, error) {
	var outcomes []PhaseOutcome
	development, ok := developmentExecutable(request.Pipeline)
	if !ok {
		return outcomes, PhaseOutcome{}, errors.New("CI feedback requires an enabled Development phase")
	}
	qa, ok := qaExecutable(request.Pipeline)
	if !ok {
		return outcomes, PhaseOutcome{}, errors.New("CI feedback requires an enabled QA phase")
	}
	for attempt := 1; attempt < maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return outcomes, PhaseOutcome{}, err
		}
		if err := c.persistQALoop(context.WithoutCancel(ctx), request, attempt, "fix", "", feedback); err != nil {
			return outcomes, PhaseOutcome{}, err
		}
		fixes, err := c.subphases(pipeline.PhaseDevelopment, request.Subphases)
		if err != nil {
			return outcomes, PhaseOutcome{}, err
		}
		for _, subphase := range fixes {
			fix, fixErr := c.executePhase(ctx, *request, development, subphase, attempt, feedback)
			outcomes = append(outcomes, fix)
			if fixErr != nil {
				return outcomes, PhaseOutcome{}, fixErr
			}
		}
		rebaseOutcome, rebaseErr := c.executeRebaseBeforeQA(ctx, request, attempt, feedback)
		if rebaseOutcome.Result.Phase != "" {
			outcomes = append(outcomes, rebaseOutcome)
		}
		if rebaseErr != nil {
			return outcomes, PhaseOutcome{}, rebaseErr
		}
		qaOutcome, qaErr := c.executePhase(ctx, *request, qa, "", attempt, feedback)
		outcomes = append(outcomes, qaOutcome)
		if qaErr != nil {
			return outcomes, PhaseOutcome{}, qaErr
		}
		if durable, ok := c.state.(durableOrchestrationState); ok {
			project, resetErr := durable.ResetQALoop(context.WithoutCancel(ctx), request.Project.Slug)
			if resetErr != nil {
				return outcomes, PhaseOutcome{}, resetErr
			}
			request.Project = project
		}
		ciOutcome, ciErr := c.executePhase(ctx, *request, ciPhase, "", attempt, nil)
		outcomes = append(outcomes, ciOutcome)
		if ciErr == nil {
			return outcomes, ciOutcome, nil
		}
		feedback = appendUnique(feedback, ciOutcome.Result.ArtifactPaths...)
	}
	return outcomes, PhaseOutcome{}, fmt.Errorf("CI feedback loop exhausted after %d attempt(s)", maxAttempts)
}

// executeDevelopmentLoop runs the Development phase. When the project's plan
// has pending phases, every pending plan phase gets its own fresh
// implementation → testing → review agent sequence (each subphase a new
// agent with a clean context, scoped to that one plan phase), and completion
// is recorded only after the phase's full sequence succeeded — so resume
// re-enters at the first unfinished plan phase. Without a plan, or when
// every plan phase is already complete, one worktree-wide subphase pass runs
// (the shape QA feedback fix passes use).
func (c *sequentialController) executeDevelopmentLoop(ctx context.Context, request *Request, executable pipeline.ExecutablePhase, resumePhase, resumeSubphase string) ([]PhaseOutcome, error) {
	subphases, err := c.subphases(pipeline.PhaseDevelopment, request.Subphases)
	if err != nil {
		return nil, fmt.Errorf("generate development subphases: %w", err)
	}
	skipUntil := ""
	if resumePhase == string(pipeline.PhaseDevelopment) && resumeSubphase != "" {
		skipUntil = resumeSubphase
	}
	var outcomes []PhaseOutcome
	runSequence := func(scope *PlanPhaseScope) error {
		for _, subphase := range subphases {
			if skipUntil != "" {
				if subphase != skipUntil {
					continue
				}
				skipUntil = ""
			}
			if err := ctx.Err(); err != nil {
				return c.stopBeforeDispatch(*request, pipeline.PhaseDevelopment, subphase, err)
			}
			scoped := *request
			scoped.PlanScope = scope
			outcome, err := c.executePhase(ctx, scoped, executable, subphase, 0, nil)
			outcomes = append(outcomes, outcome)
			request.Project.ArtifactPaths = appendUnique(request.Project.ArtifactPaths, outcome.Result.ArtifactPaths...)
			if err != nil {
				return err
			}
		}
		return nil
	}
	pending, total := c.pendingPlanPhases(ctx, request.Project.Slug)
	if len(pending) == 0 {
		return outcomes, runSequence(nil)
	}
	completed := total - len(pending)
	for index, name := range pending {
		scope := &PlanPhaseScope{Name: name, Index: completed + index + 1, Total: total}
		if err := runSequence(scope); err != nil {
			return outcomes, err
		}
		if recorder, ok := c.state.(planStateRecorder); ok {
			// Completion is orchestrator-owned: recorded only after the plan
			// phase's implementation, testing, and review all succeeded.
			if _, recordErr := recorder.RecordPlan(context.WithoutCancel(ctx), request.Project.Slug, nil, []string{name}); recordErr != nil {
				return outcomes, fmt.Errorf("record plan phase %q completion: %w", name, recordErr)
			}
		}
	}
	return outcomes, nil
}

// executePlanningLoop validates only Planning artifacts from snapshots that
// carry the new contract marker. Each invalid artifact gets a fresh agent
// invocation and the same complete scope plus exact rejection evidence.
func (c *sequentialController) executePlanningLoop(ctx context.Context, request *Request, executable pipeline.ExecutablePhase) ([]PhaseOutcome, error) {
	if !pipeline.PlanningContractEnforced(request.Project.PipelineConfig) {
		outcome, err := c.executePhase(ctx, *request, executable, "", 0, nil)
		return []PhaseOutcome{outcome}, err
	}

	var outcomes []PhaseOutcome
	validationSummaries := make([]string, 0, agent.MaxPlanningAttempts)
	for attempt := 1; attempt <= agent.MaxPlanningAttempts; attempt++ {
		outcome, err := c.executePhase(ctx, *request, executable, "", attempt-1, nil)
		outcomes = append(outcomes, outcome)
		request.Project.ArtifactPaths = appendUnique(request.Project.ArtifactPaths, outcome.Result.ArtifactPaths...)
		if err == nil {
			return outcomes, nil
		}
		var contractErr *agent.PlanningContractError
		if !errors.As(err, &contractErr) {
			return outcomes, err
		}
		validationSummaries = append(validationSummaries, fmt.Sprintf("attempt %d: %s", attempt, strings.Join(contractErr.Violations, "; ")))
		if attempt == agent.MaxPlanningAttempts {
			return outcomes, fmt.Errorf("phase-limit-exceeded: Planning artifact remained invalid after %d attempts: %s: %w", agent.MaxPlanningAttempts, strings.Join(validationSummaries, " | "), err)
		}
		request.PlanningRetry = &PlanningRetry{Attempt: attempt + 1, Artifact: contractErr.Artifact, Violations: append([]string(nil), contractErr.Violations...)}
	}
	return outcomes, errors.New("phase-limit-exceeded: Planning attempts exhausted")
}

// pendingPlanPhases returns the plan phases not yet completed, freshly loaded
// from durable state (planning records the plan earlier in the same run).
func (c *sequentialController) pendingPlanPhases(ctx context.Context, slug string) (pending []string, total int) {
	loader, ok := c.state.(interface {
		Load(context.Context, string) (state.ProjectState, error)
	})
	if !ok {
		return nil, 0
	}
	project, err := loader.Load(ctx, slug)
	if err != nil || project.Plan == nil || len(project.Plan.Phases) == 0 {
		return nil, 0
	}
	done := make(map[string]bool, len(project.Plan.Completed))
	for _, name := range project.Plan.Completed {
		done[name] = true
	}
	for _, name := range project.Plan.Phases {
		if !done[name] {
			pending = append(pending, name)
		}
	}
	return pending, len(project.Plan.Phases)
}

func (c *sequentialController) executePhase(ctx context.Context, request Request, executable pipeline.ExecutablePhase, subphase string, iteration int, feedback []string) (PhaseOutcome, error) {
	phase := executable.Phase().ID()
	settings, ok := executable.Settings()
	if !ok {
		settings = config.AgentSettings{}
	}
	artifacts := appendUnique(request.Project.ArtifactPaths, feedback...)
	invocationID := phaseInvocationID(request.RunID, phase, subphase, iteration)
	promptInput := agent.PromptInput{Project: request.Project, Phase: phase, Subphase: subphase, PhaseContract: request.PhaseContracts[phase], ArtifactPaths: artifacts, WorkingDirectory: request.Project.WorktreePath, RunID: invocationID, Development: phase == pipeline.PhaseDevelopment}
	if request.PlanningRetry != nil && phase == pipeline.PhasePlanning {
		promptInput.PlanningAttempt = request.PlanningRetry.Attempt
		promptInput.RejectedPlanningArtifact = request.PlanningRetry.Artifact
		promptInput.PlanningValidationErrors = append([]string(nil), request.PlanningRetry.Violations...)
	}
	if request.PlanScope != nil {
		promptInput.PlanPhase = request.PlanScope.Name
		promptInput.PlanPhaseIndex = request.PlanScope.Index
		promptInput.PlanPhaseTotal = request.PlanScope.Total
	}
	prompt, err := c.prompts.BuildPrompt(promptInput)
	if err != nil {
		return PhaseOutcome{Iteration: iteration}, fmt.Errorf("build prompt for phase %q/%q: %w", phase, subphase, err)
	}
	if _, err := c.state.RecordPhase(ctx, request.Project.Slug, string(phase), subphase, state.StatusRunning, nil, nil); err != nil {
		return PhaseOutcome{Iteration: iteration}, fmt.Errorf("persist phase %q/%q started: %w", phase, subphase, err)
	}
	if err := c.publish(ctx, Event{ProjectSlug: request.Project.Slug, Phase: phase, Subphase: subphase, Type: EventPhaseStarted, At: time.Now().UTC()}); err != nil {
		// The running record is already durable. Any failure after that point
		// must close it through the same non-canceled bookkeeping path rather
		// than strand the project in running.
		status := state.StatusFailed
		if isCancellation(err) {
			status = state.StatusStopped
		}
		result := agent.RunResult{Phase: phase, Subphase: subphase, Status: status}
		return c.finishFailedPhase(context.Background(), request, phase, subphase, result, err, iteration, nil)
	}

	var runResult agent.RunResult
	var runErr error
	var previousHead string
	// GitOps phases run deterministically whenever the services are wired and
	// the resolved settings enable them; agents cannot push branches or open
	// pull requests, so falling back to an agent would only produce drafts.
	if request.Project.GitDisabled && gitOpsPhase(phase) {
		// The project folder is not a git repository: rebase, PR, and CI do
		// not apply. Skip them deterministically (no agent tokens burned)
		// with an artifact stating why, so the rest of the pipeline runs.
		runResult = agent.RunResult{ProjectSlug: request.Project.Slug, Phase: phase, Status: state.StatusFinished, Disposition: agent.DispositionPassed}
		if path, writeErr := writeGitOpsArtifact(request, string(phase)+"-skipped.md", fmt.Sprintf("# %s skipped\n\nThe project folder is not a git repository; the %s phase does not apply.\n", phase, phase)); writeErr == nil {
			runResult.ArtifactPaths = append(runResult.ArtifactPaths, path)
		}
	} else if phase == pipeline.PhaseRebase && c.rebaser != nil && strings.TrimSpace(request.GitOps.ParentBranch) != "" {
		result, rebaseErr := c.runRebase(ctx, request, settings)
		runResult, runErr = result, rebaseErr
	} else if phase == pipeline.PhasePR && c.pullRequests != nil && request.GitOps.EnablePR {
		result, prErr := c.runPullRequest(ctx, request)
		runResult, runErr = result, prErr
	} else if phase == pipeline.PhaseCI && c.checks != nil && request.GitOps.EnableCI {
		result, ciErr := c.runCI(ctx, request)
		runResult, runErr = result, ciErr
	}
	gitOpsHandled := runResult.Phase != ""
	dispatched := false
	commitChecks := false
	if !gitOpsHandled {
		commitChecks = phase == pipeline.PhaseDevelopment && c.developmentCommits != nil && !request.AllowDevelopmentSubphaseWithoutCommit && !request.Project.GitDisabled
		if commitChecks {
			previousHead, runErr = c.developmentCommits.HeadCommit(ctx, request.Project.WorktreePath)
			if runErr != nil {
				runResult = agent.RunResult{Phase: phase, Subphase: subphase, Status: state.StatusFailed}
				return c.finishFailedPhase(ctx, request, phase, subphase, runResult, runErr, iteration, feedback, previousHead)
			}
		}
		if durable, ok := c.state.(durableDispatchState); ok {
			if claimErr := durable.ClaimDispatch(ctx, request.Project.Slug, request.RunID); claimErr != nil {
				if errors.Is(claimErr, state.ErrStopRequested) {
					runErr = context.Canceled
					runResult = agent.RunResult{Phase: phase, Subphase: subphase, Status: state.StatusStopped}
				} else {
					runErr = claimErr
					runResult = agent.RunResult{Phase: phase, Subphase: subphase, Status: state.StatusFailed}
				}
			} else {
				dispatched = true
				runResult, runErr = c.runner.Run(ctx, agent.RunRequest{Project: request.Project, Phase: phase, Subphase: subphase, Settings: settings, Prompt: prompt, WorkingDirectory: request.Project.WorktreePath, ArtifactPaths: artifacts, RunID: invocationID})
			}
		} else {
			dispatched = true
			runResult, runErr = c.runner.Run(ctx, agent.RunRequest{Project: request.Project, Phase: phase, Subphase: subphase, Settings: settings, Prompt: prompt, WorkingDirectory: request.Project.WorktreePath, ArtifactPaths: artifacts, RunID: invocationID})
		}
	}
	if phase == pipeline.PhaseRebase && c.conflictState != nil && !request.Project.GitDisabled {
		unresolved, inspectErr := c.conflictState.HasUnresolvedConflicts(context.WithoutCancel(ctx), request.Project.WorktreePath)
		if inspectErr != nil {
			runErr = errors.Join(runErr, fmt.Errorf("inspect Rebase Git index: %w", inspectErr))
			runResult.Status = state.StatusFailed
		} else if unresolved {
			runErr = errors.Join(runErr, errRebaseHasUnmergedPaths)
			runResult.Status = state.StatusFailed
		}
	}
	if commitChecks && dispatched {
		requireCommit := runErr == nil && runResult.Status != state.StatusFailed && runResult.Status != state.StatusStopped
		if requireCommit {
			// Preserve work the agent finished but forgot to commit instead
			// of failing an otherwise successful subphase.
			if commitErr := c.developmentCommits.AutoCommitUncommittedChanges(
				context.WithoutCancel(ctx),
				request.Project.WorktreePath,
				fmt.Sprintf("gg: %s/%s", phase, subphase),
			); commitErr != nil {
				runErr = errors.Join(runErr, fmt.Errorf("auto-commit development changes for %q/%q: %w", phase, subphase, commitErr))
				runResult.Status = state.StatusFailed
				requireCommit = false
			}
		}
		if verifyErr := c.developmentCommits.VerifyUnsignedDevelopmentCommits(
			context.WithoutCancel(ctx),
			request.Project.WorktreePath,
			previousHead,
			requireCommit,
		); verifyErr != nil {
			runErr = errors.Join(runErr, fmt.Errorf("verify unsigned development commits for %q/%q: %w", phase, subphase, verifyErr))
			runResult.Status = state.StatusFailed
		}
	}
	outcome := PhaseOutcome{Result: runResult, Iteration: iteration, FeedbackArtifactPaths: append([]string(nil), feedback...)}
	if runErr == nil && runResult.Status != state.StatusFailed && runResult.Status != state.StatusStopped {
		if cancellation := ctx.Err(); cancellation != nil {
			runResult.Status = state.StatusStopped
			return c.finishFailedPhase(context.Background(), request, phase, subphase, runResult, cancellation, iteration, feedback, previousHead)
		}
	}
	if runErr != nil || runResult.Status == state.StatusFailed || runResult.Status == state.StatusStopped {
		if runResult.Disposition == agent.DispositionBlocked && blockedInterviewPhase(phase) {
			// An early phase declared itself blocked on missing decisions:
			// re-open the grooming interview with the agent's open questions
			// so the user unblocks it and the rerun starts from the top.
			// This should be rare — grooming is meant to catch everything.
			if parker, ok := c.state.(blockerInterviewer); ok {
				_, _ = parker.ReopenInterviewForBlockers(context.WithoutCancel(ctx), request.Project.Slug, agent.ReadOpenQuestions(request.Project.WorktreePath, phase))
			}
		}
		return c.finishFailedPhase(ctx, request, phase, subphase, runResult, runErr, iteration, feedback, previousHead)
	}
	if phase == pipeline.PhasePlanning && pipeline.PlanningContractEnforced(request.Project.PipelineConfig) {
		if _, validationErr := agent.ValidatePlanningArtifact(request.Project.WorktreePath); validationErr != nil {
			return c.finishFailedPhase(ctx, request, phase, subphase, runResult, validationErr, iteration, feedback, previousHead)
		}
	}
	if dispatched && (phase == pipeline.PhasePlanning || (phase == pipeline.PhaseDevelopment && request.PlanScope == nil)) {
		// Scoped development runs are excluded: their plan-phase completion is
		// orchestrator-owned (recorded after the phase's review passes), and an
		// agent-reported early completion must not skip that phase's testing or
		// review on resume. Planning reaches this point only after strict
		// validation succeeds; legacy plans retain tolerant display parsing.
		c.recordPlanProgress(context.WithoutCancel(ctx), request, phase)
	}

	if err := c.recordResult(ctx, request.Project.Slug, phase, subphase, runResult, state.StatusFinished, previousHead); err != nil {
		if isCancellation(err) || ctx.Err() != nil {
			return c.finishFailedPhase(context.Background(), request, phase, subphase, agent.RunResult{Phase: phase, Subphase: subphase, Status: state.StatusStopped}, err, iteration, feedback, previousHead)
		}
		return outcome, fmt.Errorf("persist phase %q/%q succeeded: %w", phase, subphase, err)
	}
	if err := c.publish(ctx, Event{ProjectSlug: request.Project.Slug, Phase: phase, Subphase: subphase, Type: EventPhaseSucceeded, At: time.Now().UTC(), Outcome: &outcome}); err != nil {
		status := state.StatusFailed
		eventType := EventPhaseFailed
		if isCancellation(err) || ctx.Err() != nil {
			status, eventType = state.StatusStopped, EventProjectStopped
		}
		return c.finishAfterPersistedPhase(context.Background(), request, phase, subphase, outcome, status, eventType, err)
	}
	return outcome, nil
}

// MaxRebaseAttempts is the fixed retry budget for one Rebase execution.
const MaxRebaseAttempts = 3

const maxRebaseAttempts = MaxRebaseAttempts

func (c *sequentialController) runRebase(ctx context.Context, request Request, settings config.AgentSettings) (agent.RunResult, error) {
	result := agent.RunResult{ProjectSlug: request.Project.Slug, Phase: pipeline.PhaseRebase, Subphase: ""}
	parent := strings.TrimSpace(request.GitOps.ParentBranch)
	if parent == "" {
		result.Status = state.StatusFailed
		return result, errors.New("Rebase GitOps configuration requires parent branch")
	}

	manager, hasCheckpoint := c.rebaser.(RebaseCheckpointManager)
	var checkpoint git.RebaseCheckpoint
	if hasCheckpoint {
		var err error
		checkpoint, err = manager.CaptureRebaseCheckpoint(ctx, request.Project.WorktreePath)
		if err != nil {
			result.Status = state.StatusFailed
			return result, fmt.Errorf("capture Rebase checkpoint: %w", err)
		}
	}

	evidence := make([]string, 0, maxRebaseAttempts)
	var lastResult git.RebaseResult
	var lastErr error
	for attempt := 1; attempt <= maxRebaseAttempts; attempt++ {
		if hasCheckpoint {
			if err := manager.RestoreRebaseCheckpoint(ctx, checkpoint); err != nil {
				result.Status = state.StatusFailed
				return result, fmt.Errorf("restore Rebase checkpoint before attempt %d: %w", attempt, err)
			}
		}

		if _, err := c.rebaser.FetchParent(ctx, parent); err != nil {
			lastErr = fmt.Errorf("Rebase attempt %d fetch parent %q: %w", attempt, parent, err)
			evidence = append(evidence, lastErr.Error())
		} else {
			// RebaseProject derives origin/<parent> from ParentBranch. BaseRef is
			// deliberately not an authority so a stale explicit ref cannot
			// override the freshly fetched parent.
			lastResult, lastErr = c.rebaser.RebaseProject(ctx, git.RebaseRequest{
				WorktreePath: request.Project.WorktreePath,
				Branch:       request.Project.BranchName,
				ParentBranch: parent,
				BaseRef:      "origin/" + parent,
			})
			if lastErr != nil {
				evidence = appendRebaseEvidence(evidence, attempt, lastResult, lastErr)
				if c.rebaseAgent != nil {
					agentResult, agentErr := c.runRebaseAgent(ctx, request, settings, attempt, evidence)
					result.ArtifactPaths = appendUnique(result.ArtifactPaths, agentResult.ArtifactPaths...)
					if agentErr == nil {
						lastErr = c.verifyRebaseWorktree(ctx, request.Project.WorktreePath)
						if lastErr == nil {
							lastResult.Conflict = nil
						}
					} else {
						lastErr = agentErr
						evidence = append(evidence, fmt.Sprintf("attempt %d Rebase agent: %v", attempt, agentErr))
					}
				}
			} else {
				lastErr = c.verifyRebaseWorktree(ctx, request.Project.WorktreePath)
				if lastErr == nil && c.rebaseAgent != nil {
					agentResult, agentErr := c.runRebaseAgent(ctx, request, settings, attempt, evidence)
					result.ArtifactPaths = appendUnique(result.ArtifactPaths, agentResult.ArtifactPaths...)
					lastErr = agentErr
					if lastErr == nil {
						lastErr = c.verifyRebaseWorktree(ctx, request.Project.WorktreePath)
					}
				}
			}
		}

		if lastErr == nil {
			result.Status = state.StatusFinished
			result.Disposition = agent.DispositionPassed
			path, writeErr := writeGitOpsArtifact(request, "rebase-report.md", fmt.Sprintf("# Rebase Report\n\n- Branch: `%s`\n- Base: `origin/%s`\n- Attempts: `%d`\n- Result: passed\n\n## Git output\n\n```text\n%s\n```\n", lastResult.Branch, parent, attempt, strings.TrimSpace(lastResult.Output)))
			if writeErr == nil {
				result.ArtifactPaths = appendUnique(result.ArtifactPaths, path)
			}
			return result, nil
		}

		if attempt == maxRebaseAttempts && hasCheckpoint {
			if restoreErr := manager.RestoreRebaseCheckpoint(ctx, checkpoint); restoreErr != nil {
				result.Status = state.StatusFailed
				return result, errors.Join(lastErr, fmt.Errorf("restore Rebase checkpoint after attempt %d: %w", attempt, restoreErr))
			}
		}
	}

	result.Status = state.StatusFailed
	if lastResult.Conflict != nil {
		path, writeErr := writeGitOpsArtifact(request, "rebase-conflict.md", fmt.Sprintf("# Rebase Conflict\n\n- Branch: `%s`\n- Base: `origin/%s`\n- Attempts: `%d`\n- Paths: `%s`\n\n## Evidence\n\n%s\n\n## Git output\n\n```text\n%s\n```\n", lastResult.Branch, parent, maxRebaseAttempts, strings.Join(lastResult.Conflict.Paths, ", "), strings.Join(evidence, "\n"), strings.TrimSpace(lastResult.Conflict.Output)))
		if writeErr == nil {
			result.ArtifactPaths = appendUnique(result.ArtifactPaths, path)
		} else {
			lastErr = errors.Join(lastErr, writeErr)
		}
	}
	return result, fmt.Errorf("Rebase failed after %d attempts: %w", maxRebaseAttempts, lastErr)
}

func (c *sequentialController) verifyRebaseWorktree(ctx context.Context, worktree string) error {
	if verifier, ok := c.rebaser.(RebaseWorktreeVerifier); ok {
		if err := verifier.VerifyRebaseWorktree(ctx, worktree); err != nil {
			return err
		}
	}
	if c.conflictState != nil {
		unresolved, err := c.conflictState.HasUnresolvedConflicts(ctx, worktree)
		if err != nil {
			return fmt.Errorf("inspect Rebase Git index: %w", err)
		}
		if unresolved {
			return errRebaseHasUnmergedPaths
		}
	}
	return nil
}

func (c *sequentialController) runRebaseAgent(ctx context.Context, request Request, settings config.AgentSettings, attempt int, evidence []string) (agent.RunResult, error) {
	input := agent.PromptInput{
		Project:            request.Project,
		AcceptanceCriteria: append([]string(nil), request.Project.AcceptanceCriteria...),
		Phase:              pipeline.PhaseRebase,
		PhaseContract:      request.PhaseContracts[pipeline.PhaseRebase],
		ArtifactPaths:      append([]string(nil), request.Project.ArtifactPaths...),
		WorkingDirectory:   request.Project.WorktreePath,
		RunID:              phaseInvocationID(request.RunID, pipeline.PhaseRebase, "agent", attempt-1),
	}
	prompt, err := c.prompts.BuildPrompt(input)
	if err != nil {
		return agent.RunResult{Phase: pipeline.PhaseRebase}, fmt.Errorf("build Rebase attempt %d prompt: %w", attempt, err)
	}
	if len(evidence) > 0 {
		prompt += "\n\nPrior Rebase attempt evidence (use it to improve this fresh attempt):\n- " + strings.Join(evidence, "\n- ")
	}
	runResult, runErr := c.rebaseAgent.Run(ctx, agent.RunRequest{
		Project:          request.Project,
		Phase:            pipeline.PhaseRebase,
		Settings:         settings,
		Prompt:           prompt,
		WorkingDirectory: request.Project.WorktreePath,
		ArtifactPaths:    input.ArtifactPaths,
		RunID:            input.RunID,
	})
	if runErr != nil {
		return runResult, runErr
	}
	if runResult.Status == state.StatusFailed || runResult.Status == state.StatusStopped {
		return runResult, fmt.Errorf("Rebase agent attempt %d returned status %s", attempt, runResult.Status)
	}
	if runResult.Disposition != "" && runResult.Disposition != agent.DispositionPassed {
		return runResult, fmt.Errorf("Rebase agent attempt %d returned disposition %q", attempt, runResult.Disposition)
	}
	return runResult, nil
}

func appendRebaseEvidence(evidence []string, attempt int, result git.RebaseResult, err error) []string {
	entry := fmt.Sprintf("attempt %d: %v", attempt, err)
	if result.Conflict != nil {
		entry += fmt.Sprintf("; paths=%s; output=%s", strings.Join(result.Conflict.Paths, ","), strings.TrimSpace(result.Conflict.Output))
	}
	return append(evidence, entry)
}

func (c *sequentialController) runPullRequest(ctx context.Context, request Request) (agent.RunResult, error) {
	result := agent.RunResult{ProjectSlug: request.Project.Slug, Phase: pipeline.PhasePR}
	// A proof artifact exists only when the QA phase is part of this
	// pipeline; with QA disabled the PR is created without one.
	proofRequired := false
	for _, executable := range request.Pipeline.Phases() {
		if executable.Phase().ID() == pipeline.PhaseQA {
			proofRequired = true
			break
		}
	}
	created, err := c.pullRequests.Create(ctx, pr.Request{GitOps: request.GitOps, Worktree: request.Project.WorktreePath, Remote: "origin", Branch: request.Project.BranchName, Title: "chore: update project", Why: request.Project.OriginalGoal, What: "Execute the configured gg pipeline", Push: true, ProofRequired: proofRequired})
	if err != nil {
		result.Status = state.StatusFailed
		return result, err
	}
	result.Status, result.Disposition = state.StatusFinished, agent.DispositionPassed
	result.ExternalIdentity = created.URL
	if path, writeErr := writeGitOpsArtifact(request, "pr.md", fmt.Sprintf("# Pull Request\n\n- URL: %s\n- Created: %t\n", created.URL, created.Created)); writeErr == nil {
		result.ArtifactPaths = append(result.ArtifactPaths, path)
	}
	return result, nil
}

func (c *sequentialController) runCI(ctx context.Context, request Request) (agent.RunResult, error) {
	result := agent.RunResult{ProjectSlug: request.Project.Slug, Phase: pipeline.PhaseCI}
	identity := request.PullRequestURL
	if identity == "" {
		identity = request.Project.BranchName
	}
	ciResult, err := c.checks.Monitor(ctx, ci.Config{Enabled: request.GitOps.EnableCI, Identity: identity, Worktree: request.Project.WorktreePath, ArtifactRoot: request.ArtifactRoot, ProjectSlug: request.Project.Slug, RunID: request.RunID, MaxPolls: 3})
	result.ArtifactPaths = append(result.ArtifactPaths, ciResult.ReportPath, ciResult.FeedbackPath)
	if err != nil {
		result.Status = state.StatusFailed
		return result, err
	}
	if ciResult.Outcome != ci.OutcomePassed {
		result.Status, result.Disposition = state.StatusFailed, agent.DispositionFeedback
		return result, &agent.SemanticFailureError{Phase: pipeline.PhaseCI, Disposition: agent.DispositionFeedback, Details: "CI checks did not pass"}
	}
	result.Status, result.Disposition = state.StatusFinished, agent.DispositionPassed
	return result, nil
}

// blockedInterviewPhase reports whether a blocked disposition in this phase
// re-opens the grooming interview: only the early requirement/planning phases
// may punt decisions back to the user.
func blockedInterviewPhase(phase pipeline.PhaseID) bool {
	return phase == pipeline.PhaseAcceptanceCriteria || phase == pipeline.PhaseGrooming || phase == pipeline.PhasePlanning
}

// blockerInterviewer is the optional capability for re-opening the grooming
// interview with a blocked phase's open questions.
type blockerInterviewer interface {
	ReopenInterviewForBlockers(context.Context, string, []string) (state.ProjectState, error)
}

// gitOpsPhase reports whether the phase operates on git/GitHub state and is
// therefore meaningless for a project whose folder is not a git repository.
func gitOpsPhase(phase pipeline.PhaseID) bool {
	return phase == pipeline.PhaseRebase || phase == pipeline.PhasePR || phase == pipeline.PhaseCI
}

func writeGitOpsArtifact(request Request, name, content string) (string, error) {
	root := request.ArtifactRoot
	if strings.TrimSpace(root) == "" {
		root = request.Project.WorktreePath
	}
	dir := filepath.Join(root, ".gg", "projects", request.Project.Slug, "artifacts")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return "", err
	}
	return path, nil
}

func phaseInvocationID(runID string, phase pipeline.PhaseID, subphase string, iteration int) string {
	if subphase == "" {
		subphase = "phase"
	}
	return fmt.Sprintf("%s/%s/%s/iteration-%d", runID, phase, subphase, iteration)
}

func (c *sequentialController) finishAfterPersistedPhase(ctx context.Context, request Request, phase pipeline.PhaseID, subphase string, outcome PhaseOutcome, status state.LifecycleStatus, eventType EventType, cause error) (PhaseOutcome, error) {
	var closeErr error
	if closer, ok := c.state.(durableRunCloser); ok {
		closeErr = closer.CloseRun(ctx, request.Project.Slug, status)
	} else {
		closeErr = c.recordResult(ctx, request.Project.Slug, phase, subphase, outcome.Result, status)
	}
	publishErr := c.publish(ctx, Event{ProjectSlug: request.Project.Slug, Phase: phase, Subphase: subphase, Type: eventType, At: time.Now().UTC(), Outcome: &outcome, Error: cause})
	return outcome, errors.Join(fmt.Errorf("publish phase %q/%q succeeded: %w", phase, subphase, cause), closeErr, publishErr)
}

func (c *sequentialController) stopBeforeDispatch(request Request, phase pipeline.PhaseID, subphase string, cause error) error {
	result := agent.RunResult{Phase: phase, Subphase: subphase, Status: state.StatusStopped}
	_, err := c.finishFailedPhase(context.Background(), request, phase, subphase, result, cause, 0, nil)
	return err
}

func (c *sequentialController) finishFailedPhase(ctx context.Context, request Request, phase pipeline.PhaseID, subphase string, runResult agent.RunResult, runErr error, iteration int, feedback []string, developmentBaseCommit ...string) (PhaseOutcome, error) {
	outcome := PhaseOutcome{Result: runResult, Iteration: iteration, FeedbackArtifactPaths: append([]string(nil), feedback...)}
	baseCommit := ""
	if len(developmentBaseCommit) > 0 {
		baseCommit = developmentBaseCommit[0]
	}
	phaseErr := runErr
	if phaseErr == nil {
		phaseErr = fmt.Errorf("agent returned status %s", runResult.Status)
	}
	if runResult.Error == "" {
		// GitOps phases (rebase, PR, CI) fail through runErr without an
		// agent-populated result: persist the reason so screens can show it.
		runResult.Error = phaseErr.Error()
	}
	status, eventType := state.StatusFailed, EventPhaseFailed
	if runResult.Status == state.StatusStopped || isCancellation(runErr) {
		status, eventType = state.StatusStopped, EventProjectStopped
	}
	persistCtx := ctx
	if status == state.StatusStopped {
		persistCtx = context.Background()
	}
	if persistErr := c.recordResult(persistCtx, request.Project.Slug, phase, subphase, runResult, status, baseCommit); persistErr != nil {
		return outcome, errors.Join(fmt.Errorf("execute phase %q/%q: %w", phase, subphase, phaseErr), fmt.Errorf("persist phase %q/%q failed: %w", phase, subphase, persistErr))
	}
	var stoppedErr error
	if status == state.StatusStopped {
		stoppedErr = c.markProjectStopped(persistCtx, request.Project.Slug, string(phase), subphase)
	}
	publishErr := c.publish(persistCtx, Event{ProjectSlug: request.Project.Slug, Phase: phase, Subphase: subphase, Type: eventType, At: time.Now().UTC(), Outcome: &outcome, Error: phaseErr})
	outcome.retryableSemanticFailure = status == state.StatusFailed &&
		(runResult.Disposition == agent.DispositionFailed || runResult.Disposition == agent.DispositionFeedback) &&
		(runErr == nil || agent.IsSemanticFailure(runErr)) &&
		publishErr == nil
	return outcome, errors.Join(fmt.Errorf("execute phase %q/%q: %w", phase, subphase, phaseErr), stoppedErr, publishErr)
}

func (c *sequentialController) publishFeedback(ctx context.Context, slug string, outcome PhaseOutcome) error {
	// The failed QA result already persisted its artifact paths. Feedback is an
	// event over that durable result, not a second phase-history entry.
	return c.publish(ctx, Event{ProjectSlug: slug, Phase: pipeline.PhaseQA, Type: EventFeedbackCreated, At: time.Now().UTC(), Outcome: &outcome})
}

func (c *sequentialController) subphases(phase pipeline.PhaseID, generation pipeline.DevelopmentSubphaseGeneration) ([]string, error) {
	if phase != pipeline.PhaseDevelopment {
		return []string{""}, nil
	}
	generated, err := pipeline.GenerateDevelopmentSubphases(generation)
	if err != nil {
		return nil, err
	}
	subphases := make([]string, 0, len(generated))
	for _, subphase := range generated {
		subphases = append(subphases, string(subphase.ID()))
	}
	return subphases, nil
}

// planStateRecorder is the optional capability for mirroring plan.md's phase
// list and development completion marks into project state.
type planStateRecorder interface {
	RecordPlan(context.Context, string, []string, []string) (state.ProjectState, error)
}

// recordPlanProgress mirrors the plan-tracking frontmatter of the phase's
// canonical artifact into project state so the attach view can show which
// plan phases are done. It is best-effort display data: parsing or
// persistence problems must never affect the phase outcome.
func (c *sequentialController) recordPlanProgress(ctx context.Context, request Request, phase pipeline.PhaseID) {
	recorder, ok := c.state.(planStateRecorder)
	if !ok {
		return
	}
	phases, completed := agent.ReadPlanFrontmatter(request.Project.WorktreePath, phase)
	if len(phases) == 0 && len(completed) == 0 {
		return
	}
	_, _ = recorder.RecordPlan(ctx, request.Project.Slug, phases, completed)
}

func (c *sequentialController) recordResult(ctx context.Context, slug string, phase pipeline.PhaseID, subphase string, result agent.RunResult, status state.LifecycleStatus, developmentBaseCommit ...string) error {
	baseCommit := ""
	if len(developmentBaseCommit) > 0 {
		baseCommit = developmentBaseCommit[0]
	}
	_, err := c.state.RecordPhase(ctx, slug, string(phase), subphase, status, executionOutcome(result, baseCommit), result.ArtifactPaths)
	return err
}
func executionOutcome(result agent.RunResult, developmentBaseCommit string) *state.ExecutionOutcome {
	return &state.ExecutionOutcome{
		ExitCode:              result.ExitCode,
		StartedAt:             result.StartedAt,
		FinishedAt:            result.FinishedAt,
		Duration:              result.Duration,
		DevelopmentBaseCommit: developmentBaseCommit,
		TokensUsed:            result.TokensUsed,
		CostUSD:               result.CostUSD,
		Error:                 result.Error,
	}
}
func isCancellation(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
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

func (c *sequentialController) publish(ctx context.Context, event Event) error {
	if c.events != nil {
		if err := c.events.Publish(ctx, event); err != nil {
			return err
		}
	}
	if c.notifications != nil {
		// Completion delivery is a best-effort side effect. Durable lifecycle
		// persistence and the event journal must not be rolled back or stop
		// monitoring because an injected transport is unavailable.
		_ = c.notifications.Publish(ctx, event)
	}
	return nil
}

func (c *sequentialController) Stop(ctx context.Context, request StopRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	id := request.RunID
	if id == "" {
		found := false
		// requests includes completed runs for in-process resume. Anonymous
		// stop lookup must inspect active IDs only.
		for candidate := range c.active {
			saved, ok := c.requests[candidate]
			if !ok || saved.Project.Slug != request.ProjectSlug {
				continue
			}
			if found {
				c.mu.Unlock()
				return fmt.Errorf("project %q has ambiguous active run identity", request.ProjectSlug)
			}
			id = candidate
			found = true
		}
	}
	cancel, ok := c.active[id]
	saved, savedOK := c.requests[id]
	c.mu.Unlock()
	if request.ProjectSlug == "" && savedOK {
		request.ProjectSlug = saved.Project.Slug
	}
	if recoverer, recoverable := c.state.(durableRunReservationCanceler); recoverable {
		if request.ProjectSlug == "" {
			return errors.New("stop requires a project selector")
		}
		recovered, err := recoverer.CancelRunReservation(ctx, request.ProjectSlug)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("run %q is not active", request.ProjectSlug)
			}
			return err
		}
		if recovered {
			if ok {
				cancel()
			}
			return c.publish(context.WithoutCancel(ctx), Event{
				ProjectSlug: request.ProjectSlug,
				Type:        EventProjectStopped,
				At:          time.Now().UTC(),
			})
		}
	}
	durable, durableOK := c.state.(durableStopState)
	if durableOK {
		if request.ProjectSlug == "" {
			return errors.New("stop requires a project selector")
		}
		if err := durable.RequestStop(ctx, request.ProjectSlug, request.RunID); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("run %q is not active", request.ProjectSlug)
			}
			return err
		}
	}
	if !ok {
		if durableOK {
			return nil
		}
		return fmt.Errorf("run %q is not active", id)
	}
	cancel()
	return nil
}

func (c *sequentialController) watchDurableStop(ctx context.Context, cancel context.CancelFunc, durable durableStopState, slug, runID string, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			requested, err := durable.StopRequested(ctx, slug, runID)
			if err == nil && requested {
				cancel()
				return
			}
		}
	}
}

func (c *sequentialController) Resume(ctx context.Context, request ResumeRequest) ([]PhaseOutcome, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id := request.RunID
	if id == "" {
		id = fmt.Sprintf("%s-%d", request.ProjectSlug, time.Now().UnixNano())
	}
	execution := request.Execution
	c.mu.Lock()
	if len(execution.Pipeline.Phases()) == 0 {
		execution = c.requests[id]
	}
	c.mu.Unlock()
	if len(execution.Pipeline.Phases()) == 0 {
		return nil, errors.New("resume execution request is required")
	}
	if execution.Project.Slug == "" {
		execution.Project.Slug = request.ProjectSlug
	}
	if execution.RunID == "" {
		execution.RunID = id
	}
	resumable, ok := c.state.(ResumeState)
	if !ok {
		return nil, errors.New("orchestrator phase state does not support resume")
	}
	project, err := resumable.Load(ctx, execution.Project.Slug)
	if err != nil {
		return nil, fmt.Errorf("load project for resume: %w", err)
	}
	reactivatedFinished := false
	if project.Status.IsTerminal() {
		if project.Status == state.StatusFinished && project.Terminal != nil && project.Terminal.Kind == state.TerminalPullRequestMerged {
			return nil, nil
		}
		if project.Status != state.StatusFinished {
			return nil, fmt.Errorf("cannot resume terminal project %q", project.Slug)
		}
		reactivator, ok := c.state.(interface {
			ReactivateFinished(context.Context, string) (state.ProjectState, error)
		})
		if !ok {
			return nil, fmt.Errorf("cannot resume finished project %q: finished-project reactivation is unsupported", project.Slug)
		}
		project, err = reactivator.ReactivateFinished(ctx, project.Slug)
		if err != nil {
			return nil, fmt.Errorf("reactivate finished project %q: %w", project.Slug, err)
		}
		reactivatedFinished = true
	}
	if !reactivatedFinished && project.Status != state.StatusStopped && project.Status != state.StatusFailed {
		return nil, fmt.Errorf("cannot resume project %q from status %s", project.Slug, project.Status)
	}
	if project.MaxQAAttempts > 0 {
		if execution.MaxIterations > 0 && execution.MaxIterations != project.MaxQAAttempts {
			return nil, fmt.Errorf(
				"cannot resume project %q: persisted execution snapshot QA maximum %d does not match lifecycle state %d",
				project.Slug,
				execution.MaxIterations,
				project.MaxQAAttempts,
			)
		}
		execution.MaxIterations = project.MaxQAAttempts
	}
	if project.QALoopStage == "exhausted" || (project.MaxQAAttempts > 0 && project.QACompletedAttempts >= project.MaxQAAttempts) {
		return nil, fmt.Errorf("cannot resume project %q: QA feedback loop exhausted after %d attempt(s)", project.Slug, project.QACompletedAttempts)
	}
	_, qaEnabled := qaExecutable(execution.Pipeline)
	if (project.QALoopStage != "" || project.QACompletedAttempts > 0) && !qaEnabled {
		return nil, fmt.Errorf("cannot resume project %q: persisted QA loop cursor requires an enabled QA phase", project.Slug)
	}
	resolvedConflict := false
	if project.PendingRebaseConflict {
		router := c.conflicts
		if router == nil {
			router = terminalConflictRouter{}
		}
		route, routeErr := router.Route(ctx, Conflict{
			Phase:         pipeline.PhaseRebase,
			WorktreePath:  project.WorktreePath,
			ArtifactPaths: append([]string(nil), project.RebaseConflictArtifactPaths...),
		})
		if routeErr != nil {
			return nil, fmt.Errorf("inspect pending rebase conflict: %w", routeErr)
		}
		if route != ConflictRouteQA {
			return nil, fmt.Errorf("cannot resume project %q: rebase conflict remains unresolved", project.Slug)
		}
		if !qaEnabled {
			return nil, fmt.Errorf("cannot resume project %q: resolved rebase conflict requires an enabled QA phase", project.Slug)
		}
		resolvedConflict = true
	}
	if err := c.verifyInterruptedDevelopment(context.WithoutCancel(ctx), project, execution.AllowDevelopmentSubphaseWithoutCommit); err != nil {
		return nil, fmt.Errorf("resume project %q: %w", project.Slug, err)
	}
	var reservation *state.RunReservation
	var last, subphase string
	var finalizeOnly bool
	if reserver, ok := c.state.(durableResumeReserver); ok {
		project, reservation, err = reserver.ReserveRun(ctx, project.Slug, nil)
		if err != nil {
			return nil, fmt.Errorf("reserve project for resume: %w", err)
		}
		project, err = c.reconcileFinishedQAFixCursor(context.WithoutCancel(ctx), project, execution.Subphases)
		if err != nil {
			if rollbackErr := reservation.Rollback(context.WithoutCancel(ctx)); rollbackErr != nil {
				err = errors.Join(err, fmt.Errorf("rollback resume reservation: %w", rollbackErr))
			}
			return nil, fmt.Errorf("reconcile project %q resume cursor: %w", project.Slug, err)
		}
		last, subphase, finalizeOnly, err = resumeExecutionCursor(project, execution.Pipeline, execution.Subphases, resolvedConflict)
		if err != nil {
			if rollbackErr := reservation.Rollback(context.WithoutCancel(ctx)); rollbackErr != nil {
				err = errors.Join(err, fmt.Errorf("rollback resume reservation: %w", rollbackErr))
			}
			return nil, fmt.Errorf("resume project %q: %w", project.Slug, err)
		}
	} else {
		last, subphase, finalizeOnly, err = resumeExecutionCursor(project, execution.Pipeline, execution.Subphases, resolvedConflict)
		if err != nil {
			return nil, fmt.Errorf("resume project %q: %w", project.Slug, err)
		}
		project, err = resumable.Transition(ctx, project.Slug, state.StatusRunning, project.CurrentPhase, project.CurrentSubphase, nil)
		if err != nil {
			return nil, fmt.Errorf("mark project running for resume: %w", err)
		}
	}
	execution.Project = project
	outcomes, executeErr := c.execute(ctx, execution, last, subphase, resolvedConflict, finalizeOnly)
	if executeErr != nil && reservation != nil {
		if rollbackErr := reservation.Rollback(context.WithoutCancel(ctx)); rollbackErr != nil {
			executeErr = errors.Join(executeErr, fmt.Errorf("rollback unclaimed resume reservation: %w", rollbackErr))
		}
	}
	return outcomes, executeErr
}

func (c *sequentialController) verifyInterruptedDevelopment(ctx context.Context, project state.ProjectState, allowWithoutCommit bool) error {
	if allowWithoutCommit || len(project.PhaseHistory) == 0 {
		return nil
	}
	var interrupted *state.PhaseRecord
	for index := len(project.PhaseHistory) - 1; index >= 0; index-- {
		record := &project.PhaseHistory[index]
		if record.Phase != string(pipeline.PhaseDevelopment) {
			break
		}
		if (record.Status == state.StatusFailed || record.Status == state.StatusStopped) &&
			record.Outcome != nil &&
			record.Outcome.DevelopmentBaseCommit != "" {
			interrupted = record
			break
		}
	}
	if interrupted == nil {
		return nil
	}
	if c.developmentCommits == nil {
		return errors.New("persisted failed Development phase requires a commit verifier")
	}
	if err := c.developmentCommits.VerifyUnsignedDevelopmentCommits(
		ctx,
		project.WorktreePath,
		interrupted.Outcome.DevelopmentBaseCommit,
		false,
	); err != nil {
		return fmt.Errorf(
			"verify commits retained by failed Development phase %q from base %s: %w",
			interrupted.Subphase,
			interrupted.Outcome.DevelopmentBaseCommit,
			err,
		)
	}
	return nil
}

func (c *sequentialController) reconcileFinishedQAFixCursor(
	ctx context.Context,
	project state.ProjectState,
	generation pipeline.DevelopmentSubphaseGeneration,
) (state.ProjectState, error) {
	if project.QALoopStage != "fix" || len(project.PhaseHistory) == 0 {
		return project, nil
	}
	last := project.PhaseHistory[len(project.PhaseHistory)-1]
	if last.Phase != string(pipeline.PhaseDevelopment) ||
		last.Subphase != project.QAFixNextSubphase ||
		last.Status != state.StatusFinished {
		return project, nil
	}
	subphases, err := c.subphases(pipeline.PhaseDevelopment, generation)
	if err != nil {
		return state.ProjectState{}, fmt.Errorf("restore Development fix subphases: %w", err)
	}
	for index, subphase := range subphases {
		if subphase != project.QAFixNextSubphase {
			continue
		}
		if index+1 < len(subphases) {
			durable, ok := c.state.(durableQAFixCursorState)
			if !ok {
				return state.ProjectState{}, errors.New("phase state cannot advance the durable QA fix cursor")
			}
			updated, updateErr := durable.SetQAFixNextSubphase(ctx, project.Slug, subphases[index+1])
			if updateErr != nil {
				return state.ProjectState{}, fmt.Errorf("persist next QA fix subphase: %w", updateErr)
			}
			return updated, nil
		}
		durable, ok := c.state.(durableOrchestrationState)
		if !ok {
			return state.ProjectState{}, errors.New("phase state cannot return a completed QA fix to QA")
		}
		updated, updateErr := durable.UpdateQALoop(
			ctx,
			project.Slug,
			project.QACompletedAttempts,
			"qa",
			project.QAFeedbackArtifactPaths,
		)
		if updateErr != nil {
			return state.ProjectState{}, fmt.Errorf("persist QA cursor after final fix: %w", updateErr)
		}
		return updated, nil
	}
	return state.ProjectState{}, fmt.Errorf("persisted QA fix subphase %q is not configured", project.QAFixNextSubphase)
}

func resumeExecutionCursor(project state.ProjectState, plan pipeline.ExecutablePipeline, generation pipeline.DevelopmentSubphaseGeneration, resolvedConflict bool) (string, string, bool, error) {
	if resolvedConflict {
		return "", "", false, nil
	}
	if project.PostRebaseContinuationPhase != "" {
		if !pipelineContains(plan, pipeline.PhaseID(project.PostRebaseContinuationPhase)) {
			return "", "", false, fmt.Errorf("post-Rebase continuation phase %q is not in the persisted pipeline", project.PostRebaseContinuationPhase)
		}
		return project.PostRebaseContinuationPhase, "", false, nil
	}
	switch project.QALoopStage {
	case "qa":
		return string(pipeline.PhaseQA), "", false, nil
	case "fix":
		if project.QAFixNextSubphase == "" {
			return "", "", false, errors.New("persisted QA fix cursor has no next Development subphase")
		}
		return string(pipeline.PhaseQA), project.QAFixNextSubphase, false, nil
	}
	if project.CurrentPhase == "pipeline" {
		phases := plan.Phases()
		if len(phases) == 0 {
			return "", "", false, errors.New("persisted pipeline has no enabled phases")
		}
		return string(phases[0].Phase().ID()), "", false, nil
	}
	if !pipelineContains(plan, pipeline.PhaseID(project.CurrentPhase)) {
		return "", "", false, fmt.Errorf("current phase %q is not in the persisted pipeline", project.CurrentPhase)
	}
	if len(project.PhaseHistory) == 0 {
		return "", "", false, errors.New("project has no phase history for its resume cursor")
	}
	last := project.PhaseHistory[len(project.PhaseHistory)-1]
	if last.Skip != nil {
		// Skip advances the durable cursor before the continuation is
		// dispatched. On restart the skipped record is still the latest history
		// entry, so resume must trust its exact persisted next unit rather than
		// treating the old failed phase as replayable.
		if last.Skip.NextPhase == "" {
			return "", "", true, nil
		}
		if project.CurrentPhase != last.Skip.NextPhase || project.CurrentSubphase != last.Skip.NextSubphase {
			return "", "", false, fmt.Errorf("skip cursor %q/%q does not match current phase %q/%q", last.Skip.NextPhase, last.Skip.NextSubphase, project.CurrentPhase, project.CurrentSubphase)
		}
		return project.CurrentPhase, project.CurrentSubphase, false, nil
	}
	if last.Phase != project.CurrentPhase || last.Subphase != project.CurrentSubphase {
		return "", "", false, fmt.Errorf("phase history cursor %q/%q does not match current phase %q/%q", last.Phase, last.Subphase, project.CurrentPhase, project.CurrentSubphase)
	}
	if last.Status != state.StatusFinished {
		return project.CurrentPhase, project.CurrentSubphase, false, nil
	}
	phase, subphase, hasNext, err := nextExecutionCursor(plan, generation, pipeline.PhaseID(project.CurrentPhase), project.CurrentSubphase)
	if err != nil {
		return "", "", false, err
	}
	if !hasNext {
		return "", "", true, nil
	}
	return phase, subphase, false, nil
}

func pipelineContains(plan pipeline.ExecutablePipeline, target pipeline.PhaseID) bool {
	for _, executable := range plan.Phases() {
		if executable.Phase().ID() == target {
			return true
		}
	}
	return false
}

func nextExecutionCursor(plan pipeline.ExecutablePipeline, generation pipeline.DevelopmentSubphaseGeneration, current pipeline.PhaseID, currentSubphase string) (string, string, bool, error) {
	phases := plan.Phases()
	for phaseIndex, executable := range phases {
		if executable.Phase().ID() != current {
			continue
		}
		if current == pipeline.PhaseDevelopment {
			generated, err := pipeline.GenerateDevelopmentSubphases(generation)
			if err != nil {
				return "", "", false, fmt.Errorf("restore Development subphases: %w", err)
			}
			if currentSubphase == "" && len(generated) > 0 {
				return string(current), string(generated[0].ID()), true, nil
			}
			for subphaseIndex, subphase := range generated {
				if string(subphase.ID()) != currentSubphase {
					continue
				}
				if subphaseIndex+1 < len(generated) {
					return string(current), string(generated[subphaseIndex+1].ID()), true, nil
				}
				break
			}
			if currentSubphase != "" {
				found := false
				for _, subphase := range generated {
					found = found || string(subphase.ID()) == currentSubphase
				}
				if !found {
					return "", "", false, fmt.Errorf("current Development subphase %q is not configured", currentSubphase)
				}
			}
		} else if currentSubphase != "" {
			return "", "", false, fmt.Errorf("phase %q cannot resume unknown subphase %q", current, currentSubphase)
		}
		if phaseIndex+1 == len(phases) {
			return "", "", false, nil
		}
		return string(phases[phaseIndex+1].Phase().ID()), "", true, nil
	}
	return "", "", false, fmt.Errorf("current phase %q is not configured", current)
}
