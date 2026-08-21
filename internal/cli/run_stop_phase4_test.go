package cli

import (
	"bytes"
	"context"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/state"
)

func TestRunResumesPersistedStoppedProjectThroughController(t *testing.T) {
	root := t.TempDir()
	initTestRepository(t, root)
	controller := &captureController{}
	app := controllerTestApp(t, root, controller)

	var stdout, stderr bytes.Buffer
	if code := app.Run(context.Background(), []string{"run", "Stopped Project"}, &stdout, &stderr); code != 0 {
		t.Fatalf("initial run code=%d stderr=%q", code, stderr.String())
	}
	service := state.NewLifecycleService(mustStateStore(t, root), nil, mustStateStore(t, root).Locker())
	if _, err := service.Transition(context.Background(), "stopped-project", state.StatusStopped, "planning", "", nil); err != nil {
		t.Fatalf("persist stopped state: %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := app.Run(context.Background(), []string{"run", "Stopped Project"}, &stdout, &stderr); code != 0 {
		t.Fatalf("resume run code=%d stderr=%q", code, stderr.String())
	}
	if len(controller.executes) != 1 {
		t.Fatalf("fresh Execute dispatches=%d, want 1 from initial run", len(controller.executes))
	}
	if len(controller.resumes) != 1 {
		t.Fatalf("Resume dispatches=%d, want 1", len(controller.resumes))
	}
	if got := controller.resumes[0].ProjectSlug; got != "stopped-project" {
		t.Fatalf("resume selector=%q", got)
	}
	if got := controller.resumes[0].Execution.Project.Status; got != state.StatusStopped {
		t.Fatalf("resume request status=%q, want stopped cursor state", got)
	}
	if stdout.String() != "Run workflow resumed.\n" {
		t.Fatalf("stdout=%q", stdout.String())
	}
}
