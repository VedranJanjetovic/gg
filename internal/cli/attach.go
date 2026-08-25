package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/VedranJanjetovic/gg/internal/git"
	"github.com/VedranJanjetovic/gg/internal/orchestrator"
	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/state"
	"github.com/VedranJanjetovic/gg/internal/tui"
)

// ProjectAttachment is the orchestration-independent session contract exposed
// to terminal frontends. Actions are synchronous: the frontend decides how to
// coordinate them with rendering, and action errors are returned unchanged.
type ProjectAttachment struct {
	Project       state.ProjectState
	Load          func(context.Context) (state.ProjectState, error)
	Start         func(context.Context) error
	Stop          func(context.Context) error
	Resume        func(context.Context) error
	Configure     func(context.Context) error
	Skip          func(context.Context) error
	SkipAvailable bool
	SkipLabel     string
	SkipTarget    func(state.ProjectState) (bool, string)
	OpenCode      func(context.Context) error
	OpenTerminal  func(context.Context) error
	// Notice is an optional one-off message the frontend shows when the
	// session opens (for example why the grooming interview was skipped).
	Notice string
	// GroomingPending marks a project parked on unanswered grooming
	// questions; frontends offer re-entry (returning tui.ErrGroomingRequested).
	GroomingPending bool
}

// staleRunRecoverer is the optional capability for converting a running
// project whose owner process died into a resumable stopped project.
type staleRunRecoverer interface {
	RecoverIfStale(context.Context, string) (state.ProjectState, bool, error)
}

// ProjectAttacher owns one foreground project session.
type ProjectAttacher interface {
	Attach(context.Context, ProjectAttachment) error
}

// AttachProject opens an existing project session for alternate frontends.
func (a *App) AttachProject(ctx context.Context, selector string) error {
	return a.attachExisting(ctx, selector)
}

func (a *App) createAndAttach(ctx context.Context, stdout io.Writer) error {
	if err := a.requireConfiguredProject(); err != nil {
		return err
	}
	if a.attacher == nil {
		return errors.New("project attachment is not configured")
	}
	if a.projectPrompt == nil {
		return errors.New("project input is not configured")
	}
	options, err := parseRunOptions(nil)
	if err != nil {
		return err
	}
	return a.createAndAttachWithOptions(ctx, stdout, options)
}

// createAndAttachWithOptions creates a project from the interactive prompt and
// attaches the live TUI; the pipeline starts through the attachment's Start
// action so progress is visible instead of running headless.
func (a *App) createAndAttachWithOptions(ctx context.Context, stdout io.Writer, options runOptions) error {
	selector, err := a.createProject(ctx, stdout, options.maxIterations)
	if err != nil {
		return fmt.Errorf("create project: %w", err)
	}
	return a.attachProject(ctx, selector, a.pipelineStarter(selector, options))
}

// pipelineStarter builds the attachment Start action. With a run spawner the
// pipeline executes in a detached background gg that survives this process
// exiting; otherwise it runs in-process (tests, minimal compositions).
func (a *App) pipelineStarter(selector string, options runOptions) func(context.Context) error {
	if a.runSpawner != nil {
		args := append(append([]string{"run"}, options.flagArgs...), selector)
		return func(startCtx context.Context) error {
			return a.startDetached(startCtx, selector, args)
		}
	}
	return func(startCtx context.Context) error {
		run := options
		run.args = []string{selector}
		return a.runPipeline(startCtx, io.Discard, run)
	}
}

func (a *App) attachExisting(ctx context.Context, rawSelector string) error {
	if err := a.requireConfiguredProject(); err != nil {
		return err
	}
	selector, err := git.ProjectSlug(rawSelector)
	if err != nil {
		return fmt.Errorf("normalize project selector: %w", err)
	}
	return a.attachProject(ctx, selector, nil)
}

// autoResumeAfterInterview resumes a stopped or failed project whose grooming
// interview just completed — the interview was the only thing parking it (for
// example a feedback loop stopped the pipeline), so no manual r is demanded.
func (a *App) autoResumeAfterInterview(ctx context.Context, service lifecycleService, selector, notice string, project *state.ProjectState) string {
	if a.runSpawner == nil || (project.Status != state.StatusStopped && project.Status != state.StatusFailed) {
		return notice
	}
	if resumeErr := a.startDetached(ctx, selector, []string{"resume", selector}); resumeErr != nil {
		return fmt.Sprintf("Interview recorded, but resuming failed: %v — press r to resume.", resumeErr)
	}
	if reloaded, loadErr := service.Load(ctx, selector); loadErr == nil {
		*project = reloaded
	}
	if notice == "" {
		return "Interview recorded — pipeline resumed."
	}
	return notice + " Pipeline resumed."
}

