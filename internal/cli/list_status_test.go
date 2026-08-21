package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/VedranJanjetovic/gg/internal/config"
	"github.com/VedranJanjetovic/gg/internal/state"
)

type listStatusProjects struct {
	projects []state.ProjectState
	missing  bool
}

func (s *listStatusProjects) Create(context.Context, state.ProjectState) error { return nil }
func (s *listStatusProjects) Load(_ context.Context, slug string) (state.ProjectState, error) {
	for _, project := range s.projects {
		if project.Slug == slug {
			return project, nil
		}
	}
	if s.missing {
		return state.ProjectState{}, os.ErrNotExist
	}
	return state.ProjectState{}, errors.New("unexpected project lookup")
}
func (s *listStatusProjects) List(context.Context) ([]state.ProjectState, error) {
	return append([]state.ProjectState(nil), s.projects...), nil
}
func (s *listStatusProjects) Delete(context.Context, string) error { return nil }
func (s *listStatusProjects) Transition(context.Context, string, state.LifecycleStatus, string, string, []string) (state.ProjectState, error) {
	return state.ProjectState{}, nil
}

type listFixedRoot struct{ err error }

func (r listFixedRoot) ConfiguredRoot(context.Context) (string, error) { return "/configured", r.err }

func fixtureProjects() []state.ProjectState {
	updated := time.Date(2026, 8, 3, 15, 0, 0, 0, time.UTC)
	return []state.ProjectState{
		{Name: "Active", Slug: "active", Status: state.StatusRunning, CurrentPhase: "development", BranchName: "agent/active", WorktreePath: "/tmp/active", UpdatedAt: updated},
		{Name: "Finished", Slug: "finished", Status: state.StatusFinished, CurrentPhase: "qa", BranchName: "agent/finished", WorktreePath: "/tmp/finished", UpdatedAt: updated},
		{Name: "Terminated", Slug: "terminated", Status: state.StatusTerminated, BranchName: "agent/terminated", WorktreePath: "/tmp/terminated", UpdatedAt: updated},
	}
}

func TestListFiltersTerminalProjectsUnlessAll(t *testing.T) {
	projects := &listStatusProjects{projects: fixtureProjects()}
	app := New(WithLifecycleService(projects), WithRootResolver(listFixedRoot{}))
	for _, test := range []struct {
		name         string
		args         []string
		want, absent string
	}{
		{name: "default", args: nil, want: "Active", absent: "Finished"},
		{name: "all", args: []string{"-a"}, want: "Finished", absent: "No gg projects"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := app.listCommand(context.Background(), &out, test.args); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), test.want) || (test.absent != "" && strings.Contains(out.String(), test.absent)) {
				t.Fatalf("output=%q", out.String())
			}
		})
	}
}

func TestStatusTableHasRequiredColumnsAndDetailNormalizesSelector(t *testing.T) {
	projects := &listStatusProjects{projects: fixtureProjects()}
	app := New(WithLifecycleService(projects), WithRootResolver(listFixedRoot{}))
	var table bytes.Buffer
	if err := app.statusCommand(context.Background(), &table, nil); err != nil {
		t.Fatal(err)
	}
	for _, header := range []string{"NAME", "STATUS", "CURRENT PHASE", "BRANCH", "WORKTREE", "UPDATED"} {
		if !strings.Contains(table.String(), header) {
			t.Fatalf("table missing %q: %s", header, table.String())
		}
	}
	var detail bytes.Buffer
	if err := app.statusCommand(context.Background(), &detail, []string{"Active"}); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"Name: Active", "Status: running", "Current phase: development", "Branch: agent/active"} {
		if !strings.Contains(detail.String(), field) {
			t.Fatalf("detail missing %q: %s", field, detail.String())
		}
	}
}

func TestStatusMissingProjectAndUnconfiguredFolder(t *testing.T) {
	missing := New(WithLifecycleService(&listStatusProjects{missing: true}), WithRootResolver(listFixedRoot{}))
	var output bytes.Buffer
	if err := missing.statusCommand(context.Background(), &output, []string{"does-not-exist"}); err == nil || !strings.Contains(err.Error(), "load project") {
		t.Fatalf("error=%v", err)
	}

	unconfigured := New(WithLifecycleService(&listStatusProjects{projects: fixtureProjects()}), WithWorkingDirectory(func() (string, error) { return "/not-configured", nil }), WithConfigStore(&memoryConfigureStore{projectErr: config.ErrProjectNotConfigured}))
	if err := unconfigured.listCommand(context.Background(), io.Discard, nil); err == nil || !strings.Contains(err.Error(), "gg configure") {
		t.Fatalf("error=%v", err)
	}
}

