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

type tempRoot struct{ root string }

func (r tempRoot) ConfiguredRoot(context.Context) (string, error) { return r.root, nil }

func TestParseRunOptionsPreservesRawFlagArgsForRespawn(t *testing.T) {
	t.Skip("removed configuration flags are not reconstructed for detached runs")
	options, err := parseRunOptions([]string{"--model", "opus", "my project", "--max-iterations", "5"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"--model", "opus", "--max-iterations", "5"}; !reflect.DeepEqual(options.flagArgs, want) {
		t.Fatalf("flagArgs = %v, want %v", options.flagArgs, want)
	}
	if want := []string{"my project"}; !reflect.DeepEqual(options.args, want) {
		t.Fatalf("args = %v, want %v", options.args, want)
	}
}

func TestStartDetachedSpawnsRunAndWaitsForOwnershipHandover(t *testing.T) {
	t.Skip("removed configuration flags are not reconstructed for detached runs")
	service := &listStatusProjects{projects: []state.ProjectState{{Name: "Demo", Slug: "demo", Status: state.StatusPending}}}
	var gotRoot, gotLog string
	var gotArgs []string
	root := t.TempDir()
	spawner := func(_ context.Context, spawnRoot string, args []string, logPath string) error {
		gotRoot, gotArgs, gotLog = spawnRoot, args, logPath
		service.projects[0].Status = state.StatusRunning
		return nil
	}
	app := New(WithLifecycleService(service), WithRootResolver(tempRoot{root: root}), WithRunSpawner(spawner))
	app.detachedStartTimeout = time.Second
	app.detachedPollInterval = time.Millisecond

	options, err := parseRunOptions([]string{"--model", "opus"})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.pipelineStarter("demo", options)(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"run", "--model", "opus", "demo"}; !reflect.DeepEqual(gotArgs, want) {
		t.Fatalf("spawned args = %v, want %v", gotArgs, want)
	}
	if gotRoot != root {
		t.Fatalf("spawn root = %q, want %q", gotRoot, root)
	}
	if want := filepath.Join(root, ".gg", "projects", "demo", "logs", "daemon.log"); gotLog != want {
		t.Fatalf("log path = %q, want %q", gotLog, want)
	}
}

func TestStartDetachedFailsWithLogPathWhenDaemonNeverTakesOver(t *testing.T) {
	service := &listStatusProjects{projects: []state.ProjectState{{Name: "Demo", Slug: "demo", Status: state.StatusPending}}}
	app := New(WithLifecycleService(service), WithRootResolver(tempRoot{root: t.TempDir()}), WithRunSpawner(func(context.Context, string, []string, string) error { return nil }))
	app.detachedStartTimeout = 10 * time.Millisecond
	app.detachedPollInterval = time.Millisecond

	err := app.startDetached(context.Background(), "demo", []string{"run", "demo"})
	if err == nil || !strings.Contains(err.Error(), "daemon.log") {
		t.Fatalf("error = %v, want timeout mentioning the daemon log", err)
	}
}