func (a *App) attachProject(ctx context.Context, selector string, start func(context.Context) error) error {
	service, err := a.projectService(ctx)
	if err != nil {
		return fmt.Errorf("load project service: %w", err)
	}
	// The session loops so the g key can re-enter a paused grooming
	// interview and the f key can open the feedback chat: the frontend quits
	// with a sentinel error and the flow runs before re-attaching.
	carryNotice := ""
	for {
		project, err := service.Load(ctx, selector)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("project %q does not exist", selector)
			}
			return fmt.Errorf("load project %q: %w", selector, err)
		}
		recoveredNotice := ""
		if project.Status == state.StatusRunning {
			// A "running" project whose owning process died is stale: recover
			// it to a resumable stopped state instead of showing a dead run.
			if recoverer, ok := service.(staleRunRecoverer); ok {
				recovered, changed, recoverErr := recoverer.RecoverIfStale(ctx, selector)
				if recoverErr == nil && changed {
					project = recovered
					recoveredNotice = "This run's process was gone — recovered to stopped; press r to resume."
				}
			}
		}
		sessionStart := start
		if sessionStart == nil && project.Status == state.StatusPending {
			// Attaching to a project whose pipeline never started (for
			// example after pausing the grooming interview) must be able to
			// start it.
			if options, optErr := parseRunOptions(nil); optErr == nil {
				sessionStart = a.pipelineStarter(selector, options)
			}
		}
		notice := recoveredNotice
		if carryNotice != "" {
			notice, carryNotice = carryNotice, ""
		}
		groomingPending := false
		if interviewPending(project) && (a.questionAsker != nil || a.interviewSession != nil) && tui.InteractiveTerminal(a.input, a.output.Stdout) {
			// The project is waiting on grooming answers: run the interview
			// before the pipeline TUI so it cannot start past the questions.
			proceed, interviewNotice := a.runGroomingInterview(ctx, service, &project)
			notice = interviewNotice
			if !proceed {
				sessionStart = nil
				groomingPending = true
				if notice == "" {
					notice = "Grooming interview paused — press g to continue answering."
				}
			} else if sessionStart == nil {
				notice = a.autoResumeAfterInterview(ctx, service, selector, notice, &project)
			}
		}
		attachment := ProjectAttachment{
			Notice:          notice,
			GroomingPending: groomingPending,
			Project:         project,
			Load: func(loadCtx context.Context) (state.ProjectState, error) {
				current, loadErr := service.Load(loadCtx, selector)
				if loadErr != nil {
					return state.ProjectState{}, fmt.Errorf("load project %q: %w", selector, loadErr)
				}
				return current, nil
			},
			Start: sessionStart,
			Stop: func(stopCtx context.Context) error {
				return a.stop(stopCtx, io.Discard, []string{selector})
			},
			Resume: func(resumeCtx context.Context) error {
				if a.runSpawner != nil {
					// Detached resume: the run must outlive this gg process.
					return a.startDetached(resumeCtx, selector, []string{"resume", selector})
				}
				return a.resume(resumeCtx, io.Discard, resumeOptions{selector: selector})
			},
		}
		if skipper, ok := service.(skipExecutionService); ok {
			available, label := skipProjection(project)
			attachment.SkipAvailable = available
			attachment.SkipLabel = label
			attachment.SkipTarget = skipProjection
			attachment.Skip = func(skipCtx context.Context) error {
				return a.skipFailedExecution(skipCtx, selector, skipper)
			}
		}
		if project.Status == state.StatusFailed || project.Status == state.StatusStopped {
			attachment.Configure = func(configureCtx context.Context) error {
				return a.configureExistingProject(configureCtx, selector, project)
			}
		}
		if a.launchActions != nil {
			attachment.OpenCode = func(openCtx context.Context) error {
				return a.launchActions.OpenCode(openCtx, project)
			}
			attachment.OpenTerminal = func(openCtx context.Context) error {
				return a.launchActions.OpenTerminal(openCtx, project)
			}
		}
		switch {
		case a.attacher != nil:
			err = a.attacher.Attach(ctx, attachment)
		case a.tui != nil:
			err = a.tui(ctx, attachment, a.input, a.output.Stdout)
		default:
			return errors.New("project attachment is not configured")
		}
		if errors.Is(err, tui.ErrGroomingRequested) {
			continue
		}
		if errors.Is(err, tui.ErrInteractiveRequested) {
			// The interactive session (QA chat or feedback loop) owns the
			// terminal; its outcome is shown as the next session's notice.
			// A feedback rerun starts only via g (interview) then r.
			carryNotice = a.runInteractiveSession(ctx, service, &project)
			start = nil
			continue
		}
		if errors.Is(err, tui.ErrConfigureRequested) {
			if attachment.Configure == nil {
				return errors.New("project configuration editing is not configured")
			}
			if configureErr := attachment.Configure(ctx); configureErr != nil {
				if errors.Is(configureErr, errProjectConfigurationCancelled) {
					carryNotice = "Configuration unchanged."
					start = nil
					continue
				}
				return fmt.Errorf("configure project %q: %w", selector, configureErr)
			}
			carryNotice = "Configuration saved. Press r to resume the project."
			start = nil
			continue
		}
		if err != nil {
			return fmt.Errorf("attach project %q: %w", selector, err)
		}
		return nil
	}
}

