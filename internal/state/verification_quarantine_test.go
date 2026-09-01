package state

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func quarantineFixture(t *testing.T) (*LifecycleService, string, string) {
	t.Helper()
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	service := NewLifecycleService(store, nil, store.Locker())
	project := validProjectState()
	if err := service.Create(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	contract := VerificationContract{Steps: []VerificationStep{
		{Name: "affected-unit-tests", Command: "go", Args: []string{"test"}, Adapter: VerificationAdapterGoTest},
		{Name: "affected-race-tests", Command: "go", Args: []string{"test", "-race"}, Adapter: VerificationAdapterGoTest},
	}}
	if _, err := service.SetVerificationContract(context.Background(), project.Slug, contract, PipelineConfigSnapshot{SchemaVersion: 1, Data: json.RawMessage(`{"verificationSteps":[]}`)}); err != nil {
		t.Fatal(err)
	}
	return service, root, project.Slug
}

func TestQuarantineVerificationChecksMergesByNameAndSortsDeterministically(t *testing.T) {
	service, _, slug := quarantineFixture(t)
	if _, err := service.QuarantineVerificationChecks(context.Background(), slug, []VerificationQuarantine{
		{CheckName: "affected-race-tests", BaselineStatus: "unavailable"},
		{CheckName: "affected-unit-tests", BaselineStatus: "unclassifiable", Reason: "docker is not running"},
	}); err != nil {
		t.Fatal(err)
	}
	updated, err := service.QuarantineVerificationChecks(context.Background(), slug, []VerificationQuarantine{
		{CheckName: "affected-unit-tests", BaselineStatus: "unavailable", Reason: "docker daemon absent", LogPath: ".gg/logs/unit.log"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []VerificationQuarantine{
		{CheckName: "affected-race-tests", BaselineStatus: "unavailable"},
		{CheckName: "affected-unit-tests", BaselineStatus: "unavailable", Reason: "docker daemon absent", LogPath: ".gg/logs/unit.log"},
	}
	if !reflect.DeepEqual(updated.Verification.QuarantinedChecks, want) {
		t.Fatalf("quarantined checks = %#v, want %#v", updated.Verification.QuarantinedChecks, want)
	}
}

func TestQuarantineVerificationChecksRoundTripsThroughTheStore(t *testing.T) {
	service, _, slug := quarantineFixture(t)
	if _, err := service.QuarantineVerificationChecks(context.Background(), slug, []VerificationQuarantine{{CheckName: "affected-unit-tests", BaselineStatus: "unclassifiable", Reason: "docker is not running", LogPath: ".gg/logs/unit.log"}}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := service.Load(context.Background(), slug)
	if err != nil {
		t.Fatal(err)
	}
	want := []VerificationQuarantine{{CheckName: "affected-unit-tests", BaselineStatus: "unclassifiable", Reason: "docker is not running", LogPath: ".gg/logs/unit.log"}}
	if !reflect.DeepEqual(reloaded.Verification.QuarantinedChecks, want) {
		t.Fatalf("reloaded quarantines = %#v, want %#v", reloaded.Verification.QuarantinedChecks, want)
	}
}

func TestQuarantineVerificationChecksRejectsAnEmptyCheckNameAndAMissingContract(t *testing.T) {
	service, _, slug := quarantineFixture(t)
	if _, err := service.QuarantineVerificationChecks(context.Background(), slug, []VerificationQuarantine{{CheckName: "  "}}); err == nil {
		t.Fatal("empty check name was accepted")
	}
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	bare := NewLifecycleService(store, nil, store.Locker())
	project := validProjectState()
	if err := bare.Create(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	if _, err := bare.QuarantineVerificationChecks(context.Background(), project.Slug, []VerificationQuarantine{{CheckName: "affected-unit-tests"}}); err == nil {
		t.Fatal("project without a verification contract accepted a quarantine")
	}
}

func TestProjectStateWrittenWithoutQuarantinedChecksStillDecodes(t *testing.T) {
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
		t.Fatalf("state.json has no verification object: %s", raw)
	}
	if _, present := verification["quarantinedChecks"]; present {
		t.Fatalf("an unquarantined project must not emit quarantinedChecks: %s", raw)
	}
	reloaded, err := service.Load(context.Background(), slug)
	if err != nil {
		t.Fatalf("state.json written without quarantinedChecks failed to decode: %v", err)
	}
	if reloaded.Verification == nil || len(reloaded.Verification.QuarantinedChecks) != 0 {
		t.Fatalf("reloaded verification = %#v, want no quarantines", reloaded.Verification)
	}
}
