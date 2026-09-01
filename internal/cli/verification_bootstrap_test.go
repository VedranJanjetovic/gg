package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/pipeline"
)

func TestResumeFixChecksRecordsTheRequestAndRewindsTheCursorToPlanning(t *testing.T) {
	app, slug := quarantineApp(t)
	var stdout strings.Builder
	if err := app.requestVerificationBootstrap(context.Background(), &stdout, slug); err != nil {
		t.Fatal(err)
	}
	service, err := app.projectService(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	project, err := service.Load(context.Background(), slug)
	if err != nil {
		t.Fatal(err)
	}
	if !project.Verification.BootstrapRequested {
		t.Fatal("bootstrapRequested was not persisted")
	}
	if got, want := project.ReplanContinuationPhase, string(pipeline.PhasePlanning); got != want {
		t.Fatalf("replan continuation phase = %q, want %q", got, want)
	}
	if !strings.Contains(stdout.String(), "affected-unit-tests") || !strings.Contains(stdout.String(), "Planning re-runs") {
		t.Fatalf("stdout = %q, want it to name the blocked checks and the Planning rerun", stdout.String())
	}
}

func TestResumeFixChecksIsRejectedWhenNoCheckIsBlocking(t *testing.T) {
	app, slug := quarantineApp(t)
	var stdout strings.Builder
	if err := app.quarantineVerificationChecks(context.Background(), &stdout, slug, []string{"affected-unit-tests", "affected-race-tests"}); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	err := app.requestVerificationBootstrap(context.Background(), &stdout, slug)
	if err == nil {
		t.Fatal("a bootstrap was accepted although every blocking check is quarantined")
	}
	if !strings.Contains(err.Error(), "unavailable or unclassifiable") {
		t.Fatalf("error = %v, want it to explain there is nothing to repair", err)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want nothing printed for a rejected bootstrap", stdout.String())
	}
}

func TestResumeWithoutASelectorRejectsFixChecks(t *testing.T) {
	app := New()
	var stdout strings.Builder
	err := app.resume(context.Background(), &stdout, resumeOptions{fixChecks: true})
	if err == nil || !strings.Contains(err.Error(), "requires a project selector") {
		t.Fatalf("err = %v, want a selector requirement", err)
	}
}

func TestParseResumeOptionsRejectsFixChecksTogetherWithSkipChecks(t *testing.T) {
	if _, err := parseResumeOptions([]string{"proj", "--fix-checks", "--skip-checks=a"}); err == nil {
		t.Fatal("--fix-checks and --skip-checks were accepted together")
	} else if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("error = %v, want a mutual-exclusion message", err)
	}
}

func TestParseResumeOptionsAcceptsEveryFixChecksSpelling(t *testing.T) {
	tests := []struct {
		args         []string
		wantSelector string
		wantFix      bool
	}{
		{args: []string{"proj", "--fix-checks"}, wantSelector: "proj", wantFix: true},
		{args: []string{"proj", "-fix-checks"}, wantSelector: "proj", wantFix: true},
		{args: []string{"--fix-checks", "proj"}, wantSelector: "proj", wantFix: true},
		{args: []string{"--fix-checks=true", "proj"}, wantSelector: "proj", wantFix: true},
		{args: []string{"--fix-checks=false", "proj"}, wantSelector: "proj"},
		{args: []string{"proj"}, wantSelector: "proj"},
	}
	for _, test := range tests {
		t.Run(strings.Join(test.args, " "), func(t *testing.T) {
			options, err := parseResumeOptions(test.args)
			if err != nil {
				t.Fatal(err)
			}
			if options.selector != test.wantSelector {
				t.Fatalf("selector = %q, want %q", options.selector, test.wantSelector)
			}
			if options.fixChecks != test.wantFix {
				t.Fatalf("fixChecks = %t, want %t", options.fixChecks, test.wantFix)
			}
		})
	}
}
