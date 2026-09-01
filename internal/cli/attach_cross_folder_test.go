package cli

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/VedranJanjetovic/gg/internal/state"
)

// The global project view lists every configured folder, so attaching must
// resolve the project in the folder that owns it rather than in the folder gg
// happens to be running from.
func TestAttachProjectInResolvesProjectOutsideTheCurrentFolder(t *testing.T) {
	current := t.TempDir()
	owning := t.TempDir()
	initTestRepository(t, owning)
	if _, stderr, code := runApp(t, New(WithRootResolver(fixedRoot{root: owning})), "run", "Attach Me"); code != 0 {
		t.Fatalf("seed project in owning folder code=%d stderr=%q", code, stderr)
	}
	stampLiveRunOwner(t, mustStateStore(t, owning), "attach-me")

	attached := ""
	newApp := func() *App {
		return New(
			// The app is scoped to a folder that holds no projects at all.
			WithRootResolver(fixedRoot{root: current}),
			WithConfigStore(completeConfiguredMemoryStore()),
			WithOrchestratorController(&captureController{}),
			WithProjectAttacher(projectAttacherFunc(func(_ context.Context, attachment ProjectAttachment) error {
				attached = attachment.Project.Slug
				return nil
			})),
		)
	}

	if err := newApp().AttachProjectIn(context.Background(), owning, "attach-me"); err != nil {
		t.Fatalf("cross-folder attach: %v", err)
	}
	if attached != "attach-me" {
		t.Fatalf("attached project = %q, want %q", attached, "attach-me")
	}

	// Guards the regression: the current folder's store must not be consulted.
	attached = ""
	err := newApp().AttachProject(context.Background(), "attach-me")
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("current-folder attach error = %v, want a does-not-exist failure", err)
	}
}

// A scoped app must not mutate the app it was derived from: the global view
// attaches to many folders in sequence from one App.
func TestForFolderLeavesTheOriginalAppUnscoped(t *testing.T) {
	current := t.TempDir()
	owning := t.TempDir()
	app := New(WithRootResolver(fixedRoot{root: current}))

	scoped, err := app.forFolder(owning)
	if err != nil {
		t.Fatal(err)
	}
	scopedRoot, err := scoped.root.ConfiguredRoot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if scopedRoot != owning {
		t.Fatalf("scoped root = %q, want %q", scopedRoot, owning)
	}
	originalRoot, err := app.root.ConfiguredRoot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if originalRoot != current {
		t.Fatalf("original root = %q, want %q", originalRoot, current)
	}
	if app.projects != nil {
		t.Fatal("forFolder cached a lifecycle service on the original app")
	}
}

// Resuming a project owned by another folder must run there: a detached gg
// started in the current folder would look the project up in the wrong state
// store and write its log into the wrong .gg directory.
func TestDetachedRunForAProjectOutsideTheCurrentFolderTargetsTheOwningFolder(t *testing.T) {
	current := t.TempDir()
	owning := t.TempDir()
	initTestRepository(t, owning)
	if _, stderr, code := runApp(t, New(WithRootResolver(fixedRoot{root: owning})), "run", "Attach Me"); code != 0 {
		t.Fatalf("seed project in owning folder code=%d stderr=%q", code, stderr)
	}
	store := mustStateStore(t, owning)

	var gotRoot, gotLog string
	var gotArgs []string
	app := New(
		WithRootResolver(fixedRoot{root: current}),
		WithRunSpawner(func(_ context.Context, spawnRoot string, args []string, logPath string) error {
			gotRoot, gotArgs, gotLog = spawnRoot, args, logPath
			// Stand in for the daemon taking ownership. startDetached watches
			// for any status or UpdatedAt change, and Save preserves
			// UpdatedAt, so the bump has to be explicit.
			project, loadErr := store.Load(context.Background(), "attach-me")
			if loadErr != nil {
				return loadErr
			}
			project.Status = state.StatusRunning
			project.UpdatedAt = project.UpdatedAt.Add(time.Second)
			return store.Save(context.Background(), project)
		}),
	)
	app.detachedStartTimeout = time.Second
	app.detachedPollInterval = time.Millisecond

	scoped, err := app.forFolder(owning)
	if err != nil {
		t.Fatal(err)
	}
	if err := scoped.startDetached(context.Background(), "attach-me", []string{"resume", "attach-me"}); err != nil {
		t.Fatalf("detached resume: %v", err)
	}
	if gotRoot != owning {
		t.Fatalf("spawn root = %q, want the owning folder %q", gotRoot, owning)
	}
	if want := filepath.Join(owning, ".gg", "projects", "attach-me", "logs", "daemon.log"); gotLog != want {
		t.Fatalf("daemon log = %q, want %q", gotLog, want)
	}
	if want := []string{"resume", "attach-me"}; !reflect.DeepEqual(gotArgs, want) {
		t.Fatalf("spawn args = %v, want %v", gotArgs, want)
	}
}
