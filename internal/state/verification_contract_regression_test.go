package state

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

// These tests pin a fixed bug: SetVerificationContract replaced the whole
// VerificationState, so the --fix-checks bootstrap request set before the
// Planning re-run was wiped when that re-run persisted its new contract. The
// deferred preflight then never happened and the run parked on the same
// blocked check forever. Standing user decisions — the bootstrap request and
// quarantined checks — must survive a re-declared contract; observations and
// the plan-specific bootstrap cursor must not.

func replannedContract() (VerificationContract, PipelineConfigSnapshot) {
	contract := VerificationContract{Steps: []VerificationStep{
		{Name: "affected-unit-tests", Command: "go", Args: []string{"test"}, Adapter: VerificationAdapterGoTest},
	}}
	return contract, PipelineConfigSnapshot{SchemaVersion: 1, Data: json.RawMessage(`{"verificationSteps":[]}`)}
}

func TestSetVerificationContractPreservesTheBootstrapRequestAcrossAFixChecksReplan(t *testing.T) {
	service, slug := blockedFixture(t)
	if _, err := service.RequestVerificationBootstrap(context.Background(), slug); err != nil {
		t.Fatal(err)
	}
	contract, snapshot := replannedContract()
	updated, err := service.SetVerificationContract(context.Background(), slug, contract, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.Verification.BootstrapRequested {
		t.Fatalf("bootstrapRequested = false after the replan persisted its contract, want true")
	}
	reloaded, err := service.Load(context.Background(), slug)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.Verification.BootstrapRequested {
		t.Fatalf("reloaded bootstrapRequested = false, want true")
	}
}

func TestSetVerificationContractResetsThePlanSpecificBootstrapCursor(t *testing.T) {
	service, slug := blockedFixture(t)
	if _, err := service.RequestVerificationBootstrap(context.Background(), slug); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecordVerificationBootstrapPhase(context.Background(), slug, "Phase 1: old plan's bootstrap"); err != nil {
		t.Fatal(err)
	}
	contract, snapshot := replannedContract()
	updated, err := service.SetVerificationContract(context.Background(), slug, contract, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Verification.BootstrapPhase != "" {
		t.Fatalf("bootstrapPhase = %q, want it reset so the deferral re-records the new plan's leading phase", updated.Verification.BootstrapPhase)
	}
	if !updated.Verification.BootstrapRequested {
		t.Fatalf("bootstrapRequested = false, want the request itself retained")
	}
}

func TestSetVerificationContractPreservesQuarantinedChecksAcrossAReplan(t *testing.T) {
	service, _, slug := quarantineFixture(t)
	quarantines := []VerificationQuarantine{{CheckName: "affected-unit-tests", BaselineStatus: "unavailable", Reason: "docker daemon absent"}}
	if _, err := service.QuarantineVerificationChecks(context.Background(), slug, quarantines); err != nil {
		t.Fatal(err)
	}
	contract, snapshot := replannedContract()
	updated, err := service.SetVerificationContract(context.Background(), slug, contract, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(updated.Verification.QuarantinedChecks, quarantines) {
		t.Fatalf("quarantinedChecks = %#v, want %#v retained across the replan", updated.Verification.QuarantinedChecks, quarantines)
	}
}