type skipExecutionService interface {
	SkipFailedExecution(context.Context, string, state.SkipRequest, state.SkipCleanupFunc) (state.ProjectState, error)
}

func skipProjection(project state.ProjectState) (bool, string) {
	if project.Status != state.StatusFailed || len(project.PhaseHistory) == 0 {
		return false, ""
	}
	record := project.PhaseHistory[len(project.PhaseHistory)-1]
	if record.OccurrenceID == "" || record.Status != state.StatusFailed || record.Skip != nil || (record.Outcome != nil && record.Outcome.Canceled) {
		return false, ""
	}
	phase := pipeline.PhaseID(record.Phase)
	if err := orchestrator.ValidateSkipTarget(project, phase, record.Subphase, record.OccurrenceID); err != nil {
		return false, ""
	}
	if record.Phase == string(pipeline.PhaseDevelopment) {
		if planPhase := currentPlanPhase(project); planPhase != "" {
			return true, "Development / " + planPhase + " / Testing"
		}
		return true, "Development / Testing"
	}
	for _, definition := range pipeline.DefaultPipeline().Phases() {
		if definition.ID() == phase {
			return true, definition.Metadata().DisplayName
		}
	}
	return false, ""
}

func currentPlanPhase(project state.ProjectState) string {
	if project.Plan == nil {
		return ""
	}
	completed := make(map[string]struct{}, len(project.Plan.Completed))
	for _, phase := range project.Plan.Completed {
		completed[phase] = struct{}{}
	}
	for _, phase := range project.Plan.Phases {
		if _, ok := completed[phase]; !ok {
			return phase
		}
	}
	return ""
}

func (a *App) skipFailedExecution(ctx context.Context, selector string, skipper skipExecutionService) error {
	project, err := a.projectService(ctx)
	if err != nil {
		return fmt.Errorf("load project service for skip: %w", err)
	}
	current, err := project.Load(ctx, selector)
	if err != nil {
		return fmt.Errorf("load project %q for skip: %w", selector, err)
	}
	if _, label := skipProjection(current); label == "" {
		return fmt.Errorf("skip project %q: no eligible failed execution", selector)
	}
	last := current.PhaseHistory[len(current.PhaseHistory)-1]
	nextPhase, nextSubphase, err := orchestrator.SkipContinuation(current)
	if err != nil {
		return err
	}
	externalIdentity := ""
	if last.Outcome != nil {
		externalIdentity = last.Outcome.ExternalIdentity
	}
	if externalIdentity == "" {
		externalIdentity = current.PullRequestURL
	}
	if _, err := skipper.SkipFailedExecution(ctx, selector, state.SkipRequest{
		OccurrenceID:     last.OccurrenceID,
		NextPhase:        nextPhase,
		NextSubphase:     nextSubphase,
		ExternalIdentity: externalIdentity,
	}, nil); err != nil {
		return fmt.Errorf("skip %s/%s: %w", last.Phase, last.Subphase, err)
	}
	if a.runSpawner != nil {
		return a.startDetached(ctx, selector, []string{"resume", selector})
	}
	return a.resume(ctx, io.Discard, resumeOptions{selector: selector})
}
