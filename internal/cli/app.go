// Package cli contains command parsing and user-facing command wiring for gg.
package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/VedranJanjetovic/gg/internal/agent"
	"github.com/VedranJanjetovic/gg/internal/config"
	"github.com/VedranJanjetovic/gg/internal/git"
	"github.com/VedranJanjetovic/gg/internal/orchestrator"
	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/resume"
	"github.com/VedranJanjetovic/gg/internal/state"
	"github.com/VedranJanjetovic/gg/internal/tui"
	"github.com/VedranJanjetovic/gg/internal/update"
	"github.com/VedranJanjetovic/gg/internal/version"
)

type projectPrompter interface {
	Prompt(context.Context, io.Writer) (orchestrator.ProjectInput, error)
}

type projectEventSink interface {
	Publish(context.Context, orchestrator.Event) error
}

type terminalProjectPrompter struct{ input io.Reader }

// Prompt collects a free-form project description. On a terminal it runs the
// full-screen TUI screen (typed text echoed live, confirmation with the
// inferred project name); piped input falls back to line prompts. The
// description seeds both the goal and the initial acceptance criterion; the
// pipeline's Acceptance criteria phase derives the formal criteria from it.
func (p terminalProjectPrompter) Prompt(ctx context.Context, output io.Writer) (orchestrator.ProjectInput, error) {
	description, err := tui.RunProjectPrompt(ctx, inferredNamePreview, p.input, output)
	if err == nil {
		return projectInputFromDescription(description)
	}
	if !errors.Is(err, tui.ErrPickerNonInteractive) {
		return orchestrator.ProjectInput{}, err
	}
	reader := bufio.NewReader(p.input)
	if _, err := fmt.Fprintln(output, "Describe the project (finish with an empty line):"); err != nil {
		return orchestrator.ProjectInput{}, err
	}
	var lines []string
	for {
		line, readErr := reader.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		if strings.TrimSpace(line) == "" {
			if readErr != nil && len(lines) == 0 {
				return orchestrator.ProjectInput{}, fmt.Errorf("read project description: %w", readErr)
			}
			break
		}
		lines = append(lines, line)
		if readErr != nil {
			break
		}
		if err := ctx.Err(); err != nil {
			return orchestrator.ProjectInput{}, err
		}
	}
	return projectInputFromDescription(strings.Join(lines, "\n"))
}

func projectInputFromDescription(description string) (orchestrator.ProjectInput, error) {
	description = strings.TrimSpace(description)
	if description == "" {
		return orchestrator.ProjectInput{}, errors.New("project description is required")
	}
	return orchestrator.ProjectInput{Goal: description, AcceptanceCriteria: []string{description}}, nil
}

// inferredNamePreview previews the project name the description will produce,
// for display on the TUI confirmation screen.
func inferredNamePreview(description string) string {
	name, err := orchestrator.InferProjectName(orchestrator.ProjectInput{Goal: description})
	if err != nil {
		return ""
	}
	return name
}

type lifecycleService interface {
	Create(context.Context, state.ProjectState) error
	Load(context.Context, string) (state.ProjectState, error)
	List(context.Context) ([]state.ProjectState, error)
	Delete(context.Context, string) error
	Transition(context.Context, string, state.LifecycleStatus, string, string, []string) (state.ProjectState, error)
}

type lockedProjectService interface {
	WithProjectLock(context.Context, string, func(context.Context, state.ProjectState) error) error
}

type projectPruner interface {
	PruneProject(context.Context, string, func(context.Context, state.ProjectState) error) error
}

type runReserver interface {
	ReserveRun(context.Context, string, func(context.Context, state.ProjectState) error) (state.ProjectState, *state.RunReservation, error)
}

type snapshotRunReserver interface {
	ReserveRunWithSnapshot(context.Context, string, state.PipelineConfigSnapshot, int, func(context.Context, state.ProjectState) error) (state.ProjectState, *state.RunReservation, error)
}

type finishedProjectReactivator interface {
	ReactivateFinished(context.Context, string) (state.ProjectState, error)
}

type worktreeService interface {
	RepositoryRoot(context.Context) (string, error)
	LookupWorktree(context.Context, string, string) (git.Worktree, error)
	EnsureWorktree(context.Context, git.WorktreeRequest) (git.Worktree, bool, error)
	RemoveWorktree(context.Context, string, string) error
}

// defaultBranchDetector is the optional git capability used to replace the
// guessed default GitOps parent branch with the repository's actual default.
type defaultBranchDetector interface {
	DefaultBranch(context.Context) string
}

// App owns the command-line surface for gg. It delegates all business behavior to
// internal services so parsing stays isolated from implementation details.
type App struct {
	projectPrompt        projectPrompter
	attacher             ProjectAttacher
	globalRunner         func(context.Context, io.Reader, io.Writer) error
	tui                  TUIRunner
	output               Output
	events               projectEventSink
	input                io.Reader
	cwd                  func() (string, error)
	store                ConfigureStore
	catalogSource        config.AgentCatalogSource
	configurePicker      ConfigurePicker
	projectConfigChooser ProjectConfigurationChooser
	config               *config.Service
	root                 config.RootResolver
	rootInjected         bool
	projects             lifecycleService
	git                  worktreeService
	baseRef              string
	state                StateReader
	pipeline             PipelineService
	controller           orchestrator.Controller
	resumeAll            orchestrator.ResumeCoordinator
	resolved             config.ResolvedConfig
	resolvedSet          bool
	configuredFolderGate bool
	update               updateService
	updateInjected       bool
	version              version.Metadata
	launchActions        *LaunchActions
	questionAsker        QuestionAsker
	questionGenerator    QuestionGenerator
	projectNamer         ProjectNamer
	busyRunner           BusyRunner
	interviewSession     InterviewSession
	runSpawner           RunSpawner
	detachedStartTimeout time.Duration
	detachedPollInterval time.Duration
}

// PipelineService is the lifecycle boundary used by command dispatch.
type PipelineService interface {
	Run(context.Context, pipeline.RunRequest) error
	Stop(context.Context, pipeline.StopRequest) error
	Prune(context.Context) error
}

type updateService interface {
	Update(context.Context) (update.Result, error)
}

// Option customizes App construction, primarily for tests.
type Option func(*App)

// WithInput supplies terminal input for interactive commands.
func WithInput(input io.Reader) Option {
	return func(app *App) { app.input = input; app.projectPrompt = terminalProjectPrompter{input: input} }
}

// WithProjectPrompter injects project input collection for tests and alternate UIs.
func WithProjectPrompter(prompt projectPrompter) Option {
	return func(app *App) { app.projectPrompt = prompt }
}

// WithProjectAttacher supplies the terminal session boundary used by bare gg
// and project shorthand invocations.
func WithProjectAttacher(attacher ProjectAttacher) Option {
	return func(app *App) { app.attacher = attacher }
}

// WithGlobalRunner supplies the bare-gg global observation session.
func WithGlobalRunner(runner func(context.Context, io.Reader, io.Writer) error) Option {
	return func(app *App) { app.globalRunner = runner }
}

