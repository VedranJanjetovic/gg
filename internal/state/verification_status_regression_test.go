package state

import "testing"

// A run resumed with --skip-checks retains the strict preflight results of the
// quarantined checks in CurrentResults. VerificationIsPaused must ignore them,
// exactly as VerificationBlockingResults does, so a progressing run is not
// presented as running[paused] with skip/fix controls still offered.
func TestVerificationIsPausedIgnoresQuarantinedStrictResults(t *testing.T) {
	project := ProjectState{Verification: &VerificationState{
		CurrentResults: []VerificationCommandResult{
			{CheckName: "affected-unit-tests", Status: "unclassifiable"},
			{CheckName: "affected-race-tests", Status: "unclassifiable"},
			{CheckName: "proto-lint", Status: "passed"},
		},
		CurrentFindings: []VerificationFinding{
			{CheckName: "affected-unit-tests", Classification: "unclassifiable"},
		},
		QuarantinedChecks: []VerificationQuarantine{
			{CheckName: "affected-unit-tests", BaselineStatus: "unclassifiable"},
			{CheckName: "affected-race-tests", BaselineStatus: "unclassifiable"},
		},
	}}

	if VerificationIsPaused(project) {
		t.Fatalf("paused = true for a run whose only strict results are quarantined, want false")
	}

	project.Verification.CurrentResults = append(project.Verification.CurrentResults,
		VerificationCommandResult{CheckName: "diff-check", Status: "unavailable"})
	if !VerificationIsPaused(project) {
		t.Fatalf("paused = false with a non-quarantined unavailable result, want true")
	}
}
