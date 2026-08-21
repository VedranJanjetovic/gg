package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/VedranJanjetovic/gg/internal/agent"
	"github.com/VedranJanjetovic/gg/internal/ci"
	"github.com/VedranJanjetovic/gg/internal/cli"
	"github.com/VedranJanjetovic/gg/internal/config"
	"github.com/VedranJanjetovic/gg/internal/eventlog"
	"github.com/VedranJanjetovic/gg/internal/gh"
	"github.com/VedranJanjetovic/gg/internal/git"
	"github.com/VedranJanjetovic/gg/internal/orchestrator"
	"github.com/VedranJanjetovic/gg/internal/pr"
	"github.com/VedranJanjetovic/gg/internal/proof"
	"github.com/VedranJanjetovic/gg/internal/state"
	"github.com/VedranJanjetovic/gg/internal/tui"
	"github.com/VedranJanjetovic/gg/internal/update"
	"github.com/VedranJanjetovic/gg/internal/version"
)

func main() {
	// SIGINT/SIGTERM cancel the run context so headless and detached runs
	// stop gracefully (agents killed, project persisted as stopped) instead
	// of dying with a stale "running" record.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	var app *cli.App
	var err error
	switch {
	case isVersionRequest(os.Args[1:]):
		app = cli.New(cli.WithVersion(version.Current()))
	case isUpdateRequest(os.Args[1:]):
		app, err = newUpdateApp(ctx)
	default:
		app, err = newApp(ctx)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "gg: initialize application: %v\n", err)
		os.Exit(1)
	}
	os.Exit(app.Run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func newUpdateApp(ctx context.Context) (*cli.App, error) {
	rootResolver := config.NewService()
	root, err := rootResolver.ConfiguredRoot(ctx)
	if err != nil {
		return nil, err
	}
	store, err := state.NewFileStore(root)
	if err != nil {
		return nil, err
	}
	projects := state.NewLifecycleService(store, nil, store.Locker())
	service := update.NewServiceWithDependencies(
		func() string { return version.Current().Version },
		update.NewHTTPReleaseLookup(nil, os.Getenv("GG_RELEASE_SOURCE")),
		update.WithProjectStatusLister(productionProjectStatusLister{projects: projects}),
		update.WithInstaller(update.NewPlatformInstaller()),
	)
	return cli.New(cli.WithVersion(version.Current()), cli.WithRootResolver(rootResolver), cli.WithLifecycleService(projects), cli.WithUpdateService(service)), nil
}

// unsupportedPRCIRemediator makes the production limitation explicit.
// Observation and durable persistence remain enabled; remediation is not
// silently skipped when a conflict or failed check is observed.
type unsupportedPRCIRemediator struct{}

func (unsupportedPRCIRemediator) Remediate(context.Context, orchestrator.Remediation) error {
	return errors.New("PR/CI remediation is unsupported in this production configuration")
}

type productionProjectStatusLister struct {
	projects interface {
		List(context.Context) ([]state.ProjectState, error)
	}
}

func (l productionProjectStatusLister) List(ctx context.Context) ([]update.ProjectStatus, error) {
	projects, err := l.projects.List(ctx)
	if err != nil {
		return nil, err
	}
	statuses := make([]update.ProjectStatus, 0, len(projects))
	for _, project := range projects {
		statuses = append(statuses, update.ProjectStatus{Status: string(project.Status)})
	}
	return statuses, nil
}
func newApp(ctx context.Context) (*cli.App, error) {
	return newAppWithIO(ctx, os.Stdin, os.Stdout, tui.Run, true)
}

func newAppWithIO(ctx context.Context, input io.Reader, output io.Writer, runTUI tuiRunFunc, global ...bool) (*cli.App, error) {
	enableGlobal := len(global) > 0 && global[0]
	rootResolver := config.NewService()
	root, err := rootResolver.ConfiguredRoot(ctx)
	if err != nil {
		return nil, err
	}
	configStore := config.NewStore()
	store, err := state.NewFileStore(root)
	if err != nil {
		return nil, err
	}
	projects := state.NewLifecycleService(store, nil, store.Locker())
	events, err := eventlog.New(root)
	if err != nil {
		return nil, err
	}
	gitClient := git.NewClient(root, nil)
	// A credential embedded in the origin remote URL is the repository's
	// configured GitHub identity: gh operations (PR, CI checks) must act as
	// it — matching git push — instead of gh's interactive login, which may
	// not even see the repository. The token stays in child process env only.
	var ghEnv []string
	if remoteURL, remoteErr := gitClient.RemoteURL(ctx, "origin"); remoteErr == nil {
		if token := gh.TokenFromRemoteURL(remoteURL); token != "" {
			ghEnv = []string{"GH_TOKEN=" + token}
		}
	}
	gitOpsPR := pr.NewGitHubService(gitClient, gh.ExecCommandExecutor{Env: ghEnv})
	gitOpsCI := ci.NewService(ci.ExecExecutor{Env: ghEnv})
	monitor := orchestrator.NewPRCILifecycleMonitor(
		pr.NewGitHubStateProvider(gh.ExecCommandExecutor{Env: ghEnv}),
		ci.NewGitHubCheckProvider(ci.ExecExecutor{Env: ghEnv}),
		unsupportedPRCIRemediator{},
		projects,
	)
	runner := agent.NewAgentRunner(agent.AgentRunnerOptions{
		Factory: agent.NewExecProcessFactory(nil, nil),
		LogRoot: root,
		Proof:   proof.NewArtifactService(root, gitClient),
		Events:  events.AgentSink(),
	})
	// Code-touching phases are pointed at the installed coding-patterns
	// reference by absolute path; skills are referenced by gg-* name only.
	codingPatternsPath := ""
	if home, homeErr := os.UserHomeDir(); homeErr == nil {
		codingPatternsPath = filepath.Join(home, ".gg", "gg-coding-patterns.md")
		if _, statErr := os.Stat(codingPatternsPath); statErr != nil {
			codingPatternsPath = ""
		}
	}
	controller := orchestrator.NewProductionController(runner, projects, gitClient, orchestrator.WithEventSink(events.OrchestratorSink()), orchestrator.WithGitOpsServices(gitClient, gitOpsPR, gitOpsCI), orchestrator.WithPRCILifecycleMonitor(monitor), orchestrator.WithPromptBuilder(agent.StandalonePromptBuilder{CodingPatternsPath: codingPatternsPath}))
	resumeCoordinator, err := orchestrator.NewResumeCoordinator(productionResumeSource{roots: rootResolver}, controller, orchestrator.ResumeCoordinatorOptions{Concurrency: 4})
	if err != nil {
		return nil, err
	}
	terminalExecutable, terminalArgs := os.Getenv("TERMINAL"), []string(nil)
	if terminalExecutable == "" && runtime.GOOS == "darwin" {
		terminalExecutable, terminalArgs = "open", []string{"-a", "Terminal", cli.WorktreePlaceholder}
	}
	launchActions := cli.NewLaunchActions(cli.ExecCommandLauncher{}, "code", terminalExecutable, terminalArgs)
	var app *cli.App
	options := []cli.Option{
		cli.WithInput(input), cli.WithConfigStore(configStore), cli.WithRootResolver(rootResolver),
		cli.WithAgentCatalogSource(rootResolver), cli.WithConfigurePicker(tui.RunConfigureWizard),
		cli.WithConfiguredFolderGate(), cli.WithLifecycleService(projects), cli.WithOrchestratorController(controller), cli.WithResumeCoordinator(resumeCoordinator),
		cli.WithProjectEventSink(events.OrchestratorSink()), cli.WithLaunchActions(launchActions),
		cli.WithProjectAttacher(projectTUIAttacher{input: input, output: output, run: runTUI}),
		cli.WithQuestionAsker(tui.RunQuestionPrompt), cli.WithBusyRunner(tui.RunBusy),
		cli.WithInterviewSession(cli.ExecInterviewSession),
		cli.WithRunSpawner(cli.NewDetachedRunSpawner()),
	}
	if enableGlobal {
		globalController, controllerErr := tui.NewGlobalController(
			func(folderCtx context.Context) ([]string, error) {
				// The global view is machine-wide: every folder registered by
				// gg configure (that still exists), plus the current folder
				// when it carries a gg configuration.
				var folders []string
				seen := map[string]bool{}
				add := func(folder string) {
					folder = filepath.Clean(folder)
					if seen[folder] {
						return
					}
					if info, statErr := os.Stat(filepath.Join(folder, ".gg")); statErr != nil || !info.IsDir() {
						return
					}
					seen[folder] = true
					folders = append(folders, folder)
				}
				if global, globalErr := configStore.LoadGlobal(); globalErr == nil {
					for _, folder := range global.Folders {
						add(folder)
					}
				}
				if root, rootErr := rootResolver.ConfiguredRoot(folderCtx); rootErr == nil {
					add(root)
				}
				return folders, nil
			},
			func(folderCtx context.Context, folder string) ([]state.ProjectState, error) {
				folderStore, err := state.NewFileStore(folder)
				if err != nil {
					return nil, err
				}
				states, err := folderStore.List(folderCtx)
				if err != nil {
					return nil, err
				}
				// Recover projects whose owning process died so the list
				// shows a resumable stopped state instead of a dead "running".
				folderService := state.NewLifecycleService(folderStore, nil, folderStore.Locker())
				for i, project := range states {
					if project.Status != state.StatusRunning {
						continue
					}
					if recovered, changed, recoverErr := folderService.RecoverIfStale(folderCtx, project.Slug); recoverErr == nil && changed {
						states[i] = recovered
					}
				}
				return states, nil
			},
			tui.WithRefreshTimeout(2*tui.DefaultGlobalRefreshInterval),
		)
		if controllerErr != nil {
			return nil, controllerErr
		}
		options = append(options, cli.WithGlobalRunner(func(globalCtx context.Context, globalInput io.Reader, globalOutput io.Writer) error {
			return tui.RunGlobal(globalCtx, globalController, globalInput, globalOutput, tui.WithGlobalProjectAttacher(func(attachCtx context.Context, project state.ProjectState) error {
				return app.AttachProject(attachCtx, project.Slug)
			}))
		}))
	}
	app = cli.New(options...)
	return app, nil
}

func isStandaloneRequest(args []string) bool {
	return isVersionRequest(args) || isUpdateRequest(args)
}

func isUpdateRequest(args []string) bool {
	return len(args) > 0 && args[0] == "update"
}

func isVersionRequest(args []string) bool {
	return len(args) > 0 && (args[0] == "--version" || args[0] == "version")
}
