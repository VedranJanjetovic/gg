// Package proof parses and classifies reviewer-facing PROOF.md evidence.
package proof

import (
	"fmt"
	"regexp"
	"strings"
)

type Classification string

const (
	ClassificationPass     Classification = "passed"
	ClassificationFail     Classification = "failed"
	ClassificationFeedback Classification = "feedback"
)

// Status is the required per-validation disposition in PROOF.md.
type Status string

const (
	StatusPass     Status = "pass"
	StatusFail     Status = "fail"
	StatusFeedback Status = "feedback"
	StatusDeferred Status = "deferred"
)

type Validation struct {
	Status                Status
	TestLocation          string
	TestName              string
	FlowScenario          string
	WhatItVerifies        string
	ProofItPassed         string
	RemoteOnlyReason      string
	RepositoryEvidence    string
	ManualRunInstructions string
}

// DeferredCheck is the normalized evidence carried to later handoff phases.
// It describes a check that is valid but cannot run without a remote
// credential or endpoint. It never claims that the check passed.
type DeferredCheck struct {
	TestLocation       string `json:"testLocation"`
	CheckName          string `json:"checkName"`
	FlowScenario       string `json:"flowScenario"`
	ExpectedBehavior   string `json:"expectedBehavior"`
	RemoteOnlyReason   string `json:"remoteOnlyReason"`
	RepositoryEvidence string `json:"repositoryEvidence"`
	RunInstructions    string `json:"runInstructions"`
}

func (c DeferredCheck) Validate() error {
	fields := []struct{ name, value string }{
		{"test location", c.TestLocation}, {"check name", c.CheckName},
		{"flow/scenario", c.FlowScenario}, {"expected behavior", c.ExpectedBehavior},
		{"remote-only reason", c.RemoteOnlyReason}, {"repository evidence", c.RepositoryEvidence},
		{"run instructions", c.RunInstructions},
	}
	for _, field := range fields {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("%s is required", field.name)
		}
	}
	return nil
}

type Proof struct {
	Validations []Validation
	Feedback    string
	RunID       string
}

func (p Proof) Validate() error {
	if len(p.Validations) == 0 {
		return fmt.Errorf("proof must contain at least one validation")
	}
	for index, validation := range p.Validations {
		if err := validation.validate(); err != nil {
			return fmt.Errorf("validation %d: %w", index+1, err)
		}
	}
	return nil
}

func (v Validation) validate() error {
	switch v.Status {
	case StatusPass, StatusFail, StatusFeedback, StatusDeferred:
	default:
		return fmt.Errorf("status must be one of %q, %q, %q, or %q", StatusPass, StatusFail, StatusFeedback, StatusDeferred)
	}
	if v.Status == StatusDeferred {
		if strings.TrimSpace(v.ProofItPassed) != "" {
			return fmt.Errorf("deferred validation must not include proof it passed")
		}
		return (DeferredCheck{
			TestLocation: v.TestLocation, CheckName: v.TestName, FlowScenario: v.FlowScenario,
			ExpectedBehavior: v.WhatItVerifies, RemoteOnlyReason: v.RemoteOnlyReason,
			RepositoryEvidence: v.RepositoryEvidence, RunInstructions: v.ManualRunInstructions,
		}).Validate()
	}
	fields := []struct{ name, value string }{
		{"test location", v.TestLocation}, {"test name", v.TestName},
		{"flow/scenario", v.FlowScenario}, {"what it verifies", v.WhatItVerifies},
		{"proof it passed", v.ProofItPassed}, {"manual run instructions", v.ManualRunInstructions},
	}
	for _, field := range fields {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("%s is required", field.name)
		}
	}
	if !hasCommandEvidence(v.ProofItPassed) {
		return fmt.Errorf("proof it passed must include the command run and its result")
	}
	// A pass entry pasting only a failing result is dishonest evidence — but
	// honest narratives may mention past failures ("previously failed, now
	// PASS"), so a failing phrase is rejected only when no passing outcome
	// appears anywhere in the evidence.
	if v.Status == StatusPass && hasFailedCommandResult(v.ProofItPassed) && !hasPassingCommandResult(v.ProofItPassed) {
		return fmt.Errorf("proof it passed contains an explicit failed command result")
	}
	return nil
}

func hasCommandEvidence(value string) bool {
	parts := strings.Split(value, "`")
	for i := 1; i < len(parts); i += 2 {
		if isCommandShaped(parts[i]) && hasCommandResult(value) {
			return true
		}
	}
	for _, line := range strings.Split(value, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "$ ") && isCommandShaped(strings.TrimSpace(strings.TrimPrefix(line, "$ "))) && hasCommandResult(value) {
			return true
		}
	}
	return false
}