func TestParseListAndStatusArguments(t *testing.T) {
	if got, err := parseListOptions([]string{"-a"}); err != nil || !got.includeFinished {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if _, err := parseListOptions([]string{"project"}); err == nil {
		t.Fatal("expected list selector error")
	}
	if got, err := statusSelector([]string{"Demo Project"}); err != nil || got != "demo-project" {
		t.Fatalf("got=%q err=%v", got, err)
	}
	if _, err := statusSelector([]string{"one", "two"}); err == nil {
		t.Fatal("expected status arity error")
	}
}

type recoveringProjects struct {
	listStatusProjects
	recovered []string
}

func (s *recoveringProjects) RecoverIfStale(_ context.Context, slug string) (state.ProjectState, bool, error) {
	s.recovered = append(s.recovered, slug)
	for _, project := range s.projects {
		if project.Slug == slug {
			project.Status = state.StatusStopped
			return project, true, nil
		}
	}
	return state.ProjectState{}, false, os.ErrNotExist
}

func TestListAndStatusRepairDeadRunningProjects(t *testing.T) {
	// A "running" project whose owning process died must be reported as
	// stopped (resumable) everywhere, not only on attach — a stale
	// "running" row in gg list contradicts what attach then shows.
	service := &recoveringProjects{listStatusProjects: listStatusProjects{projects: fixtureProjects()}}
	app := New(WithLifecycleService(service), WithRootResolver(listFixedRoot{}))
	var out bytes.Buffer
	if err := app.listCommand(context.Background(), &out, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "stopped") || strings.Contains(out.String(), "running") {
		t.Fatalf("list did not repair dead run: %q", out.String())
	}
	if len(service.recovered) != 1 || service.recovered[0] != "active" {
		t.Fatalf("recovered = %v, want only the running project", service.recovered)
	}
	var detail bytes.Buffer
	if err := app.statusCommand(context.Background(), &detail, []string{"Active"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail.String(), "Status: stopped") {
		t.Fatalf("status detail did not repair dead run: %q", detail.String())
	}
}

func TestUsageCommandSumsTokensAndReportedCostPerProject(t *testing.T) {
	projects := fixtureProjects()
	projects[0].PhaseHistory = []state.PhaseRecord{
		{Phase: "grooming", Status: state.StatusFinished, Outcome: &state.ExecutionOutcome{TokensUsed: 37452, CostUSD: 0.42}},
		{Phase: "qa", Status: state.StatusFinished, Outcome: &state.ExecutionOutcome{TokensUsed: 100000}},
	}
	projects[1].PhaseHistory = []state.PhaseRecord{
		{Phase: "development", Subphase: "implementation", Status: state.StatusFinished, Outcome: &state.ExecutionOutcome{TokensUsed: 2000, CostUSD: 1.08}},
	}
	app := New(WithLifecycleService(&listStatusProjects{projects: projects}), WithRootResolver(listFixedRoot{}))
	var out bytes.Buffer
	if err := app.usageCommand(context.Background(), &out, nil); err != nil {
		t.Fatal(err)
	}
	view := out.String()
	for _, want := range []string{
		"NAME", "TOKENS", "COST",
		"137,452", "$0.42", // Active: codex QA adds tokens but no cost
		"2,000", "$1.08", // Finished projects are included
		"139,452", "$1.50", // TOTAL row
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("usage output missing %q:\n%s", want, view)
		}
	}
	if !strings.Contains(view, "TOTAL") {
		t.Fatalf("usage output missing total row:\n%s", view)
	}
	// The zero-usage project renders dashes instead of fabricated zeros.
	if !strings.Contains(view, "-") {
		t.Fatalf("zero-usage project must show dashes:\n%s", view)
	}
}

func TestRemoveCommandRemovesFailedProjectAndRefusesRunning(t *testing.T) {
	root := t.TempDir()
	initTestRepository(t, root)
	app := New(WithRootResolver(fixedRoot{root: root}))
	if _, stderr, code := runApp(t, app, "run", "doomed-project"); code != 0 {
		t.Fatalf("setup run failed: %s", stderr)
	}
	store, err := state.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	service := state.NewLifecycleService(store, nil, store.Locker())

	// Running projects are protected.
	if _, stderr, code := runApp(t, app, "remove", "doomed-project", "--yes"); code == 0 || !strings.Contains(stderr, "stop it first") {
		t.Fatalf("remove of running project: code=%d stderr=%q", code, stderr)
	}

	if _, err := service.Transition(context.Background(), "doomed-project", state.StatusFailed, "qa", "", nil); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runApp(t, app, "remove", "doomed-project", "--yes")
	if code != 0 {
		t.Fatalf("remove failed: %s", stderr)
	}
	if !strings.Contains(stdout, "Removed project") {
		t.Fatalf("stdout = %q", stdout)
	}
	if _, err := store.Load(context.Background(), "doomed-project"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("project still present: %v", err)
	}
}

func TestRemoveGitDisabledProjectKeepsFolderAndCode(t *testing.T) {
	// A non-git project runs directly in the user's folder: remove (and
	// prune, which shares the same cleanup) must delete only gg's state,
	// never the folder or the code in it.
	root := t.TempDir()
	code := filepath.Join(root, "main.py")
	if err := os.WriteFile(code, []byte("print('keep me')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	app := New(WithRootResolver(fixedRoot{root: root}))
	runApp(t, app, "run", "folder-project") // non-git folder → in-place GitDisabled project
	store, err := state.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.Load(context.Background(), "folder-project")
	if err != nil || !project.GitDisabled {
		t.Fatalf("setup project = %#v err=%v, want git-disabled", project, err)
	}
	service := state.NewLifecycleService(store, nil, store.Locker())
	if _, err := service.Transition(context.Background(), "folder-project", state.StatusFailed, "qa", "", nil); err != nil {
		t.Fatal(err)
	}

	if _, stderr, code := runApp(t, app, "remove", "folder-project", "--yes"); code != 0 {
		t.Fatalf("remove failed: %s", stderr)
	}
	if _, err := store.Load(context.Background(), "folder-project"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("gg state still present: %v", err)
	}
	if data, err := os.ReadFile(code); err != nil || !strings.Contains(string(data), "keep me") {
		t.Fatalf("user code was touched: %v", err)
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		t.Fatalf("project folder was removed: %v", err)
	}
}
