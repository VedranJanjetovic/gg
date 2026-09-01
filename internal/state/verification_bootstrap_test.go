package state

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// blockedFixture parks the shared quarantine fixture on an unclassifiable
// check, which is the only state from which a bootstrap may be requested.
func blockedFixture(t *testing.T) (*LifecycleService, string) {
	t.Helper()
	service, _, slug := quarantineFixture(t)
	if _, err := service.RecordVerificationResultReport(context.Background(), slug, []VerificationCommandResult{
		{CheckName: "affected-unit-tests", Command: "go", Args: []string{"test"}, Status: "unavailable", UnavailableErr: "go: command not found", LogPath: ".gg/logs/unit.log"},
		{CheckName: "affected-race-tests", Command: "go", Args: []string{"test", "-race"}, Status: "passed"},
	}, nil, nil, "parent-preflight", 0, "make every planned verification step executable, then resume"); err != nil {
		t.Fatal(err)
	}
	return service, slug
}

func TestRequestVerificationBootstrapMarksTheRequestForABlockedProject(t *testing.T) {
	service, slug := blockedFixture(t)
	updated, err := service.RequestVerificationBootstrap(context.Background(), slug)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.Verification.BootstrapRequested {
		t.Fatalf("bootstrapRequested = false, want true")
	}
	reloaded, err := service.Load(context.Background(), slug)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.Verification.BootstrapRequested {
		t.Fatalf("reloaded bootstrapRequested = false, want true")
	}
}

func TestRequestVerificationBootstrapRejectsAProjectWithNoBlockingCheck(t *testing.T) {
	service, _, slug := quarantineFixture(t)
	if _, err := service.RecordVerificationResultReport(context.Background(), slug, []VerificationCommandResult{
		{CheckName: "affected-unit-tests", Command: "go", Args: []string{"test"}, Status: "passed"},
	}, nil, nil, "parent-preflight", 0, "continue"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RequestVerificationBootstrap(context.Background(), slug); err == nil {
		t.Fatal("bootstrap was accepted for a project with no unclassifiable check")
	} else if !strings.Contains(err.Error(), "unavailable or unclassifiable") {
		t.Fatalf("error = %v, want it to name the missing blocking check", err)
	}
}

func TestRequestVerificationBootstrapRejectsAnAlreadyQuarantinedBlockingCheck(t *testing.T) {
	service, slug := blockedFixture(t)
	if _, err := service.QuarantineVerificationChecks(context.Background(), slug, []VerificationQuarantine{{CheckName: "affected-unit-tests", BaselineStatus: "unavailable"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RequestVerificationBootstrap(context.Background(), slug); err == nil {
		t.Fatal("bootstrap was accepted although the only blocking check is quarantined")
	}
}

func TestRecordVerificationBootstrapPhaseRoundTripsAndRejectsABlankName(t *testing.T) {
	service, slug := blockedFixture(t)
	if _, err := service.RecordVerificationBootstrapPhase(context.Background(), slug, "   "); err == nil {
		t.Fatal("a blank bootstrap phase name was accepted")
	}
	if _, err := service.RecordVerificationBootstrapPhase(context.Background(), slug, " Make checks executable "); err != nil {
		t.Fatal(err)
	}
	reloaded, err := service.Load(context.Background(), slug)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := reloaded.Verification.BootstrapPhase, "Make checks executable"; got != want {
		t.Fatalf("bootstrapPhase = %q, want %q", got, want)
	}
}

func TestCompleteVerificationBootstrapPromotesThePhaseAndClearsTheRequest(t *testing.T) {
	service, slug := blockedFixture(t)
	if _, err := service.RequestVerificationBootstrap(context.Background(), slug); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecordVerificationBootstrapPhase(context.Background(), slug, "Make checks executable"); err != nil {
		t.Fatal(err)
	}
	updated, err := service.CompleteVerificationBootstrap(context.Background(), slug)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Verification.BootstrapRequested {
		t.Fatalf("bootstrapRequested = true, want false after completion")
	}
	if got, want := updated.Verification.BaselineAfterPhase, "Make checks executable"; got != want {
		t.Fatalf("baselineAfterPhase = %q, want %q", got, want)
	}
	if got, want := updated.Verification.BootstrapPhase, "Make checks executable"; got != want {
		t.Fatalf("bootstrapPhase = %q, want it retained as %q", got, want)
	}
}

func TestProjectStateWrittenWithoutTheBootstrapFieldsStillDecodes(t *testing.T) {
	service, root, slug := quarantineFixture(t)
	path := filepath.Join(root, ".gg", "projects", slug, "state.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	verification, ok := decoded["verification"].(map[string]any)
	if !ok {
		t.Fatalf("persisted state has no verification object: %s", raw)
	}
	for _, field := range []string{"bootstrapRequested", "bootstrapPhase", "baselineAfterPhase"} {
		if _, present := verification[field]; present {
			t.Fatalf("field %q was persisted although it is unset", field)
		}
		delete(verification, field)
	}
	rewritten, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, rewritten, 0o600); err != nil {
		t.Fatal(err)
	}
	reloaded, err := service.Load(context.Background(), slug)
	if err != nil {
		t.Fatalf("state without the bootstrap fields failed to decode: %v", err)
	}
	if reloaded.Verification.BootstrapRequested || reloaded.Verification.BootstrapPhase != "" || reloaded.Verification.BaselineAfterPhase != "" {
		t.Fatalf("bootstrap fields = %#v, want the zero values", reloaded.Verification)
	}
}

func TestVerificationBlockingResultsExcludesHealthyAndQuarantinedChecks(t *testing.T) {
	service, slug := blockedFixture(t)
	project, err := service.Load(context.Background(), slug)
	if err != nil {
		t.Fatal(err)
	}
	names := VerificationBlockingCheckNames(project)
	if len(names) != 1 || names[0] != "affected-unit-tests" {
		t.Fatalf("blocking check names = %#v, want [affected-unit-tests]", names)
	}
	if _, err := service.QuarantineVerificationChecks(context.Background(), slug, []VerificationQuarantine{{CheckName: "affected-unit-tests"}}); err != nil {
		t.Fatal(err)
	}
	project, err = service.Load(context.Background(), slug)
	if err != nil {
		t.Fatal(err)
	}
	if names := VerificationBlockingCheckNames(project); names != nil {
		t.Fatalf("blocking check names = %#v, want none once quarantined", names)
	}
}