// hasCommandResult reports whether the evidence mentions any outcome. The
// keyword set is deliberately broad and only ever widens acceptance: an
// unrecognized phrasing must never fail a phase.
func hasCommandResult(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range []string{"exit", "status", "pass", "fail", "succeeded", "success", "result", "code 0", "ok", "->", "→", "✓", "✗", "error", "output"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func hasFailedCommandResult(value string) bool {
	return failedCommandResultPattern.MatchString(value) || nonZeroResultPattern.MatchString(value)
}

// hasPassingCommandResult reports an explicit successful outcome in the
// evidence text.
func hasPassingCommandResult(value string) bool {
	return passingCommandResultPattern.MatchString(value)
}

var passingCommandResultPattern = regexp.MustCompile(`(?i)\bpass(?:ed|es)?\b|\bok\b|\bsucce(?:ss(?:ful(?:ly)?)?|eded)\b|\bexit(?:\s+(?:code|status))?\s*(?::|=)?\s*0\b|\bcode\s*(?::|=)?\s*0\b|✓`)

var (
	// Result fields are deliberately permissive about punctuation because the
	// evidence is human-authored Markdown, but strict about the failure value.
	failedCommandResultPattern = regexp.MustCompile(`(?i)\b(?:status|result|exit(?:\s+(?:code|status))?|return(?:ed)?(?:\s+(?:code|status))?)\s*(?::|=|is)?\s*(?:fail(?:ed)?|failure|error)\b|\b(?:command|check|test|run)\s+(?:fail(?:ed)?|failure)\b`)
	nonZeroResultPattern       = regexp.MustCompile(`(?i)\bnon[- ]?zero\b|\b(?:exit|return(?:ed)?)\s+(?:(?:code|status)\s*)?(?::|=)?\s*[1-9][0-9]*\b|\b(?:status|result|code)\s*(?::|=)?\s*[1-9][0-9]*\b`)
)

// isCommandShaped decides whether a snippet is a real command rather than
// prose. The rules are fail-open by construction: no incomplete tool list can
// ever reject a genuine command.
//
//   - An explicit "$ " shell prompt marker accepts any command.
//   - A path-prefixed executable ("./run", "/usr/bin/x") accepts.
//   - Any executable-shaped first token with arguments accepts, unless the
//     token is a common English word (a small prose denylist whose
//     incompleteness can only over-accept, never fail a phase).
//   - Bare single-word snippets accept only for known argument-less tools
//     ("make"), keeping lone words like `verified` out.
func isCommandShaped(value string) bool {
	trimmed := strings.TrimSpace(value)
	prompted := strings.HasPrefix(trimmed, "$ ")
	value = strings.TrimSpace(strings.TrimPrefix(trimmed, "$ "))
	fields := strings.Fields(value)
	for len(fields) > 0 {
		switch fields[0] {
		case "sudo", "env", "command":
			fields = fields[1:]
			continue
		}
		if strings.Contains(fields[0], "=") && !strings.HasPrefix(fields[0], "=") {
			fields = fields[1:]
			continue
		}
		break
	}
	if len(fields) == 0 {
		return false
	}
	if prompted {
		return true
	}
	if strings.HasPrefix(fields[0], "./") || strings.HasPrefix(fields[0], "/") {
		return true
	}
	command := fields[0]
	if slash := strings.LastIndex(command, "/"); slash >= 0 {
		command = command[slash+1:]
	}
	if command == "make" {
		return true
	}
	return len(fields) > 1 && executableShaped(command) && !proseWord(command)
}

// executableShaped reports whether a token looks like a program name.
func executableShaped(token string) bool {
	if token == "" {
		return false
	}
	for i, r := range token {
		alnum := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if i == 0 && !alnum {
			return false
		}
		if !alnum && r != '.' && r != '_' && r != '-' && r != '+' {
			return false
		}
	}
	return true
}

// proseWord filters common English sentence openers so backticked prose does
// not count as a command. This denylist fails open: a missing entry can only
// accept extra text, never reject a real command.
func proseWord(token string) bool {
	switch strings.ToLower(token) {
	case "a", "all", "an", "and", "are", "checks", "evidence", "i", "if", "it", "manual", "manually", "no", "not", "or", "passed", "ran", "results", "see", "some", "tests", "that", "the", "then", "these", "this", "verified", "was", "we", "were", "yes":
		return true
	default:
		return false
	}
}

func (p Proof) Classify() Classification {
	if err := p.Validate(); err != nil {
		return ClassificationFail
	}
	for _, validation := range p.Validations {
		if validation.Status == StatusFail {
			return ClassificationFail
		}
	}
	if strings.TrimSpace(p.Feedback) != "" {
		return ClassificationFeedback
	}
	for _, validation := range p.Validations {
		if validation.Status == StatusFeedback {
			return ClassificationFeedback
		}
	}
	return ClassificationPass
}

// DeferredChecks returns a copy of every validated remote-only check in
// stable PROOF.md order.
func (p Proof) DeferredChecks() []DeferredCheck {
	checks := make([]DeferredCheck, 0)
	for _, validation := range p.Validations {
		if validation.Status != StatusDeferred {
			continue
		}
		checks = append(checks, DeferredCheck{
			TestLocation: validation.TestLocation, CheckName: validation.TestName,
			FlowScenario: validation.FlowScenario, ExpectedBehavior: validation.WhatItVerifies,
			RemoteOnlyReason: validation.RemoteOnlyReason, RepositoryEvidence: validation.RepositoryEvidence,
			RunInstructions: validation.ManualRunInstructions,
		})
	}
	return checks
}