// WithTUIRunner injects the terminal attachment implementation.
func WithTUIRunner(runner TUIRunner) Option { return func(app *App) { app.tui = runner } }

// WithLaunchActions injects external-tool actions for attached projects.
func WithLaunchActions(actions *LaunchActions) Option {
	return func(app *App) { app.launchActions = actions }
}

// WithProjectEventSink injects lifecycle event publication.
func WithProjectEventSink(sink projectEventSink) Option { return func(app *App) { app.events = sink } }

// WithOutput supplies default command streams when Run receives nil writers.
func WithOutput(output Output) Option { return func(app *App) { app.output = output } }

// WithVersion supplies build metadata for the version command.
func WithVersion(metadata version.Metadata) Option {
	return func(app *App) { app.version = metadata }
}

// WithUpdateService injects the update boundary for tests and alternate release channels.
func WithUpdateService(service updateService) Option {
	return func(app *App) { app.update = service; app.updateInjected = true }
}

// WithStateReader injects the read-only state service used by list and status.
func WithStateReader(reader StateReader) Option { return func(app *App) { app.state = reader } }

// WithWorkingDirectory supplies current-directory lookup for configuration and state.
func WithWorkingDirectory(cwd func() (string, error)) Option { return func(app *App) { app.cwd = cwd } }

// WithConfigStore supplies configuration persistence for interactive commands.
func WithConfigStore(store ConfigureStore) Option { return func(app *App) { app.store = store } }

// WithAgentCatalogSource injects the agent/model catalog used by configure.
func WithAgentCatalogSource(source config.AgentCatalogSource) Option {
	return func(app *App) { app.catalogSource = source }
}

// WithConfigurePicker injects the interactive agent/model picker.
func WithConfigurePicker(picker ConfigurePicker) Option {
	return func(app *App) { app.configurePicker = picker }
}

// WithProjectConfigurationChooser injects the new-project Inherit/Pick choice.
func WithProjectConfigurationChooser(chooser ProjectConfigurationChooser) Option {
	return func(app *App) { app.projectConfigChooser = chooser }
}

// WithPipelineService supplies workflow dispatch, primarily for tests.
func WithPipelineService(service PipelineService) Option {
	return func(app *App) { app.pipeline = service }
}

// WithOrchestratorController supplies ordered lifecycle execution for project-aware
// run, stop, and resume commands. The legacy pipeline seam remains available for
// callers that only need the original command behavior.
func WithOrchestratorController(controller orchestrator.Controller) Option {
	return func(app *App) { app.controller = controller }
}

// WithController is a concise compatibility alias for WithOrchestratorController.
func WithController(controller orchestrator.Controller) Option {
	return WithOrchestratorController(controller)
}

// WithResumeCoordinator injects restart-safe all-project resume.
func WithResumeCoordinator(coordinator orchestrator.ResumeCoordinator) Option {
	return func(app *App) { app.resumeAll = coordinator }
}

// WithResolvedConfig injects an already-resolved configuration at the application
// boundary, keeping controller tests independent of configuration files.
func WithResolvedConfig(resolved config.ResolvedConfig) Option {
	return func(app *App) { app.resolved = resolved; app.resolvedSet = true }
}

// WithRootResolver injects configured-folder resolution without coupling the CLI
// to the full configuration persistence/workflow.
func WithRootResolver(resolver config.RootResolver) Option {
	return func(app *App) { app.root = resolver; app.rootInjected = true }
}

// WithConfiguredFolderGate enables production validation of the resolver-selected
// folder. Compatibility callers that inject a root continue to bypass this gate.
func WithConfiguredFolderGate() Option {
	return func(app *App) { app.configuredFolderGate = true }
}

// WithLifecycleService injects the authoritative project state service.
func WithLifecycleService(service lifecycleService) Option {
	return func(app *App) { app.projects = service }
}

func WithGitClient(client worktreeService) Option {
	return func(app *App) { app.git = client }
}

func WithBaseRef(ref string) Option {
	return func(app *App) { app.baseRef = ref }
}

// New constructs a gg CLI application with default no-op services.
func New(options ...Option) *App {
	app := &App{
		input:         os.Stdin,
		output:        Output{Stdout: os.Stdout, Stderr: os.Stderr},
		cwd:           os.Getwd,
		store:         config.NewStore(),
		config:        config.NewService(),
		catalogSource: agent.NewCatalogSource(nil),
		state:         state.NewService(),
		pipeline:      pipeline.NewService(),
		update:        update.NewService(),
		version:       version.Current(),
		baseRef:       "HEAD",
	}
	app.root = app.config
	for _, option := range options {
		option(app)
	}
	if !app.updateInjected {
		app.update = update.NewServiceWithDependencies(func() string { return app.version.Version }, nil)
	}
	return app
}

// Run executes gg and returns a process exit code.
func (a *App) Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if stdout == nil {
		stdout = a.output.Stdout
	}
	if stderr == nil {
		stderr = a.output.Stderr
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	if err := a.run(ctx, args, stdout); err != nil {
		fmt.Fprintf(stderr, "gg: %v\n", err)
		return 1
	}
	return 0
}

func (a *App) run(ctx context.Context, args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("gg", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configure := flags.Bool("configure", false, "open the interactive configuration workflow")
	help := flags.Bool("help", false, "show help")
	showVersion := flags.Bool("version", false, "show version")
	flags.BoolVar(help, "h", false, "show help")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("parse arguments: %w", err)
	}
	if *showVersion {
		if *help || flags.NArg() > 0 {
			return errors.New("--version does not accept arguments")
		}
		return writeVersion(stdout, a.version)
	}
	if *configure {
		if flags.NArg() > 0 {
			return fmt.Errorf("--configure cannot be combined with subcommand %q", flags.Arg(0))
		}
		if *help {
			writeCommandHelp(stdout, "configure")
			return nil
		}
		return a.configure(ctx, stdout)
	}
	if *help {
		writeTopLevelHelp(stdout)
		return nil
	}
	if flags.NArg() == 0 {
		if a.globalRunner != nil {
			return a.globalRunner(ctx, a.input, stdout)
		}
		return a.createAndAttach(ctx, stdout)
	}
	command := flags.Arg(0)
	commandArgs := flags.Args()[1:]
	if _, ok := commandDescriptions[command]; !ok {
		if len(commandArgs) != 0 {
			return fmt.Errorf("unknown command %q", command)
		}
		return a.attachExisting(ctx, command)
	}
	if hasCommandHelp(command, commandArgs) {
		writeCommandHelp(stdout, command)
		return nil
	}
	switch command {
	case "version":
		if err := rejectArgs(command, commandArgs); err != nil {
			return err
		}
		return writeVersion(stdout, a.version)
	case "configure":
		if err := rejectArgs(command, commandArgs); err != nil {
			return err
		}
		return a.configure(ctx, stdout)
	case "list":
		return a.listCommand(ctx, stdout, commandArgs)
	case "status":
		return a.statusCommand(ctx, stdout, commandArgs)
	case "run":
		options, err := parseRunOptions(commandArgs)
		if err != nil {
			return err
		}
		return a.runPipeline(ctx, stdout, options)
	case "resume":
		options, err := parseResumeOptions(commandArgs)
		if err != nil {
			return err
		}
		return a.resume(ctx, stdout, options)
	case "stop":
		if err := a.requireConfiguredProject(); err != nil {
			return err
		}
		return a.stop(ctx, stdout, commandArgs)
	case "stop-all":
		if err := rejectArgs(command, commandArgs); err != nil {
			return err
		}
		return a.stopAll(ctx, stdout)
	case "prune":
		pruneArgs, yes, err := ParseConfirmation(commandArgs)
		if err != nil {
			return err
		}
		if err := rejectArgs(command, pruneArgs); err != nil {
			return err
		}
		if err := a.requireConfiguredProject(); err != nil {
			return err
		}
		return a.prune(ctx, stdout, yes)
	case "update":
		if err := rejectArgs(command, commandArgs); err != nil {
			return err
		}
		return a.updateCLI(ctx, stdout)
	case "usage":
		return a.usageCommand(ctx, stdout, commandArgs)
	case "remove":
		return a.removeCommand(ctx, stdout, commandArgs)
	}
	return fmt.Errorf("unknown command %q", command)
}

