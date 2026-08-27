package state

import "testing"

func TestVerificationDisplayJoinsCommandEvidenceAndRetainsWarnings(t *testing.T) {
	project := ProjectState{Verification: &VerificationState{
		CurrentResults: []VerificationCommandResult{{
			CheckName: "tests", Command: "go", Args: []string{"test", "./..."}, LogPath: ".gg/logs/tests.log",
		}},
		CurrentFindings:     []VerificationFinding{{CheckName: "tests", Identity: "pkg/Test", Reason: "panic: baseline", Classification: "new"}},
		Warnings:            []VerificationFinding{{CheckName: "tests", Identity: "pkg/Old", Reason: "known failure", Classification: "unchanged_baseline"}},
		RemediationAttempts: 2,
	}}

	findings := VerificationDisplay(project)
	if len(findings) != 2 {
		t.Fatalf("display findings = %#v, want current finding and warning", findings)
	}
	if findings[0].Command != "go test ./..." || findings[0].LogPath != ".gg/logs/tests.log" || findings[0].Attempts != 2 || findings[0].MaxAttempts != 3 {
		t.Fatalf("current display finding = %#v", findings[0])
	}
	if findings[1].Warning != true || findings[1].Classification != "unchanged_baseline" {
		t.Fatalf("warning display finding = %#v", findings[1])
	}
}

func TestVerificationIsPausedOnlyForStrictVerificationFailures(t *testing.T) {
	for _, test := range []struct {
		name     string
		status   string
		failures []VerificationFinding
		want     bool
	}{
		{name: "unavailable", status: "unavailable", want: true},
		{name: "unclassifiable", status: "unclassifiable", want: true},
		{name: "failed without identity", status: "failed", want: true},
		{name: "ordinary failure", status: "failed", failures: []VerificationFinding{{Identity: "TestFailure", Reason: "known failure"}}, want: false},
		{name: "passed", status: "passed", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			project := ProjectState{Verification: &VerificationState{CurrentResults: []VerificationCommandResult{{CheckName: "tests", Status: test.status, Failures: test.failures}}}}
			if got := VerificationIsPaused(project); got != test.want {
				t.Fatalf("paused = %t, want %t", got, test.want)
			}
		})
	}
}

func TestVerificationDisplayIncludesStrictPauseEvidenceWithoutIndividualFailure(t *testing.T) {
	project := ProjectState{Verification: &VerificationState{
		CurrentResults: []VerificationCommandResult{{CheckName: "tests", Status: "unavailable", UnavailableErr: "go executable is not available"}},
		ParentResults:  []VerificationCommandResult{{CheckName: "tests", LogPath: ".gg/logs/tests.log"}},
		PlannedSteps:   []VerificationStep{{Name: "tests", Command: "go", Args: []string{"test", "./..."}}},
		NextAction:     "make every planned verification step executable, then resume",
	}}

	findings := VerificationDisplay(project)
	if len(findings) != 1 {
		t.Fatalf("display findings = %#v, want one strict-pause finding", findings)
	}
	finding := findings[0]
	if finding.CheckName != "tests" || finding.Command != "go test ./..." || finding.Identity != "" ||
		finding.Reason != "go executable is not available" || finding.Classification != "unavailable" ||
		finding.LogPath != ".gg/logs/tests.log" || finding.MaxAttempts != MaxVerificationRemediationAttempts {
		t.Fatalf("strict-pause display finding = %#v", finding)
	}
}

func TestVerificationDisplayFallsBackToPlannedCommandForLegacyFinding(t *testing.T) {
	project := ProjectState{Verification: &VerificationState{
		PlannedSteps:    []VerificationStep{{Name: "tests", Command: "go", Args: []string{"test", "./..."}}},
		CurrentFindings: []VerificationFinding{{CheckName: "tests", Identity: "pkg/TestLegacy", Reason: "known failure", Classification: "unchanged_baseline"}},
	}}

	findings := VerificationDisplay(project)
	if len(findings) != 1 || findings[0].Command != "go test ./..." {
		t.Fatalf("legacy display findings = %#v, want planned command", findings)
	}
}

func TestVerificationIsPausedForMalformedOrUnknownResults(t *testing.T) {
	for _, result := range []VerificationCommandResult{
		{CheckName: "tests", Status: "passed", Failures: []VerificationFinding{{CheckName: "tests"}}},
		{CheckName: "tests", Status: "failed", Failures: []VerificationFinding{{CheckName: "tests", Identity: "TestFailure"}}},
		{CheckName: "tests", Status: "unexpected"},
	} {
		project := ProjectState{Verification: &VerificationState{CurrentResults: []VerificationCommandResult{result}}}
		if !VerificationIsPaused(project) {
			t.Fatalf("result %#v was not classified as a strict pause", result)
		}
	}
}
