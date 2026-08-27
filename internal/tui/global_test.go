package tui

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/VedranJanjetovic/gg/internal/state"
)

func globalProject(slug string, status state.LifecycleStatus) state.ProjectState {
	return state.ProjectState{Slug: slug, Name: slug, Status: status}
}

// absoluteFolder mirrors the normalization NewGlobalController applies to
// listed folders. Relative-to-drive inputs such as "/a" only become absolute
// once filepath.Abs runs, so tests must compare against the same form.
func absoluteFolder(t *testing.T, folder string) string {
	t.Helper()
	absolute, err := filepath.Abs(folder)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(absolute)
}

func TestGlobalControllerGroupsSortsAndClassifiesProjects(t *testing.T) {
	first := absoluteFolder(t, "/a")
	controller, err := NewGlobalController(func(context.Context) ([]string, error) { return []string{"/z", "/a", "/a"}, nil }, func(_ context.Context, folder string) ([]state.ProjectState, error) {
		if folder == first {
			return []state.ProjectState{globalProject("z", state.StatusFinished), globalProject("a", state.StatusRunning), globalProject("s", state.StatusStopped)}, nil
		}
		return []state.ProjectState{globalProject("f", state.StatusFailed)}, nil
	}, WithRefreshTimeout(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := controller.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Folders) != 2 || snapshot.Folders[0].Folder != first {
		t.Fatalf("folders = %#v", snapshot.Folders)
	}
	projects := snapshot.Folders[0].Projects
	if got := projects[0].Project.Slug; got != "a" {
		t.Errorf("first project = %q", got)
	}
	if got := projects[1].TerminalKind; got != state.TerminalNone {
		t.Errorf("stopped terminal kind = %q", got)
	}
	if snapshot.ProjectCount() != 4 {
		t.Errorf("project count = %d", snapshot.ProjectCount())
	}
}

func TestGlobalControllerHonorsCancellationAndTimeout(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	controller, err := NewGlobalController(func(context.Context) ([]string, error) { return []string{"/a"}, nil }, func(context.Context, string) ([]state.ProjectState, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Refresh(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled refresh error = %v", err)
	}

	controller, err = NewGlobalController(func(context.Context) ([]string, error) { return []string{"/a"}, nil }, func(ctx context.Context, _ string) ([]state.ProjectState, error) { <-ctx.Done(); return nil, ctx.Err() }, WithRefreshTimeout(time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_, err = controller.Refresh(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error = %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("refresh was not bounded")
	}
}

func TestGlobalModelViewRendersEmptyAndLifecycleStates(t *testing.T) {
	controller, err := NewGlobalController(func(context.Context) ([]string, error) { return []string{"/projects"}, nil }, func(context.Context, string) ([]state.ProjectState, error) {
		return []state.ProjectState{globalProject("empty", state.StatusPending), globalProject("running", state.StatusRunning), globalProject("stopped", state.StatusStopped), globalProject("failed", state.StatusFailed), globalProject("finished", state.StatusFinished)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	model, err := NewGlobalModel(context.Background(), controller, WithGlobalRefreshInterval(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	updated, _ := model.Update(globalRefreshMsg{snapshot: GlobalSnapshot{Folders: []FolderObservation{{Folder: "/empty", Projects: nil}, {Folder: "/projects", Projects: []state.ProjectObservation{state.Observe(globalProject("empty", state.StatusPending)), state.Observe(globalProject("running", state.StatusRunning)), state.Observe(globalProject("stopped", state.StatusStopped)), state.Observe(globalProject("failed", state.StatusFailed)), state.Observe(globalProject("finished", state.StatusFinished))}}}}})
	view := updated.(GlobalModel).View()
	for _, want := range []string{"/empty", "(empty)", "empty  empty", "running  running", "stopped  stopped", "failed  failed", "finished  finished", "q quit"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}
}

func TestGlobalSnapshotSelectionIsOneBasedAndBounded(t *testing.T) {
	snapshot := GlobalSnapshot{Folders: []FolderObservation{
		{Folder: "/one", Projects: []state.ProjectObservation{state.Observe(globalProject("first", state.StatusRunning))}},
		{Folder: "/two", Projects: []state.ProjectObservation{state.Observe(globalProject("second", state.StatusFinished))}},
	}}
	if got, ok := snapshot.ProjectAt(0); !ok || got.Slug != "first" {
		t.Fatalf("row 1 = %#v, %t", got, ok)
	}
	if got, ok := snapshot.ProjectAt(1); !ok || got.Slug != "second" {
		t.Fatalf("row 2 = %#v, %t", got, ok)
	}
	for _, index := range []int{-1, 2} {
		if _, ok := snapshot.ProjectAt(index); ok {
			t.Fatalf("out-of-range row %d was selectable", index)
		}
	}
}

func TestGlobalModelAttachesSelectedProjectAndPreservesSnapshot(t *testing.T) {
	controller, err := NewGlobalController(func(context.Context) ([]string, error) { return nil, nil }, func(context.Context, string) ([]state.ProjectState, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	var attached state.ProjectState
	model, err := NewGlobalModel(context.Background(), controller, WithGlobalProjectAttacher(func(_ context.Context, project state.ProjectState) error { attached = project; return nil }))
	if err != nil {
		t.Fatal(err)
	}
	snapshot := GlobalSnapshot{Folders: []FolderObservation{{Folder: "/root", Projects: []state.ProjectObservation{
		state.Observe(globalProject("one", state.StatusRunning)), state.Observe(globalProject("two", state.StatusFinished)),
	}}}}
	updated, _ := model.Update(globalRefreshMsg{snapshot: snapshot})
	updated, command := updated.(GlobalModel).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	if command == nil {
		t.Fatal("numeric selection did not quit for attachment")
	}
	if _, ok := command().(tea.QuitMsg); !ok {
		t.Fatalf("selection command = %#v, want quit so the session owns the terminal", command())
	}
	got := updated.(GlobalModel)
	selected := got.Selected()
	if selected == nil || selected.Slug != "two" || got.Snapshot().ProjectCount() != 2 {
		t.Fatalf("selected=%#v snapshot=%#v", selected, got.Snapshot())
	}
	// The attacher runs outside the program (RunGlobal drives it); the model
	// itself must not invoke it.
	if attached.Slug != "" {
		t.Fatalf("model invoked attacher directly: %#v", attached)
	}
	if _, command := got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'3'}}); command != nil {
		t.Fatal("out-of-range numeric selection dispatched")
	}
}
