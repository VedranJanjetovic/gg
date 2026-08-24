package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/VedranJanjetovic/gg/internal/git"
	"github.com/VedranJanjetovic/gg/internal/orchestrator"
	"github.com/VedranJanjetovic/gg/internal/state"
)

// stampLiveRunOwner marks a running fixture project as owned by this test
// process so attach-time stale-run recovery leaves it untouched.
func stampLiveRunOwner(t *testing.T, store *state.FileStore, slug string) {
	t.Helper()
	project, err := store.Load(context.Background(), slug)
	if err != nil {
		t.Fatal(err)
	}
	if project.Status != state.StatusRunning {
		return
	}
	project.RunOwnerPID = os.Getpid()
	if err := store.Save(context.Background(), project); err != nil {
		t.Fatal(err)
	}
}

type projectAttacherFunc func(context.Context, ProjectAttachment) error

func (f projectAttacherFunc) Attach(ctx context.Context, attachment ProjectAttachment) error {
	return f(ctx, attachment)
}

func TestBareGGCreatesStartsAndAttachesProject(t *testing.T) {
	repo := t.TempDir()
	initTestRepository(t, repo)
	stateRoot := t.TempDir()
	controller := &captureController{}
	attachCalls := 0
	app := New(
		WithRootResolver(fixedRoot{root: stateRoot}),
		WithConfigStore(completeConfiguredMemoryStore()),
		WithGitClient(git.NewClient(repo, nil)),
		WithProjectPrompter(projectPromptStub{input: orchestrator.ProjectInput{
			Goal:               "Build an attachable project UI.",
			AcceptanceCriteria: []string{"Start the project from the attached session"},
		}}),
		WithOrchestratorController(controller),
		WithProjectAttacher(projectAttacherFunc(func(ctx context.Context, attachment ProjectAttachment) error {
			attachCalls++
			if attachment.Project.Slug != "attachable-project-ui" {
				t.Fatalf("attached project = %q", attachment.Project.Slug)
			}
			if attachment.Project.Status != state.StatusPending {
				t.Fatalf("initial attached status = %s, want pending", attachment.Project.Status)
			}
			if attachment.Load == nil || attachment.Start == nil || attachment.Stop == nil || attachment.Resume == nil {
				t.Fatal("new-project attachment is missing session operations")
			}
			return attachment.Start(ctx)
		})),
	)
	var stdout, stderr bytes.Buffer

	if code := app.Run(context.Background(), nil, &stdout, &stderr); code != 0 {
		t.Fatalf("bare gg code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if attachCalls != 1 {
		t.Fatalf("attach calls = %d, want 1", attachCalls)
	}
	if len(controller.executes) != 1 || controller.executes[0].Project.Slug != "attachable-project-ui" {
		t.Fatalf("execute requests = %#v", controller.executes)
	}
}

func TestProjectShorthandAttachesExactlyOnceWithoutMutationOrExecution(t *testing.T) {
	root := t.TempDir()
	initTestRepository(t, root)
	if _, stderr, code := runApp(t, New(WithRootResolver(fixedRoot{root: root})), "run", "Attach Me"); code != 0 {
		t.Fatalf("seed project code=%d stderr=%q", code, stderr)
	}
	store := mustStateStore(t, root)
	stampLiveRunOwner(t, store, "attach-me")
	before, err := store.Load(context.Background(), "attach-me")
	if err != nil {
		t.Fatal(err)
	}
	controller := &captureController{}
	attachCalls := 0
	app := New(
		WithRootResolver(fixedRoot{root: root}),
		WithOrchestratorController(controller),
		WithProjectAttacher(projectAttacherFunc(func(ctx context.Context, attachment ProjectAttachment) error {
			attachCalls++
			if attachment.Project.Slug != "attach-me" {
				t.Fatalf("attached project = %q", attachment.Project.Slug)
			}
			if attachment.Start != nil {
				t.Fatal("existing-project attachment unexpectedly received a start action")
			}
			loaded, loadErr := attachment.Load(ctx)
			if loadErr != nil {
				return loadErr
			}
			if !reflect.DeepEqual(loaded, before) {
				t.Fatalf("loaded state changed before attachment: %#v", loaded)
			}
			return nil
		})),
	)
	var stdout, stderr bytes.Buffer

	if code := app.Run(context.Background(), []string{"Attach Me"}, &stdout, &stderr); code != 0 {
		t.Fatalf("shorthand code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if attachCalls != 1 {
		t.Fatalf("attach calls = %d, want 1", attachCalls)
	}
	if len(controller.executes) != 0 || len(controller.resumes) != 0 || len(controller.stops) != 0 {
		t.Fatalf("attach-only invoked controller: %#v", controller)
	}
	after, err := store.Load(context.Background(), "attach-me")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("attach mutated state\nbefore: %#v\nafter:  %#v", before, after)
	}
}

func TestUnknownProjectShorthandDoesNotAttach(t *testing.T) {
	root := t.TempDir()
	attachCalls := 0
	app := New(
		WithRootResolver(fixedRoot{root: root}),
		WithProjectAttacher(projectAttacherFunc(func(context.Context, ProjectAttachment) error {
			attachCalls++
			return nil
		})),
	)
	var stdout, stderr bytes.Buffer

	if code := app.Run(context.Background(), []string{"Missing Project"}, &stdout, &stderr); code == 0 {
		t.Fatal("unknown project shorthand unexpectedly succeeded")
	}
	if !strings.Contains(stderr.String(), "project \"missing-project\" does not exist") {
		t.Fatalf("stderr = %q, want clear unknown-project error", stderr.String())
	}
	if attachCalls != 0 {
		t.Fatalf("attach calls = %d, want 0", attachCalls)
	}
}

func TestAttachmentErrorsAreReturnedToCaller(t *testing.T) {
	root := t.TempDir()
	initTestRepository(t, root)
	if _, stderr, code := runApp(t, New(WithRootResolver(fixedRoot{root: root})), "run", "attach-error"); code != 0 {
		t.Fatalf("seed project code=%d stderr=%q", code, stderr)
	}
	attachErr := errors.New("terminal session failed")
	app := New(
		WithRootResolver(fixedRoot{root: root}),
		WithProjectAttacher(projectAttacherFunc(func(context.Context, ProjectAttachment) error { return attachErr })),
	)
	var stdout, stderr bytes.Buffer

	if code := app.Run(context.Background(), []string{"attach-error"}, &stdout, &stderr); code == 0 {
		t.Fatal("attachment error unexpectedly succeeded")
	}
	if !strings.Contains(stderr.String(), "attach project \"attach-error\": terminal session failed") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestAttachmentLoadReadsLatestDurableState(t *testing.T) {
	root := t.TempDir()
	initTestRepository(t, root)
	if _, stderr, code := runApp(t, New(WithRootResolver(fixedRoot{root: root})), "run", "live-state"); code != 0 {
		t.Fatalf("seed project code=%d stderr=%q", code, stderr)
	}
	store := mustStateStore(t, root)
	stampLiveRunOwner(t, store, "live-state")
	lifecycle := state.NewLifecycleService(store, nil, store.Locker())
	app := New(
		WithRootResolver(fixedRoot{root: root}),
		WithLifecycleService(lifecycle),
		WithProjectAttacher(projectAttacherFunc(func(ctx context.Context, attachment ProjectAttachment) error {
			if attachment.Project.Status != state.StatusRunning {
				t.Fatalf("initial status = %s, want running", attachment.Project.Status)
			}
			if _, err := lifecycle.Transition(ctx, attachment.Project.Slug, state.StatusStopped, attachment.Project.CurrentPhase, attachment.Project.CurrentSubphase, nil); err != nil {
				return err
			}
			latest, err := attachment.Load(ctx)
			if err != nil {
				return err
			}
			if latest.Status != state.StatusStopped {
				t.Fatalf("loaded status = %s, want latest stopped state", latest.Status)
			}
			return nil
		})),
	)
	var stdout, stderr bytes.Buffer

	if code := app.Run(context.Background(), []string{"Live State"}, &stdout, &stderr); code != 0 {
		t.Fatalf("attach code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestSkipProjectionNamesCurrentDevelopmentPlanPhase(t *testing.T) {
	now := time.Now()
	project := state.ProjectState{
		Status:       state.StatusFailed,
		Plan:         &state.PlanState{Phases: []string{"Phase 1: done", "Phase 2: docs"}, Completed: []string{"Phase 1: done"}},
		PhaseHistory: []state.PhaseRecord{{Phase: "development", Subphase: "testing", Status: state.StatusFailed, OccurrenceID: "testing-1", StartedAt: now, CompletedAt: &now}},
	}
	available, label := skipProjection(project)
	if !available || label != "Development / Phase 2: docs / Testing" {
		t.Fatalf("skip projection = %t/%q, want true/current plan phase", available, label)
	}
}

func TestAttachmentStopAndResumeCallbacksHonorContextAndCanonicalSelector(t *testing.T) {
	root := t.TempDir()
	initTestRepository(t, root)
	store := mustStateStore(t, root)
	lifecycle := state.NewLifecycleService(store, nil, store.Locker())
	controller := &captureController{}
	seed := New(
		WithRootResolver(fixedRoot{root: root}),
		WithConfigStore(configuredMemoryStore()),
		WithLifecycleService(lifecycle),
		WithOrchestratorController(controller),
	)
	if _, stderr, code := runApp(t, seed, "run", "Callback Project"); code != 0 {
		t.Fatalf("seed project code=%d stderr=%q", code, stderr)
	}
	controller.executes = nil

	stopApp := New(
		WithRootResolver(fixedRoot{root: root}),
		WithLifecycleService(lifecycle),
		WithOrchestratorController(controller),
		WithProjectAttacher(projectAttacherFunc(func(ctx context.Context, attachment ProjectAttachment) error {
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			if err := attachment.Stop(canceled); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled stop error = %v", err)
			}
			if len(controller.stops) != 0 {
				t.Fatal("canceled stop reached controller")
			}
			return attachment.Stop(ctx)
		})),
	)
	var stdout, stderr bytes.Buffer
	if code := stopApp.Run(context.Background(), []string{"Callback Project"}, &stdout, &stderr); code != 0 {
		t.Fatalf("stop callback attach code=%d stderr=%q", code, stderr.String())
	}
	if len(controller.stops) != 1 || controller.stops[0].ProjectSlug != "callback-project" {
		t.Fatalf("stop requests = %#v", controller.stops)
	}
	if _, err := lifecycle.Transition(context.Background(), "callback-project", state.StatusStopped, "pipeline", "run", nil); err != nil {
		t.Fatal(err)
	}

	resumeApp := New(
		WithRootResolver(fixedRoot{root: root}),
		WithLifecycleService(lifecycle),
		WithOrchestratorController(controller),
		WithProjectAttacher(projectAttacherFunc(func(ctx context.Context, attachment ProjectAttachment) error {
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			if err := attachment.Resume(canceled); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled resume error = %v", err)
			}
			if len(controller.resumes) != 0 {
				t.Fatal("canceled resume reached controller")
			}
			return attachment.Resume(ctx)
		})),
	)
	stdout.Reset()
	stderr.Reset()
	if code := resumeApp.Run(context.Background(), []string{"Callback Project"}, &stdout, &stderr); code != 0 {
		t.Fatalf("resume callback attach code=%d stderr=%q", code, stderr.String())
	}
	if len(controller.resumes) != 1 || controller.resumes[0].ProjectSlug != "callback-project" {
		t.Fatalf("resume requests = %#v", controller.resumes)
	}
}

func TestBareGGStartFailureIsReturnedAndReservationIsRolledBack(t *testing.T) {
	repo := t.TempDir()
	initTestRepository(t, repo)
	stateRoot := t.TempDir()
	dispatchErr := errors.New("controller unavailable")
	attachCalls := 0
	app := New(
		WithRootResolver(fixedRoot{root: stateRoot}),
		WithConfigStore(completeConfiguredMemoryStore()),
		WithGitClient(git.NewClient(repo, nil)),
		WithProjectPrompter(projectPromptStub{input: orchestrator.ProjectInput{
			Goal:               "Build a failure-safe session.",
			AcceptanceCriteria: []string{"Rollback a failed start"},
		}}),
		WithOrchestratorController(&captureController{err: dispatchErr}),
		WithProjectAttacher(projectAttacherFunc(func(ctx context.Context, attachment ProjectAttachment) error {
			attachCalls++
			return attachment.Start(ctx)
		})),
	)
	var stdout, stderr bytes.Buffer

	if code := app.Run(context.Background(), nil, &stdout, &stderr); code == 0 {
		t.Fatal("bare gg start failure unexpectedly succeeded")
	}
	if !strings.Contains(stderr.String(), "attach project \"failure-safe-session\"") || !strings.Contains(stderr.String(), dispatchErr.Error()) {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if attachCalls != 1 {
		t.Fatalf("attach calls = %d, want 1", attachCalls)
	}
	project, err := mustStateStore(t, stateRoot).Load(context.Background(), "failure-safe-session")
	if err != nil {
		t.Fatal(err)
	}
	if project.Status != state.StatusPending || project.RunReservationToken != "" {
		t.Fatalf("project after failed start = %#v, want unreserved pending state", project)
	}
}
