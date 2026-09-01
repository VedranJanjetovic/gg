package cli

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/state"
)

func quarantineApp(t *testing.T) (*App, string) {
	t.Helper()
	root := t.TempDir()
	store, err := state.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	service := state.NewLifecycleService(store, nil, store.Locker())
	project := state.ProjectState{
		SchemaVersion:      state.CurrentSchemaVersion,
		Name:               "Example Project",
		Slug:               "example-project",
		OriginalGoal:       "Build the project",
		AcceptanceCriteria: []string{"It works"},
		PipelineConfig:     state.PipelineConfigSnapshot{SchemaVersion: 1, Data: json.RawMessage(`{"phases":["build"]}`)},
		CurrentPhase:       "build",
		Status:             state.StatusStopped,
		WorktreePath:       root,
		BranchName:         "agent/example-project",
	}
	if err := service.Create(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	contract := state.VerificationContract{Steps: []state.VerificationStep{
		{Name: "affected-unit-tests", Command: "go", Args: []string{"test"}, Adapter: state.VerificationAdapterGoTest},
		{Name: "affected-race-tests", Command: "go", Args: []string{"test", "-race"}, Adapter: state.VerificationAdapterGoTest},
	}}
	if _, err := service.SetVerificationContract(context.Background(), project.Slug, contract, state.PipelineConfigSnapshot{SchemaVersion: 1, Data: json.RawMessage(`{"verificationSteps":[]}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecordVerificationResultReport(context.Background(), project.Slug, []state.VerificationCommandResult{
		{CheckName: "affected-unit-tests", Command: "go", Status: "unclassifiable", LogPath: ".gg/logs/unit.log", UnavailableErr: "docker is not running"},
		{CheckName: "affected-race-tests", Command: "go", Status: "unclassifiable", LogPath: ".gg/logs/race.log"},
	}, nil, nil, "parent-preflight", 0, "make every planned verification step executable, then resume"); err != nil {
		t.Fatal(err)
	}
	return New(WithLifecycleService(service)), project.Slug
}

func TestResumeSkipChecksPersistsTheQuarantineWithItsRecordedEvidence(t *testing.T) {
	app, slug := quarantineApp(t)
	var stdout strings.Builder
	if err := app.quarantineVerificationChecks(context.Background(), &stdout, slug, []string{"affected-race-tests", "affected-unit-tests"}); err != nil {
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
	want := []state.VerificationQuarantine{
		{CheckName: "affected-race-tests", BaselineStatus: "unclassifiable", LogPath: ".gg/logs/race.log"},
		{CheckName: "affected-unit-tests", BaselineStatus: "unclassifiable", Reason: "docker is not running", LogPath: ".gg/logs/unit.log"},
	}
	if !reflect.DeepEqual(project.Verification.QuarantinedChecks, want) {
		t.Fatalf("quarantined checks = %#v, want %#v", project.Verification.QuarantinedChecks, want)
	}
	if !strings.Contains(stdout.String(), "affected-race-tests, affected-unit-tests") || !strings.Contains(stdout.String(), "no regression signal") {
		t.Fatalf("stdout = %q, want it to name the checks and state they carry no regression signal", stdout.String())
	}
}

func TestResumeSkipChecksRejectsAnUnknownCheckNameAndListsThePlannedSteps(t *testing.T) {
	app, slug := quarantineApp(t)
	var stdout strings.Builder
	err := app.quarantineVerificationChecks(context.Background(), &stdout, slug, []string{"affected-unit-test"})
	if err == nil {
		t.Fatal("an unknown check name was accepted")
	}
	if !strings.Contains(err.Error(), "affected-unit-tests") || !strings.Contains(err.Error(), "affected-race-tests") {
		t.Fatalf("error = %q, want it to list the planned step names", err.Error())
	}
	service, err2 := app.projectService(context.Background())
	if err2 != nil {
		t.Fatal(err2)
	}
	project, err2 := service.Load(context.Background(), slug)
	if err2 != nil {
		t.Fatal(err2)
	}
	if len(project.Verification.QuarantinedChecks) != 0 {
		t.Fatalf("a rejected quarantine persisted %#v", project.Verification.QuarantinedChecks)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want nothing printed for a rejected quarantine", stdout.String())
	}
}

func TestResumeWithoutASelectorRejectsSkipChecks(t *testing.T) {
	app := New()
	var stdout strings.Builder
	err := app.resume(context.Background(), &stdout, resumeOptions{skipChecks: []string{"affected-unit-tests"}})
	if err == nil || !strings.Contains(err.Error(), "requires a project selector") {
		t.Fatalf("err = %v, want a selector requirement", err)
	}
}

func TestParseResumeOptionsAcceptsEverySkipChecksSpelling(t *testing.T) {
	tests := []struct {
		args []string
		want []string
	}{
		{args: []string{"proj", "--skip-checks=a,b"}, want: []string{"a", "b"}},
		{args: []string{"proj", "-skip-checks=a,b"}, want: []string{"a", "b"}},
		{args: []string{"--skip-checks", "a,b", "proj"}, want: []string{"a", "b"}},
		{args: []string{"proj", "--skip-checks", "a, b ,"}, want: []string{"a", "b"}},
		{args: []string{"proj", "--skip-checks="}},
		{args: []string{"proj"}},
	}
	for _, test := range tests {
		t.Run(strings.Join(test.args, " "), func(t *testing.T) {
			options, err := parseResumeOptions(test.args)
			if err != nil {
				t.Fatal(err)
			}
			if options.selector != "proj" {
				t.Fatalf("selector = %q, want %q", options.selector, "proj")
			}
			if !reflect.DeepEqual(options.skipChecks, test.want) {
				t.Fatalf("skipChecks = %#v, want %#v", options.skipChecks, test.want)
			}
		})
	}
}
