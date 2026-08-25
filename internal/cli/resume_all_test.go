package cli_test

import (
	"bytes"
	"context"
	"github.com/VedranJanjetovic/gg/internal/cli"
	"github.com/VedranJanjetovic/gg/internal/orchestrator"
	"github.com/VedranJanjetovic/gg/internal/state"
	"os"
	"strings"
	"testing"
)

type allResumeFake struct{}

func (allResumeFake) ResumeAll(context.Context, orchestrator.ResumeAllRequest) ([]orchestrator.ResumeResult, error) {
	return []orchestrator.ResumeResult{{ProjectSlug: "one", Kind: state.RerunResume}}, nil
}

func TestResumeWithoutSelectorUsesAllProjectCoordinator(t *testing.T) {
	var stdout, stderr strings.Builder
	app := cli.New(cli.WithResumeCoordinator(allResumeFake{}))
	if code := app.Run(context.Background(), []string{"resume"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if got := stdout.String(); got != "Resumed 1 project(s).\n" {
		t.Fatalf("stdout=%q", got)
	}
}

type resumeAllStatusFake struct{}

func (resumeAllStatusFake) ResumeAll(context.Context, orchestrator.ResumeAllRequest) ([]orchestrator.ResumeResult, error) {
	return []orchestrator.ResumeResult{{ProjectSlug: "one", Kind: state.RerunResume}}, nil
}

func TestResumeWithoutSelectorPrintsSuccessfulVerificationWarnings(t *testing.T) {
	projects := &resumeAllStatusProjects{project: state.ProjectState{
		Name: "One", Slug: "one", Status: state.StatusFinished,
		Verification: &state.VerificationState{
			CurrentResults: []state.VerificationCommandResult{{CheckName: "tests", Command: "go", Args: []string{"test", "./..."}, Status: "passed"}},
			Warnings:       []state.VerificationFinding{{CheckName: "tests", Identity: "TestLegacy", Reason: "known failure", Classification: "unchanged_baseline"}},
			NextAction:     "continue; warning retained",
		},
	}}
	app := cli.New(cli.WithResumeCoordinator(resumeAllStatusFake{}), cli.WithLifecycleService(projects))
	var stdout bytes.Buffer
	if code := app.Run(context.Background(), []string{"resume"}, &stdout, &strings.Builder{}); code != 0 {
		t.Fatalf("code=%d output=%q", code, stdout.String())
	}
	for _, want := range []string{"Resumed 1 project(s).", "Verification summary for One:", "Check: tests", "Identity: TestLegacy", "Next action: continue; warning retained"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, stdout.String())
		}
	}
}

type resumeAllStatusProjects struct{ project state.ProjectState }

func (s *resumeAllStatusProjects) Create(context.Context, state.ProjectState) error { return nil }
func (s *resumeAllStatusProjects) Load(_ context.Context, slug string) (state.ProjectState, error) {
	if slug != s.project.Slug {
		return state.ProjectState{}, os.ErrNotExist
	}
	return s.project, nil
}
func (s *resumeAllStatusProjects) List(context.Context) ([]state.ProjectState, error) {
	return []state.ProjectState{s.project}, nil
}
func (s *resumeAllStatusProjects) Delete(context.Context, string) error { return nil }
func (s *resumeAllStatusProjects) Transition(context.Context, string, state.LifecycleStatus, string, string, []string) (state.ProjectState, error) {
	return s.project, nil
}