func (a *App) requireConfiguredProject() error {
	if a.rootInjected && !a.configuredFolderGate {
		return nil
	}
	if !a.configuredFolderGate {
		_, err := ResolveConfiguredFolder(context.Background(), a.cwd, a.store)
		return err
	}
	root, err := a.root.ConfiguredRoot(context.Background())
	if err != nil {
		return fmt.Errorf("resolve configured root: %w", err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve current folder %q: %w", root, err)
	}
	if a.store == nil {
		return errors.New("check project configuration: configuration store is not configured")
	}
	if _, err := a.store.LoadProject(root); err != nil {
		if errors.Is(err, config.ErrProjectNotConfigured) {
			return fmt.Errorf("current folder is not configured; run \"gg configure\" in %s", root)
		}
		return fmt.Errorf("check project configuration in %s: %w", root, err)
	}
	return nil
}

func (a *App) configure(ctx context.Context, stdout io.Writer) error {
	input := a.input
	if input == nil {
		input = os.Stdin
	}
	if a.store == nil {
		return errors.New("configure: configuration store is not configured")
	}
	if a.configurePicker == nil && input == os.Stdin {
		if info, statErr := os.Stdin.Stat(); statErr == nil {
			if nullInfo, nullErr := os.Stat(os.DevNull); nullErr == nil && os.SameFile(info, nullInfo) {
				_, writeErr := fmt.Fprintln(stdout, "Configuration workflow is not implemented yet.")
				return writeErr
			}
		}
	}
	workflow := NewConfigureWorkflowWithPicker(input, stdout, a.cwd, a.store, a.catalogSource, a.configurePicker)
	if err := workflow.Run(ctx); err != nil {
		// New() historically served parser/skeleton callers without a picker. Keep
		// that narrow no-input compatibility path; production composition injects
		// the picker and reports EOF/cancellation to the caller.
		if a.configurePicker == nil && strings.Contains(err.Error(), "configuration cancelled: input ended before completion") {
			_, writeErr := fmt.Fprintln(stdout, "Configuration workflow is not implemented yet.")
			return writeErr
		}
		return err
	}
	return nil
}

// Projects exposes durable project state to callers while keeping the lifecycle
// service authoritative for classification and persistence.
func (a *App) Projects(ctx context.Context) ([]state.ProjectState, error) {
	service, err := a.projectService(ctx)
	if err != nil {
		return nil, err
	}
	projects, err := service.List(ctx)
	if err != nil {
		return nil, err
	}
	// A "running" project whose owning process died must never be reported
	// as running: repair it to a resumable stopped state, exactly like
	// attach and the global view do, so list/status agree with them.
	if recoverer, ok := service.(staleRunRecoverer); ok {
		for i, project := range projects {
			if project.Status != state.StatusRunning {
				continue
			}
			if recovered, changed, recoverErr := recoverer.RecoverIfStale(ctx, project.Slug); recoverErr == nil && changed {
				projects[i] = recovered
			}
		}
	}
	return projects, nil
}

func (a *App) runPipeline(ctx context.Context, stdout io.Writer, options runOptions) error {
	if err := a.requireConfiguredProject(); err != nil {
		return err
	}
	var root string
	var err error
	resolved := a.resolved
	if !a.resolvedSet && (!a.rootInjected || a.controller != nil) {
		if a.rootInjected {
			root, err = a.root.ConfiguredRoot(ctx)
			if err != nil {
				return fmt.Errorf("resolve configured root: %w", err)
			}
		} else {
			root, err = a.cwd()
			if err != nil {
				return fmt.Errorf("determine current folder: %w", err)
			}
		}
		root, err = filepath.Abs(root)
		if err != nil {
			return fmt.Errorf("resolve current folder %q: %w", root, err)
		}
		resolved, err = config.ResolvePipelineConfig(a.store, root, options.overrides)
		if err != nil {
			if errors.Is(err, config.ErrProjectNotConfigured) {
				return fmt.Errorf("current folder is not configured; run \"gg configure\" in %s", root)
			}
			return err
		}
		if !resolved.GitOps.Configured && options.overrides.GitOps.ParentBranch == "" {
			// The unconfigured parent branch is a guess ("main"); replace it
			// with the repository's actual default branch so rebase and PR
			// never target a branch the repository does not have.
			if client, clientErr := a.gitClient(ctx); clientErr == nil {
				if detector, ok := client.(defaultBranchDetector); ok {
					if branch := detector.DefaultBranch(ctx); branch != "" {
						resolved.GitOps.ParentBranch = branch
					}
				}
			}
		}
	}
	var plan pipeline.ExecutablePipeline
	if !a.rootInjected || a.controller != nil {
		plan, err = pipeline.Resolve(pipeline.DefaultPipeline(), resolved)
		if err != nil {
			return fmt.Errorf("resolve pipeline: %w", err)
		}
	}
	subphases := pipeline.DevelopmentSubphaseGeneration{}
	var executionSnapshot state.PipelineConfigSnapshot
	if a.controller != nil {
		executionSnapshot, err = pipeline.SnapshotExecution(plan, subphases, options.maxIterations, resolved.GitOps)
		if err != nil {
			return fmt.Errorf("snapshot executable pipeline: %w", err)
		}
	}
	rawSelector := projectSelector(options.args)
	// Creating a new project on a terminal attaches the live TUI so pipeline
	// progress is visible; the pipeline itself starts via the attachment.
	// Piped callers keep the headless run so scripted input stays scriptable.
	if rawSelector == "" && a.projectPrompt != nil && a.attacher != nil && tui.InteractiveTerminal(a.input, stdout) {
		return a.createAndAttachWithOptions(ctx, stdout, options)
	}
	var selector, worktreePath string
	var resumeExisting bool
	if rawSelector == "" && a.projectPrompt != nil {
		selector, err = a.createProject(ctx, stdout, options.maxIterations)
		if err != nil {
			return fmt.Errorf("create project: %w", err)
		}
		worktreePath, err = a.projectWorktreePath(ctx, selector)
		if err != nil {
			return fmt.Errorf("load created project %q: %w", selector, err)
		}
	}
	var reservation *state.RunReservation
	finishedRerun := false
	if rawSelector != "" {
		selector, err = a.prepareProject(ctx, rawSelector)
		if err != nil {
			return fmt.Errorf("prepare project %q: %w", rawSelector, err)
		}
	}
	// Explicit selectors and interactive first-run creation share one project
	// load. Create persists pending state; the controller path additionally
	// reserves it before BeginRun so durable execution has a running identity.
	if selector != "" {
		projectService, serviceErr := a.projectService(ctx)
		if serviceErr != nil {
			return fmt.Errorf("load project service: %w", serviceErr)
		}
		projectState, loadErr := projectService.Load(ctx, selector)
		if loadErr != nil {
			return fmt.Errorf("load project %q: %w", selector, loadErr)
		}
		if projectState.Status == state.StatusStopped || projectState.Status == state.StatusFailed {
			projectState, err = resume.Prepare(ctx, projectState, projectService)
			if err != nil {
				return fmt.Errorf("prepare project %q for resume: %w", selector, err)
			}
		}
		worktreePath = projectState.WorktreePath
		resumeExisting = projectState.Status == state.StatusStopped || projectState.Status == state.StatusFailed
		// A newly created project owns its complete snapshot before Grooming
		// starts. Restore it here so ambient folder/global changes cannot replace
		// the user's Inherit/Pick decision on the first execution.
		if !resumeExisting && projectState.Status == state.StatusPending && hasPersistedExecutionSnapshot(projectState.PipelineConfig) {
			persistedPlan, persistedSubphases, persistedMax, restoreErr := pipeline.RestoreExecution(projectState.PipelineConfig)
			if restoreErr != nil {
				return fmt.Errorf("restore project %q creation snapshot: %w", selector, restoreErr)
			}
			plan, subphases, options.maxIterations = persistedPlan, persistedSubphases, persistedMax
			executionSnapshot = projectState.PipelineConfig
		}
		if projectConfig, configErr := pipeline.RestoreResolvedConfiguration(projectState.PipelineConfig); configErr == nil {
			// The project snapshot is authoritative for both controller and
			// legacy pipeline-service dispatches.
			resolved = projectConfig
		}
		if resumeExisting {
			_, _, _, restoreErr := pipeline.RestoreExecution(projectState.PipelineConfig)
			resumeExisting = restoreErr == nil
		}
		if projectState.Status == state.StatusFinished {
			reactivator, ok := projectService.(finishedProjectReactivator)
			if !ok {
				return fmt.Errorf("project service does not support finished-project rerun")
			}
			if _, _, _, err := pipeline.RestoreExecution(projectState.PipelineConfig); err != nil {
				return fmt.Errorf("validate finished project %q execution snapshot: %w", selector, err)
			}
			projectState, err = reactivator.ReactivateFinished(ctx, selector)
			if err != nil {
				return fmt.Errorf("reactivate finished project %q: %w", selector, err)
			}
			finishedRerun = true
		}
		if a.controller != nil && !resumeExisting && !finishedRerun {
			reserver, ok := projectService.(snapshotRunReserver)
			if !ok {
				return fmt.Errorf("project service does not support atomic execution snapshot reservation")
			}
			projectState, reservation, err = reserver.ReserveRunWithSnapshot(ctx, selector, executionSnapshot, options.maxIterations, a.validateProjectWorktreeState)
			if err != nil {
				return fmt.Errorf("reserve project %q: %w", selector, err)
			}
			worktreePath = projectState.WorktreePath
		}
	}
	if a.controller != nil {
		projectService, serviceErr := a.projectService(ctx)
		if serviceErr != nil {
			return fmt.Errorf("load project service: %w", serviceErr)
		}
		project, loadErr := projectService.Load(ctx, selector)
		if loadErr != nil {
			return fmt.Errorf("load project %q: %w", selector, loadErr)
		}
		runID := fmt.Sprintf("%s-%d", selector, time.Now().UnixNano())
		var execErr error
		if finishedRerun {
			var rerunErr error
			plan, subphases, options.maxIterations, rerunErr = finishedRerunPlan(project.PipelineConfig)
			if rerunErr != nil {
				return fmt.Errorf("run finished project %q from persisted execution snapshot: %w", selector, rerunErr)
			}
			_, execErr = a.controller.Execute(ctx, orchestrator.Request{
				Project: project, Pipeline: plan, PhaseContracts: plan.PhaseContracts(), Subphases: subphases,
				MaxIterations: options.maxIterations, RunID: runID, GitOps: snapshotGitOps(project.PipelineConfig), RepairExistingVerification: options.repairExistingVerification,
				ArtifactRoot: root, PullRequestURL: project.PullRequestURL,
			})
		} else if resumeExisting {
			resumePlan, resumeSubphases, resumeMaxIterations, restoreErr := pipeline.RestoreExecution(project.PipelineConfig)
			if restoreErr != nil {
				return fmt.Errorf("run project %q from persisted execution snapshot: %w", selector, restoreErr)
			}
			_, execErr = a.controller.Resume(ctx, orchestrator.ResumeRequest{ProjectSlug: selector, RunID: runID, Execution: orchestrator.Request{
				Project:                    project,
				Pipeline:                   resumePlan,
				PhaseContracts:             resumePlan.PhaseContracts(),
				Subphases:                  resumeSubphases,
				MaxIterations:              resumeMaxIterations,
				RunID:                      runID,
				GitOps:                     snapshotGitOps(project.PipelineConfig),
				ArtifactRoot:               root,
				RepairExistingVerification: options.repairExistingVerification,
				PullRequestURL:             project.PullRequestURL,
			}})
		} else {
			_, execErr = a.controller.Execute(ctx, orchestrator.Request{
				Project:                    project,
				Pipeline:                   plan,
				PhaseContracts:             plan.PhaseContracts(),
				Subphases:                  subphases,
				MaxIterations:              options.maxIterations,
				RunID:                      runID,
				GitOps:                     resolved.GitOps,
				RepairExistingVerification: options.repairExistingVerification,
				ArtifactRoot:               root,
			})
		}
		if execErr != nil {
			if reservation != nil {
				if rollbackErr := reservation.Rollback(context.WithoutCancel(ctx)); rollbackErr != nil {
					return fmt.Errorf("run project %q: %w (rollback: %v)", selector, execErr, rollbackErr)
				}
			}
			return fmt.Errorf("run project %q: %w", selector, execErr)
		}
		message := "Run workflow completed."
		if resumeExisting {
			message = "Run workflow resumed."
		}
		if finishedRerun {
			message = "Finished project PR/CI workflow rerun completed."
		}
		if _, err = fmt.Fprintln(stdout, message); err != nil {
			return err
		}
		if completed, loadErr := projectService.Load(ctx, selector); loadErr == nil && (state.VerificationHasWarnings(completed) || len(state.VerificationDisplay(completed)) > 0) {
			return writeVerificationSummary(stdout, completed, "Verification summary")
		}
		return nil
	}
	request := pipeline.RunRequest{Args: append([]string(nil), options.args...), Config: resolved, WorktreePath: worktreePath}
	if err := a.pipeline.Run(ctx, request); err != nil {
		if reservation != nil {
			if rollbackErr := reservation.Rollback(context.WithoutCancel(ctx)); rollbackErr != nil {
				return fmt.Errorf("run pipeline: %w (rollback project %q: %v)", err, selector, rollbackErr)
			}
		}
		return fmt.Errorf("run pipeline: %w", err)
	}
	if selector != "" && reservation == nil {
		if err := a.ensureRunning(ctx, selector); err != nil {
			return fmt.Errorf("run project %q: %w", selector, err)
		}
	}
	_, err = fmt.Fprintln(stdout, "Run workflow is not implemented yet.")
	return err
}

func finishedRerunPlan(snapshot state.PipelineConfigSnapshot) (pipeline.ExecutablePipeline, pipeline.DevelopmentSubphaseGeneration, int, error) {
	plan, subphases, maxIterations, err := pipeline.RestoreExecution(snapshot)
	if err != nil {
		return pipeline.ExecutablePipeline{}, pipeline.DevelopmentSubphaseGeneration{}, 0, err
	}
	plan = plan.GitOpsOnly()
	if len(plan.Phases()) == 0 {
		return pipeline.ExecutablePipeline{}, pipeline.DevelopmentSubphaseGeneration{}, 0, errors.New("finished project has no enabled PR/CI monitoring phases")
	}
	return plan, subphases, maxIterations, nil
}

func snapshotGitOps(snapshot state.PipelineConfigSnapshot) config.GitOpsConfig {
	gitOps, err := pipeline.SnapshotGitOps(snapshot)
	if err != nil {
		return config.DefaultGitOpsConfig()
	}
	return gitOps
}

func hasPersistedExecutionSnapshot(snapshot state.PipelineConfigSnapshot) bool {
	return snapshot.SchemaVersion > 0 && len(bytes.TrimSpace(snapshot.Data)) > 0 && !bytes.Equal(bytes.TrimSpace(snapshot.Data), []byte("{}"))
}

func (a *App) resume(ctx context.Context, stdout io.Writer, options resumeOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	selector, err := canonicalProjectSelector([]string{options.selector})
	if err != nil {
		return fmt.Errorf("resume: %w", err)
	}
	if selector == "" {
		if a.resumeAll == nil {
			return errors.New("resume requires a project selector")
		}
		results, resumeErr := a.resumeAll.ResumeAll(ctx, orchestrator.ResumeAllRequest{RepairExistingVerification: options.repairExistingVerification})
		if _, err := fmt.Fprintf(stdout, "Resumed %d project(s).\n", countSuccessfulResumes(results)); err != nil {
			return err
		}
		if err := a.writeResumeVerificationSummaries(ctx, stdout, results); err != nil {
			return err
		}
		return resumeErr
	}
	if err := a.requireConfiguredProject(); err != nil {
		return err
	}
	if err := a.verifyProjectWorktree(ctx, selector); err != nil {
		return fmt.Errorf("resume project %q: %w", selector, err)
	}
	if a.controller != nil {
		projectService, serviceErr := a.projectService(ctx)
		if serviceErr != nil {
			return fmt.Errorf("load project service: %w", serviceErr)
		}
		project, loadErr := projectService.Load(ctx, selector)
		if loadErr != nil {
			return fmt.Errorf("load project %q: %w", selector, loadErr)
		}
		project, loadErr = a.repairCurrentPhaseConfiguration(ctx, selector, project)
		if loadErr != nil {
			return fmt.Errorf("repair project %q configuration: %w", selector, loadErr)
		}
		project, err = resume.Prepare(ctx, project, projectService)
		if err != nil {
			return fmt.Errorf("prepare project %q for resume: %w", selector, err)
		}
		plan, subphases, maxAttempts, planErr := pipeline.RestoreExecution(project.PipelineConfig)
		gitOps := snapshotGitOps(project.PipelineConfig)
		if planErr != nil {
			return fmt.Errorf("resume project %q from persisted execution snapshot: %w", selector, planErr)
		}
		artifactRoot, rootErr := a.root.ConfiguredRoot(ctx)
		if rootErr != nil {
			return fmt.Errorf("resolve configured root: %w", rootErr)
		}
		_, resumeErr := a.controller.Resume(ctx, orchestrator.ResumeRequest{ProjectSlug: selector, Execution: orchestrator.Request{
			Project:                    project,
			Pipeline:                   plan,
			PhaseContracts:             plan.PhaseContracts(),
			Subphases:                  subphases,
			MaxIterations:              maxAttempts,
			GitOps:                     gitOps,
			ArtifactRoot:               artifactRoot,
			RepairExistingVerification: options.repairExistingVerification,
			PullRequestURL:             project.PullRequestURL,
		}})
		if resumeErr != nil {
			return fmt.Errorf("resume project %q: %w", selector, resumeErr)
		}
		if _, err = fmt.Fprintln(stdout, "Resume workflow completed."); err != nil {
			return err
		}
		if completed, loadErr := projectService.Load(ctx, selector); loadErr == nil && (state.VerificationHasWarnings(completed) || len(state.VerificationDisplay(completed)) > 0) {
			return writeVerificationSummary(stdout, completed, "Verification summary")
		}
		return nil
	}
	if err := a.transitionProject(ctx, selector, state.StatusRunning); err != nil {
		return fmt.Errorf("resume project %q: %w", selector, err)
	}
	_, err = fmt.Fprintln(stdout, "Run workflow is not implemented yet.")
	return err
}

func countSuccessfulResumes(results []orchestrator.ResumeResult) int {
	count := 0
	for _, result := range results {
		if result.Kind == state.RerunResume && result.Err == nil {
			count++
		}
	}
	return count
}

func (a *App) writeResumeVerificationSummaries(ctx context.Context, output io.Writer, results []orchestrator.ResumeResult) error {
	if a.projects == nil {
		return nil
	}
	for _, result := range results {
		if result.Err != nil {
			continue
		}
		project, err := a.projects.Load(ctx, result.ProjectSlug)
		if err != nil || (len(state.VerificationDisplay(project)) == 0 && !state.VerificationHasWarnings(project)) {
			continue
		}
		if err := writeVerificationSummary(output, project, "Verification summary for "+displayValue(project.Name)); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) stop(ctx context.Context, stdout io.Writer, args []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	selector, err := canonicalProjectSelector(args)
	if err != nil {
		return fmt.Errorf("stop: %w", err)
	}
	controllerStop := a.controller != nil
	if a.controller != nil {
		if selector == "" {
			return errors.New("stop requires a project selector")
		}
		if err := a.controller.Stop(ctx, orchestrator.StopRequest{ProjectSlug: selector}); err != nil {
			return fmt.Errorf("stop project %q: %w", selector, err)
		}
	} else {
		request := pipeline.StopRequest{Args: append([]string(nil), args...)}
		if err := a.pipeline.Stop(ctx, request); err != nil {
			return fmt.Errorf("stop pipeline: %w", err)
		}
		if selector != "" {
			if err := a.transitionProject(ctx, selector, state.StatusStopped); err != nil {
				return fmt.Errorf("stop project %q: %w", selector, err)
			}
		}
	}
	message := "Stop workflow is not implemented yet."
	if controllerStop {
		message = "Stop workflow requested."
	}
	_, err = fmt.Fprintln(stdout, message)
	return err
}

func (a *App) stopAll(ctx context.Context, stdout io.Writer) error {
	if a.controller == nil {
		return errors.New("stop-all requires the production orchestrator")
	}
	projects, err := a.projectService(ctx)
	if err != nil {
		return fmt.Errorf("stop-all: %w", err)
	}
	result, stopErr := orchestrator.StopAll(ctx, projects, a.controller)
	if _, err := fmt.Fprintln(stdout, result.Summary()); err != nil {
		return err
	}
	return stopErr
}

func (a *App) createProject(ctx context.Context, stdout io.Writer, maxQAAttempts int) (string, error) {
	creation, err := a.chooseNewProjectConfiguration(ctx, stdout, maxQAAttempts)
	if err != nil {
		return "", err
	}
	input, err := a.projectPrompt.Prompt(ctx, stdout)
	if err != nil {
		return "", err
	}
	if err := orchestrator.ValidateProjectInput(input); err != nil {
		return "", err
	}
	name, err := a.resolveProjectName(ctx, input)
	if err != nil {
		return "", err
	}
	return a.prepareProjectWithInput(ctx, name, input, creation.snapshot)
}

func (a *App) projectWorktreePath(ctx context.Context, selector string) (string, error) {
	service, err := a.projectService(ctx)
	if err != nil {
		return "", err
	}
	project, err := service.Load(ctx, selector)
	if err != nil {
		return "", err
	}
	return project.WorktreePath, nil
}

func (a *App) prepareProjectWithInput(ctx context.Context, displayName string, input orchestrator.ProjectInput, snapshot state.PipelineConfigSnapshot) (string, error) {
	slug, err := git.ProjectSlug(displayName)
	if err != nil {
		return "", fmt.Errorf("normalize inferred project name: %w", err)
	}
	service, err := a.projectService(ctx)
	if err != nil {
		return "", err
	}
	if _, err := service.Load(ctx, slug); err == nil {
		return "", fmt.Errorf("project %q already exists", slug)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	stateRoot, err := a.root.ConfiguredRoot(ctx)
	if err != nil {
		return "", err
	}
	client := a.git
	if client == nil {
		client = git.NewClient(stateRoot, nil)
	}
	worktree, created, gitDisabled, err := a.prepareProjectWorkspace(ctx, client, stateRoot, slug)
	if err != nil {
		return "", err
	}
	project := newProjectState(displayName, slug, input.Goal, input.AcceptanceCriteria, worktree.Path, worktree.Branch, snapshot)
	project.GitDisabled = gitDisabled
	// New projects owe the user a grooming interview before the pipeline
	// starts; it runs (and can be re-entered) through the attach flow.
	project.Interview = &state.InterviewState{}
	createdAt := time.Now().UTC()
	project.CreatedAt, project.UpdatedAt, project.StatusChangedAt = createdAt, createdAt, createdAt
	if err := service.Create(ctx, project); err != nil {
		if created && !errors.Is(err, state.ErrProjectExists) {
			_ = client.RemoveWorktree(ctx, worktree.Path, worktree.Branch)
		}
		return "", err
	}
	if a.events != nil {
		if err := a.events.Publish(ctx, orchestrator.Event{ProjectSlug: slug, Type: orchestrator.EventProjectCreated, At: project.CreatedAt}); err != nil {
			return "", fmt.Errorf("publish project_created event: %w", err)
		}
	}
	return slug, nil
}

func (a *App) prepareProject(ctx context.Context, displayName string) (string, error) {
	slug, err := git.ProjectSlug(displayName)
	if err != nil {
		return "", fmt.Errorf("normalize project selector: %w", err)
	}
	service, err := a.projectService(ctx)
	if err != nil {
		return "", err
	}
	if _, err := service.Load(ctx, slug); err == nil {
		validate := func(locked context.Context, project state.ProjectState) error {
			return a.validateProjectWorktreeState(locked, project)
		}
		if lockedService, ok := service.(lockedProjectService); ok {
			return slug, lockedService.WithProjectLock(ctx, slug, validate)
		}
		project, loadErr := service.Load(ctx, slug)
		if loadErr != nil {
			return "", loadErr
		}
		return slug, validate(ctx, project)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	stateRoot, err := a.root.ConfiguredRoot(ctx)
	if err != nil {
		return "", err
	}
	client := a.git
	if client == nil {
		client = git.NewClient(stateRoot, nil)
	}
	worktree, created, gitDisabled, err := a.prepareProjectWorkspace(ctx, client, stateRoot, slug)
	if err != nil {
		return "", err
	}
	project := newProjectState(displayName, slug, "Run gg pipeline for "+displayName, []string{"pipeline lifecycle is recorded"}, worktree.Path, worktree.Branch)
	project.GitDisabled = gitDisabled
	createdAt := time.Now().UTC()
	project.CreatedAt, project.UpdatedAt, project.StatusChangedAt = createdAt, createdAt, createdAt
	if err := service.Create(ctx, project); err != nil {
		if created && !errors.Is(err, state.ErrProjectExists) {
			if cleanupErr := client.RemoveWorktree(ctx, worktree.Path, worktree.Branch); cleanupErr != nil {
				return "", fmt.Errorf("%w (cleanup worktree: %v)", err, cleanupErr)
			}
		}
		return "", err
	}
	if a.events != nil {
		if err := a.events.Publish(ctx, orchestrator.Event{ProjectSlug: slug, Type: orchestrator.EventProjectCreated, At: createdAt}); err != nil {
			return "", fmt.Errorf("publish project_created event: %w", err)
		}
	}
	return slug, nil
}

func (a *App) ensureRunning(ctx context.Context, selector string) error {
	service, err := a.projectService(ctx)
	if err != nil {
		return err
	}
	project, err := service.Load(ctx, selector)
	if err != nil {
		return err
	}
	if project.Status == state.StatusRunning {
		return nil
	}
	_, err = service.Transition(ctx, selector, state.StatusRunning, "pipeline", "run", nil)
	return err
}

func (a *App) transitionProject(ctx context.Context, selector string, target state.LifecycleStatus) error {
	service, err := a.projectService(ctx)
	if err != nil {
		return err
	}
	if _, err := service.Load(ctx, selector); err != nil {
		return fmt.Errorf("load project: %w", err)
	}
	_, err = service.Transition(ctx, selector, target, "pipeline", string(target), nil)
	return err
}

func (a *App) executionPlan(ctx context.Context, overrides config.RunOverrides) (pipeline.ExecutablePipeline, error) {
	resolved := a.resolved
	if !a.resolvedSet {
		root, err := a.root.ConfiguredRoot(ctx)
		if err != nil {
			return pipeline.ExecutablePipeline{}, fmt.Errorf("resolve configured root: %w", err)
		}
		resolved, err = config.ResolvePipelineConfig(a.store, root, overrides)
		if err != nil {
			return pipeline.ExecutablePipeline{}, err
		}
	}
	return pipeline.Resolve(pipeline.DefaultPipeline(), resolved)
}

func (a *App) projectService(ctx context.Context) (lifecycleService, error) {
	if a.projects != nil {
		return a.projects, nil
	}
	root, err := a.root.ConfiguredRoot(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve configured root: %w", err)
	}
	store, err := state.NewFileStore(root)
	if err != nil {
		return nil, fmt.Errorf("open project state store: %w", err)
	}
	a.projects = state.NewLifecycleService(store, nil, store.Locker())
	return a.projects, nil
}

// prepareProjectWorkspace resolves where a new project executes. Inside a git
// repository each project owns a disposable worktree on its own branch; a
// plain (non-git) folder is used directly — no worktree, no branch — and
// gitDisabled marks the project so every git-dependent behavior (commit
// enforcement, proof uncommitted checks, rebase/PR/CI) is skipped.
func (a *App) prepareProjectWorkspace(ctx context.Context, client worktreeService, stateRoot, slug string) (worktree git.Worktree, created, gitDisabled bool, err error) {
	repoRoot, repoErr := client.RepositoryRoot(ctx)
	if repoErr != nil {
		folder, absErr := filepath.Abs(stateRoot)
		if absErr != nil {
			return git.Worktree{}, false, false, fmt.Errorf("resolve configured folder: %w", absErr)
		}
		return git.Worktree{Path: folder}, false, true, nil
	}
	naming, err := git.ProjectNamingForSlug(repoRoot, slug)
	if err != nil {
		return git.Worktree{}, false, false, fmt.Errorf("name project worktree: %w", err)
	}
	worktree, created, err = client.EnsureWorktree(ctx, git.WorktreeRequest{Path: naming.WorktreePath, Branch: naming.BranchName, BaseRef: a.baseRef})
	if err != nil {
		return git.Worktree{}, false, false, fmt.Errorf("ensure project worktree: %w", err)
	}
	return worktree, created, false, nil
}

func newProjectState(displayName, slug, goal string, criteria []string, worktreePath, branchName string, snapshots ...state.PipelineConfigSnapshot) state.ProjectState {
	snapshot := state.PipelineConfigSnapshot{SchemaVersion: 1, Data: json.RawMessage(`{}`)}
	if len(snapshots) > 0 {
		snapshot = snapshots[0]
	}
	return state.ProjectState{
		Name: displayName, Slug: slug, OriginalGoal: goal,
		AcceptanceCriteria: append([]string(nil), criteria...),
		PipelineConfig:     snapshot,
		CurrentPhase:       "pipeline", WorktreePath: filepath.Clean(worktreePath), BranchName: branchName,
	}
}

func canonicalProjectSelector(args []string) (string, error) { return FirstProjectSelector(args) }

func projectSelector(args []string) string {
	for _, arg := range args {
		if arg != "" && !strings.HasPrefix(arg, "-") {
			return arg
		}
	}
	return ""
}

func (a *App) prune(ctx context.Context, stdout io.Writer, yes bool) error {
	service, err := a.projectService(ctx)
	if err != nil {
		return fmt.Errorf("prune projects: %w", err)
	}
	projects, err := service.List(ctx)
	if err != nil {
		return fmt.Errorf("list projects for prune: %w", err)
	}
	// Prune sweeps only done (finished/terminated) projects; removing a
	// specific failed or stopped project is the explicit `gg remove` command.
	candidates := make([]state.ProjectState, 0, len(projects))
	for _, project := range projects {
		if project.Status.IsTerminal() {
			candidates = append(candidates, project)
		}
	}
	if !yes {
		if tui.InteractiveTerminal(a.input, stdout) && len(candidates) > 0 {
			items := make([]tui.PruneItem, 0, len(candidates))
			for _, project := range candidates {
				items = append(items, tui.PruneItem{Slug: project.Slug, Name: project.Name, Status: string(project.Status), Selected: true})
			}
			selected, pickErr := tui.RunPrunePrompt(ctx, items, a.input, stdout)
			switch {
			case errors.Is(pickErr, tui.ErrPickerNonInteractive):
				// Fall through to the line prompt below.
			case pickErr != nil:
				return fmt.Errorf("prune selection: %w", pickErr)
			case len(selected) == 0:
				_, err := fmt.Fprintln(stdout, "Prune cancelled.")
				return err
			default:
				chosen := make(map[string]bool, len(selected))
				for _, slug := range selected {
					chosen[slug] = true
				}
				kept := candidates[:0]
				for _, project := range candidates {
					if chosen[project.Slug] {
						kept = append(kept, project)
					}
				}
				candidates = kept
				yes = true
			}
		}
	}
	if !yes {
		confirmed, err := a.confirmPrune(ctx, stdout, len(candidates))
		if err != nil {
			return err
		}
		if !confirmed {
			_, err := fmt.Fprintln(stdout, "Prune cancelled.")
			return err
		}
	}
	if err := a.pipeline.Prune(ctx); err != nil {
		return fmt.Errorf("prune pipeline state: %w", err)
	}
	for _, project := range candidates {
		if err := a.pruneProject(ctx, service, project); err != nil {
			return fmt.Errorf("prune project %q: %w", project.Slug, err)
		}
	}
	_, err = fmt.Fprintln(stdout, "Pruned finished gg projects.")
	return err
}

func (a *App) confirmPrune(ctx context.Context, stdout io.Writer, count int) (bool, error) {
	if count == 0 {
		return true, nil
	}
	input := a.input
	if input == nil {
		input = os.Stdin
	}
	if _, err := fmt.Fprintf(stdout, "Prune %d finished/terminated project(s)? [y/N]: ", count); err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	line, err := bufio.NewReader(input).ReadString('\n')
	if err != nil && len(line) == 0 {
		return false, fmt.Errorf("read prune confirmation: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

func (a *App) pruneProject(ctx context.Context, service lifecycleService, project state.ProjectState) error {
	pruner, ok := service.(projectPruner)
	if !ok {
		return errors.New("lifecycle service does not support atomic project pruning")
	}
	return pruner.PruneProject(ctx, project.Slug, a.cleanupProject)
}

func (a *App) cleanupProject(ctx context.Context, project state.ProjectState) error {
	if project.GitDisabled {
		// The project executed directly in the user's own (non-git) folder:
		// prune removes only gg's state, never the folder or its contents.
		return nil
	}
	client, err := a.gitClient(ctx)
	if err != nil {
		return err
	}
	repoRoot, err := client.RepositoryRoot(ctx)
	if err != nil {
		return fmt.Errorf("validate repository: %w", err)
	}
	naming, err := git.ProjectNamingForSlug(repoRoot, project.Slug)
	if err != nil {
		return fmt.Errorf("derive owned worktree metadata: %w", err)
	}
	if !git.PathsEqual(project.WorktreePath, naming.WorktreePath) || project.BranchName != naming.BranchName {
		return fmt.Errorf("refusing cleanup: persisted worktree metadata does not match owned path %q and branch %q", naming.WorktreePath, naming.BranchName)
	}
	// Pruning discards a terminal project: leftover uncommitted files (for
	// example ignored artifacts) must not block removal, so prefer the forced
	// variant; ownership checks still apply.
	if forced, ok := client.(forcedWorktreeRemover); ok {
		if err := forced.RemoveWorktreeForced(ctx, project.WorktreePath, project.BranchName); err != nil {
			return fmt.Errorf("remove owned worktree: %w", err)
		}
		return nil
	}
	if err := client.RemoveWorktree(ctx, project.WorktreePath, project.BranchName); err != nil {
		return fmt.Errorf("remove owned worktree: %w", err)
	}
	return nil
}

// forcedWorktreeRemover is the optional capability for removing owned
// worktrees that still carry uncommitted changes.
type forcedWorktreeRemover interface {
	RemoveWorktreeForced(context.Context, string, string) error
}

func (a *App) verifyProjectWorktree(ctx context.Context, selector string) error {
	slug, err := git.ProjectSlug(selector)
	if err != nil {
		return fmt.Errorf("normalize project selector: %w", err)
	}
	service, err := a.projectService(ctx)
	if err != nil {
		return err
	}
	project, err := service.Load(ctx, slug)
	if err != nil {
		return fmt.Errorf("load project: %w", err)
	}
	return a.validateProjectWorktreeState(ctx, project)
}

func (a *App) validateProjectWorktreeState(ctx context.Context, project state.ProjectState) error {
	if project.GitDisabled {
		// Non-git projects run in the configured folder itself; the only
		// invariant is that the folder still exists.
		info, statErr := os.Stat(project.WorktreePath)
		if statErr != nil || !info.IsDir() {
			return fmt.Errorf("project folder %q is missing or not a directory", project.WorktreePath)
		}
		return nil
	}
	client, err := a.gitClient(ctx)
	if err != nil {
		return err
	}
	root, err := client.RepositoryRoot(ctx)
	if err != nil {
		return fmt.Errorf("validate repository: %w", err)
	}
	naming, err := git.ProjectNamingForSlug(root, project.Slug)
	if err != nil {
		return err
	}
	if !git.PathsEqual(project.WorktreePath, naming.WorktreePath) || project.BranchName != naming.BranchName {
		return errors.New("persisted worktree metadata does not match owned deterministic metadata")
	}
	worktree, err := client.LookupWorktree(ctx, project.WorktreePath, project.BranchName)
	if err != nil {
		return fmt.Errorf("verify project worktree: %w", err)
	}
	if !git.PathsEqual(worktree.Path, project.WorktreePath) || worktree.Branch != project.BranchName || worktree.Detached || worktree.Bare {
		return errors.New("persisted worktree metadata is not an attached owned worktree")
	}
	return nil
}

func (a *App) gitClient(ctx context.Context) (worktreeService, error) {
	if a.git != nil {
		return a.git, nil
	}
	root, err := a.root.ConfiguredRoot(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve configured root: %w", err)
	}
	return git.NewClient(root, nil), nil
}
func (a *App) updateCLI(ctx context.Context, stdout io.Writer) error {
	result, err := a.update.Update(ctx)
	if err != nil {
		return fmt.Errorf("update gg: %w", err)
	}
	switch result.Action {
	case "unrecognized":
		_, err = fmt.Fprintf(stdout, "gg update: current version %q is not a recognized release; no update performed.\n", result.Current)
	case "current":
		_, err = fmt.Fprintf(stdout, "gg update: %s is already the latest release.\n", result.Current)
	case "manual":
		_, err = fmt.Fprintln(stdout, update.ManualInstructions(result.Latest))
	case "blocked", "installed":
		_, err = fmt.Fprintln(stdout, result.Message)
	default:
		return fmt.Errorf("update gg: unknown update action %q", result.Action)
	}
	return err
}
func hasCommandHelp(command string, args []string) bool {
	for _, arg := range args {
		if command == "run" && arg == "--" {
			return false
		}
		if arg == "--help" || arg == "-h" {
			return true
		}
	}
	return false
}
func rejectArgs(command string, args []string) error {
	if len(args) == 0 {
		return nil
	}
	return fmt.Errorf("%q does not accept arguments: %q", command, strings.Join(args, " "))
}

func writeTopLevelHelp(w io.Writer) {
	fmt.Fprint(w, strings.TrimSpace(`gg coordinates developer-ai agent workflows.

Usage:
  gg [project]
  gg <command> [arguments]

Commands:
  configure   Open the interactive configuration workflow
  list        List gg projects
  status      Show project status
  usage       Show token and USD usage per project
  run         Start a gg workflow
  resume      Resume a stopped or failed gg workflow
  stop        Stop a running gg workflow
  stop-all    Stop every persisted running gg workflow
  prune       Remove done (finished/terminated) projects
  remove      Remove one project of any parked status
  update      Update gg components
  version     Show gg build version

Flags:
  --configure  Alias for "gg configure"
  -h, --help   Show this help

Run "gg <command> --help" for command-specific help.
`)+"\n")
}
func writeVersion(w io.Writer, metadata version.Metadata) error {
	_, err := fmt.Fprintln(w, metadata.String())
	return err
}

func writeCommandHelp(w io.Writer, command string) {
	description, ok := commandDescriptions[command]
	if !ok {
		fmt.Fprintf(w, "Unknown command %q.\n", command)
		return
	}
	fmt.Fprintf(w, "%s\n\nUsage:\n  gg %s%s\n", description.summary, command, description.usageSuffix)
	if command == "run" {
		fmt.Fprint(w, "\nRun controls: --parent-branch, --base-ref, --enable-pr, --disable-pr, --enable-ci, --disable-ci, and --max-iterations (default 3 total QA attempts). Add --repair-existing-verification to let Development repair the verification failures that already existed on the parent branch; without it those failures are recorded as warnings and only new regressions block the run. New projects choose Inherit or Pick configuration in the attached TUI; use -- to pass every following token to the pipeline unchanged.\n")
	}
	if command == "resume" {
		fmt.Fprint(w, "\nResume controls: --repair-existing-verification, which behaves exactly as it does for \"gg run\".\n")
	}
}

type commandDescription struct{ summary, usageSuffix string }

var commandDescriptions = map[string]commandDescription{
	"configure": {"Open the interactive configuration workflow.", ""}, "list": {"List gg projects.", ""}, "status": {"Show project status.", " [project]"}, "run": {"Start a gg workflow.", " [arguments]"}, "resume": {"Resume a gg workflow.", " [project]"}, "stop": {"Stop a running gg workflow.", " [arguments]"}, "stop-all": {"Stop every persisted running gg workflow.", ""}, "prune": {"Remove done (finished/terminated) projects.", ""}, "remove": {"Remove one project of any parked status.", " <project>"}, "update": {"Update gg components.", ""}, "usage": {"Show token and USD usage per project.", ""}, "version": {"Show gg build version.", ""},
}
