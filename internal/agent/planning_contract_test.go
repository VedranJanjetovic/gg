package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func planningFixture(category PlanningComplexity, phases, evidence []string, boundaries []PlanningPhaseBoundary) string {
	var b strings.Builder
	evidenceJSON, _ := json.Marshal(evidence)
	phasesJSON, _ := json.Marshal(phases)
	boundariesJSON, _ := json.Marshal(boundaries)
	fmt.Fprintf(&b, "---\ngg_run_id: \"run\"\ngg_disposition: passed\ngg_plan_complexity: %q\ngg_plan_complexity_evidence: %s\ngg_plan_phases: %s\ngg_plan_phase_boundaries: %s\n---\n", category, evidenceJSON, phasesJSON, boundariesJSON)
	fmt.Fprintf(&b, "# Implementation Plan\n\n## Complexity assessment\n\n- Complexity category: **%s**\n- Selected phase count: **%d**\n\nSupporting evidence:\n\n", category, len(phases))
	for index, item := range evidence {
		fmt.Fprintf(&b, "%d. %s\n", index+1, item)
	}
	b.WriteString("\n")
	for index, phase := range phases {
		justification := ""
		if index < len(boundaries) {
			justification = boundaries[index].Justification
		}
		fmt.Fprintf(&b, "## %s\n\nBoundary justification: %s\n\n", phase, justification)
	}
	return b.String()
}

func TestValidatePlanningArtifactAcceptsStructuralContract(t *testing.T) {
	data := planningFixture(PlanningTrivial, []string{"Phase 1: README wording"}, []string{"One cohesive localized outcome with no dependency ordering."}, []PlanningPhaseBoundary{{Phase: "Phase 1: README wording", Justification: "The outcome is cohesive and needs no split."}})
	artifact, err := ParsePlanningArtifact([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if artifact.BodyPhaseCount != 1 || artifact.BodyPhases[0] != "Phase 1: README wording" {
		t.Fatalf("body = %#v", artifact)
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".gg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gg", "plan.md"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidatePlanningArtifact(dir); err != nil {
		t.Fatal(err)
	}
}

func TestValidatePlanningArtifactReportsAllContractViolations(t *testing.T) {
	data := planningFixture(PlanningTrivial, []string{"Phase 1: one", "Phase 1: one", "Phase 3: three", "Phase 4: four", "Phase 5: five", "Phase 6: six", "Phase 7: seven", "Phase 8: eight", "Phase 9: nine", "Phase 10: ten", "Phase 11: eleven"}, nil, []PlanningPhaseBoundary{{Phase: "wrong", Justification: ""}})
	data = strings.Replace(data, `gg_plan_complexity: "Trivial"`, `gg_plan_complexity: "Unknown"`, 1)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".gg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gg", "plan.md"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ValidatePlanningArtifact(dir)
	var contractErr *PlanningContractError
	if !errors.As(err, &contractErr) {
		t.Fatalf("error = %v, want PlanningContractError", err)
	}
	for _, want := range []string{
		"is invalid",
		"must contain at least one item",
		"phase-limit-exceeded",
		"duplicate phase",
		"phase boundary",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q: %v", want, err)
		}
	}
	if len(contractErr.Violations) < 6 {
		t.Fatalf("violations = %#v, want all deterministic errors", contractErr.Violations)
	}
}

func TestValidatePlanningArtifactKeepsAdvisoryBandsNonGating(t *testing.T) {
	phases := []string{"Phase 1: one", "Phase 2: two", "Phase 3: three"}
	boundaries := []PlanningPhaseBoundary{{Phase: phases[0], Justification: "first"}, {Phase: phases[1], Justification: "second"}, {Phase: phases[2], Justification: "third"}}
	data := planningFixture(PlanningSimple, phases, []string{"One localized backward-compatible component."}, boundaries)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".gg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gg", "plan.md"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidatePlanningArtifact(dir); err != nil {
		t.Fatalf("advisory band rejected: %v", err)
	}
}

func TestValidatePlanningArtifactEnforcesExactlyOneTrivialPhase(t *testing.T) {
	phases := []string{"Phase 1: first", "Phase 2: second"}
	boundaries := []PlanningPhaseBoundary{{Phase: phases[0], Justification: "first"}, {Phase: phases[1], Justification: "second"}}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".gg"), 0o755); err != nil {
		t.Fatal(err)
	}
	data := planningFixture(PlanningTrivial, phases, []string{"The planner reported one cohesive outcome."}, boundaries)
	if err := os.WriteFile(filepath.Join(dir, ".gg", "plan.md"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ValidatePlanningArtifact(dir)
	if err == nil || !strings.Contains(err.Error(), "exactly one phase") {
		t.Fatalf("error=%v, want Trivial phase-count violation", err)
	}
}
