package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestProductionCompositionGatesUnconfiguredFolderForProjectCommands(t *testing.T) {
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previous); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	ctx := context.Background()
	commands := [][]string{
		{"list"}, {"list", "--all"}, {"status"}, {"run", "production-project"},
		{"stop", "production-project"}, {"prune", "--yes"}, {"production-project"},
	}
	for _, args := range commands {
		t.Run(strings.Join(args, "-"), func(t *testing.T) {
			app, err := newApp(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			if code := app.Run(ctx, args, &stdout, &stderr); code == 0 {
				t.Fatalf("command unexpectedly succeeded: args=%v stdout=%q", args, stdout.String())
			}
			if !strings.Contains(stderr.String(), `current folder is not configured; run "gg configure"`) {
				t.Fatalf("args=%v stderr=%q, want actionable configure guidance", args, stderr.String())
			}
		})
	}
}

func TestProductionBinaryUpdateFromUnconfiguredDirectory(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test source")
	}
	sourceDir := filepath.Dir(sourceFile)
	binary := filepath.Join(t.TempDir(), "gg")
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = sourceDir
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build gg: %v\n%s", err, output)
	}

	cmd := exec.Command(binary, "update")
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "GG_VERSION=dev")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("gg update from unconfigured cwd: %v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "not a recognized release") {
		t.Fatalf("stdout=%q, want deterministic development-build update result", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr=%q, want no application initialization error", stderr.String())
	}
}

func TestIsStandaloneRequestRecognizesVersionAndUpdate(t *testing.T) {
	for _, args := range [][]string{{"version"}, {"--version"}, {"update"}} {
		if !isStandaloneRequest(args) {
			t.Fatalf("isStandaloneRequest(%v) = false", args)
		}
	}
	if isStandaloneRequest([]string{"list"}) {
		t.Fatal("isStandaloneRequest(list) = true")
	}
}

func TestIsVersionRequestOnlyRecognizesTopLevelEntryPoints(t *testing.T) {
	for _, test := range []struct {
		args []string
		want bool
	}{
		{args: []string{"version"}, want: true},
		{args: []string{"--version"}, want: true},
		{args: []string{"run", "version"}, want: false},
		{args: nil, want: false},
	} {
		if got := isVersionRequest(test.args); got != test.want {
			t.Fatalf("isVersionRequest(%v) = %v, want %v", test.args, got, test.want)
		}
	}
}
