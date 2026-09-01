package orchestrator

import (
	"reflect"
	"strings"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/state"
	"github.com/VedranJanjetovic/gg/internal/verification"
)

func TestHasUnavailableOrUnclassifiableIgnoresTheStatusOfAQuarantinedCheck(t *testing.T) {
	steps := []verification.Step{{Name: "affected-unit-tests"}, {Name: "vet"}}
	report := verification.Report{Results: []verification.CommandResult{
		{StepName: "affected-unit-tests", Status: verification.CommandUnclassifiable},
		{StepName: "vet", Status: verification.CommandPassed},
	}}
	if hasUnavailableOrUnclassifiable(report, steps, map[string]struct{}{"affected-unit-tests": {}}) {
		t.Fatalf("quarantined unclassifiable check still blocked the preflight")
	}
	if !hasUnavailableOrUnclassifiable(report, steps, nil) {
		t.Fatalf("unquarantined unclassifiable check must still block the preflight")
	}
}

func TestHasUnavailableOrUnclassifiableStillEnforcesStructureForQuarantinedChecks(t *testing.T) {
	steps := []verification.Step{{Name: "affected-unit-tests"}, {Name: "vet"}}
	quarantined := map[string]struct{}{"affected-unit-tests": {}, "ghost": {}}
	tests := []struct {
		name   string
		report verification.Report
	}{
		{name: "duplicate step name", report: verification.Report{Results: []verification.CommandResult{
			{StepName: "affected-unit-tests", Status: verification.CommandUnclassifiable},
			{StepName: "affected-unit-tests", Status: verification.CommandUnclassifiable},
		}}},
		{name: "unknown step name", report: verification.Report{Results: []verification.CommandResult{
			{StepName: "ghost", Status: verification.CommandUnclassifiable},
			{StepName: "vet", Status: verification.CommandPassed},
		}}},
		{name: "missing result", report: verification.Report{Results: []verification.CommandResult{
			{StepName: "affected-unit-tests", Status: verification.CommandUnclassifiable},
		}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if !hasUnavailableOrUnclassifiable(test.report, steps, quarantined) {
				t.Fatalf("quarantine relaxed a structural invariant for %s", test.name)
			}
		})
	}
}

func TestPreflightBlockedErrorNamesOnlyTheBlockingChecksAndTheSkipCommand(t *testing.T) {
	report := verification.Report{Results: []verification.CommandResult{
		{StepName: "affected-unit-tests", Status: verification.CommandUnclassifiable},
		{StepName: "affected-race-tests", Status: verification.CommandUnavailable, UnavailableErr: "docker: not running"},
		{StepName: "gofmt", Status: verification.CommandUnclassifiable},
		{StepName: "vet", Status: verification.CommandPassed},
	}}
	err := preflightBlockedError(report, "demo", map[string]struct{}{"gofmt": {}})
	if err == nil {
		t.Fatalf("blocked preflight produced no error")
	}
	want := "gg resume demo --skip-checks=affected-unit-tests,affected-race-tests"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %q, want it to contain %q", err.Error(), want)
	}
	if strings.Contains(err.Error(), "gofmt,") || strings.Contains(err.Error(), ",gofmt") {
		t.Fatalf("error = %q, want the already quarantined check excluded from the skip list", err.Error())
	}
}

func TestCompareOptionsCarriesQuarantinedCheckNames(t *testing.T) {
	options := compareOptions(state.VerificationState{
		PlannedSteps:      []state.VerificationStep{{Name: "affected-unit-tests"}},
		QuarantinedChecks: []state.VerificationQuarantine{{CheckName: "affected-unit-tests"}, {CheckName: "  "}},
	})
	if !reflect.DeepEqual(options.Quarantined, []string{"affected-unit-tests"}) {
		t.Fatalf("quarantined=%#v, want the named check only", options.Quarantined)
	}
}
