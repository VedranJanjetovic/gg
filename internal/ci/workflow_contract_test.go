package ci

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type workflowContract struct {
	Jobs map[string]workflowJob `yaml:"jobs"`
}

type workflowJob struct {
	RunsOn string            `yaml:"runs-on"`
	Env    map[string]string `yaml:"env"`
	Steps  []workflowStep    `yaml:"steps"`
}

type workflowStep struct {
	Uses string            `yaml:"uses"`
	Run  string            `yaml:"run"`
	With map[string]string `yaml:"with"`
}

// TestNativeWorkflowRequiresGo122StandardGates keeps the required native
// platform evidence from silently regressing to a compile-only or partial CI
// job. The macOS linker settings are part of the contract because E2E tests
// build and execute nested Go binaries.
func TestNativeWorkflowRequiresGo122StandardGates(t *testing.T) {
	root := moduleRoot(t)
	data, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow workflowContract
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatalf("parse CI workflow: %v", err)
	}

	wantPlatforms := map[string]string{
		"linux":   "ubuntu-latest",
		"macos":   "macos-latest",
		"windows": "windows-latest",
	}
	wantGates := []string{
		"gofmt -l .",
		"go test ./...",
		"go test -race ./...",
		"go vet ./...",
		"go build -o",
		"git diff --check",
	}
	for name, platform := range wantPlatforms {
		t.Run(name, func(t *testing.T) {
			job, ok := workflow.Jobs[name]
			if !ok {
				t.Fatalf("missing required native job %q", name)
			}
			if job.RunsOn != platform {
				t.Fatalf("runs-on = %q, want %q", job.RunsOn, platform)
			}

			var setupGo workflowStep
			var commands strings.Builder
			for _, step := range job.Steps {
				if step.Uses == "actions/setup-go@v5" {
					setupGo = step
				}
				commands.WriteString(step.Run)
				commands.WriteByte('\n')
			}
			if setupGo.Uses == "" || setupGo.With["go-version"] != "1.22.12" {
				t.Fatalf("setup-go = %#v, want actions/setup-go@v5 with Go 1.22.12", setupGo)
			}
			for _, gate := range wantGates {
				if !strings.Contains(commands.String(), gate) {
					t.Errorf("missing required gate %q", gate)
				}
			}

			switch name {
			case "macos":
				if job.Env["CGO_ENABLED"] != "1" || job.Env["GOFLAGS"] != "-ldflags=-linkmode=external" {
					t.Fatalf("macOS Go environment = %#v, want CGO_ENABLED=1 and external linking", job.Env)
				}
			case "linux", "windows":
				if _, ok := job.Env["GOFLAGS"]; ok {
					t.Fatalf("%s job applies macOS-only GOFLAGS: %#v", name, job.Env)
				}
			}
		})
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate CI workflow test")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
}
