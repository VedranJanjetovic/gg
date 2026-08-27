package state

import "strings"

// VerificationDisplayFinding is the concise, user-facing projection of one
// persisted verification finding. It deliberately excludes command output;
// LogPath points to the bounded evidence for users who need more detail.
type VerificationDisplayFinding struct {
	CheckName      string
	Command        string
	Identity       string
	Reason         string
	Classification string
	Attempts       int
	MaxAttempts    int
	LogPath        string
	Warning        bool
}

// VerificationDisplay returns current findings followed by retained warnings
// in durable order. Command metadata is joined from the matching command
// result, while the finding's normalized reason and log path remain the
// authoritative values.
func VerificationDisplay(project ProjectState) []VerificationDisplayFinding {
	if project.Verification == nil {
		return nil
	}
	verification := project.Verification
	commands := make(map[string]VerificationCommandResult, len(verification.CurrentResults))
	for _, result := range verification.CurrentResults {
		commands[result.CheckName] = result
	}
	parentCommands := make(map[string]VerificationCommandResult, len(verification.ParentResults))
	for _, result := range verification.ParentResults {
		parentCommands[result.CheckName] = result
	}
	plannedCommands := make(map[string]VerificationStep, len(verification.PlannedSteps))
	for _, step := range verification.PlannedSteps {
		plannedCommands[step.Name] = step
	}
	maxAttempts := MaxVerificationRemediationAttempts
	findings := make([]VerificationDisplayFinding, 0, len(verification.CurrentFindings)+len(verification.Warnings))
	displayedChecks := make(map[string]struct{}, len(verification.CurrentFindings)+len(verification.Warnings))
	appendFinding := func(finding VerificationFinding, warning bool) {
		result := commands[finding.CheckName]
		command := commandLine(result.Command, result.Args)
		if command == "" {
			parent := parentCommands[finding.CheckName]
			command = commandLine(parent.Command, parent.Args)
		}
		if command == "" {
			step := plannedCommands[finding.CheckName]
			command = commandLine(step.Command, step.Args)
		}
		logPath := strings.TrimSpace(finding.LogPath)
		if logPath == "" {
			logPath = strings.TrimSpace(result.LogPath)
		}
		if logPath == "" {
			logPath = strings.TrimSpace(parentCommands[finding.CheckName].LogPath)
		}
		findings = append(findings, VerificationDisplayFinding{
			CheckName:      finding.CheckName,
			Command:        command,
			Identity:       finding.Identity,
			Reason:         finding.Reason,
			Classification: finding.Classification,
			Attempts:       verification.RemediationAttempts,
			MaxAttempts:    maxAttempts,
			LogPath:        logPath,
			Warning:        warning,
		})
		displayedChecks[finding.CheckName] = struct{}{}
	}
	for _, finding := range verification.CurrentFindings {
		appendFinding(finding, false)
	}
	for _, finding := range verification.Warnings {
		appendFinding(finding, true)
	}
	// Strict pauses may not have a stable individual failure to display. Keep
	// their check, command, and concise execution error visible so status still
	// gives the user a concrete repair action.
	for _, result := range verification.CurrentResults {
		if _, displayed := displayedChecks[result.CheckName]; displayed {
			continue
		}
		if !strictVerificationResult(result) {
			continue
		}
		reason := strings.TrimSpace(result.UnavailableErr)
		if reason == "" {
			reason = "verification result has no stable individual failure identity"
		}
		classification := result.Status
		if result.Status != "unavailable" {
			classification = "unclassifiable"
		}
		command := commandLine(result.Command, result.Args)
		if command == "" {
			parent := parentCommands[result.CheckName]
			command = commandLine(parent.Command, parent.Args)
		}
		if command == "" {
			step := plannedCommands[result.CheckName]
			command = commandLine(step.Command, step.Args)
		}
		logPath := strings.TrimSpace(result.LogPath)
		if logPath == "" {
			logPath = strings.TrimSpace(parentCommands[result.CheckName].LogPath)
		}
		findings = append(findings, VerificationDisplayFinding{
			CheckName:      result.CheckName,
			Command:        command,
			Reason:         reason,
			Classification: classification,
			Attempts:       verification.RemediationAttempts,
			MaxAttempts:    maxAttempts,
			LogPath:        logPath,
		})
	}
	return findings
}

func commandLine(command string, args []string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return ""
	}
	return strings.TrimSpace(strings.Join(append([]string{command}, args...), " "))
}

func strictVerificationResult(result VerificationCommandResult) bool {
	switch result.Status {
	case "unavailable", "unclassifiable":
		return true
	case "passed":
		return len(result.Failures) > 0 || hasUnstableFailure(result.Failures)
	case "failed":
		return len(result.Failures) == 0 || hasUnstableFailure(result.Failures)
	default:
		return true
	}
}

func hasUnstableFailure(failures []VerificationFinding) bool {
	for _, failure := range failures {
		if strings.TrimSpace(failure.Identity) == "" || strings.TrimSpace(failure.Reason) == "" {
			return true
		}
	}
	return false
}

// VerificationHasWarnings reports whether a project completed with retained
// baseline or flaky verification warnings.
func VerificationHasWarnings(project ProjectState) bool {
	return project.Verification != nil && len(project.Verification.Warnings) > 0
}

// VerificationIsPaused reports whether verification stopped the pipeline
// because a required result was unavailable or could not be classified.
func VerificationIsPaused(project ProjectState) bool {
	if project.Verification == nil {
		return false
	}
	for _, result := range project.Verification.CurrentResults {
		if strictVerificationResult(result) {
			return true
		}
	}
	for _, finding := range project.Verification.CurrentFindings {
		if finding.Classification == "unavailable" || finding.Classification == "unclassifiable" {
			return true
		}
	}
	return false
}
