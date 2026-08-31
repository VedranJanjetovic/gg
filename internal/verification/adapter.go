package verification

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var (
	goTestFailurePattern = regexp.MustCompile(`^--- FAIL: (.+?)(?: \([^)]+\))?$`)
	goPackagePattern     = regexp.MustCompile(`^(?:FAIL|ok)\s+([^ \t]+)`)
	goPackageHeader      = regexp.MustCompile(`^#\s+([^ \t]+)`)
	// diagnosticPattern matches file:line[:col]: message, the shape shared by
	// Go tool diagnostics and `git diff --check`.
	diagnosticPattern = regexp.MustCompile(`^(.+):([0-9]+)(?::([0-9]+))?:\s*(.+)$`)
)

// ParseOutput turns bounded command output into stable failures. A non-zero
// command with no parser-supported identity is intentionally unclassifiable.
func ParseOutput(adapter Adapter, stdout, stderr string, exitCode int) ([]IndividualFailure, bool, error) {
	combined := strings.TrimSpace(strings.ReplaceAll(stdout+"\n"+stderr, "\r\n", "\n"))
	var (
		failures []IndividualFailure
		err      error
	)
	switch adapter.Canonical() {
	case AdapterFileList:
		failures = parseFileList(combined, fileListReason(adapter))
	case AdapterGoTest:
		failures = parseGoTest(combined)
	case AdapterDiagnostic:
		failures = parseDiagnostic(combined)
	case AdapterGitDiffCheck:
		failures = parseGitDiff(combined)
	case AdapterCommandExit:
		failures = parseCommandExit(combined, exitCode)
	default:
		err = fmt.Errorf("unsupported verification adapter %q", adapter)
	}
	if err != nil {
		return nil, false, err
	}
	// A successful command is classifiable even when it emits no output. A
	// failing command is classifiable only when the adapter exposed at least
	// one stable individual failure.
	return failures, exitCode == 0 || len(failures) > 0, nil
}

// fileListReason keeps the legacy gofmt-empty wording so a baseline captured
// before the adapter was generalized still compares equal on resume; a reason
// change would otherwise reclassify every unchanged file as changed_reason.
func fileListReason(adapter Adapter) string {
	if adapter == AdapterGofmtEmpty {
		return "file requires gofmt"
	}
	return "file requires formatting"
}

// parseFileList treats each printed path as one failing file. reason is supplied
// by the caller rather than fixed here so the legacy alias keeps its wording.
func parseFileList(output, reason string) []IndividualFailure {
	var failures []IndividualFailure
	for _, line := range strings.Split(output, "\n") {
		identity := strings.TrimSpace(line)
		if identity == "" {
			continue
		}
		failures = append(failures, IndividualFailure{Identity: "format:" + identity, Reason: reason, Evidence: identity})
	}
	return failures
}

// parseCommandExit is the toolchain-agnostic fallback. The command exposes no
// per-failure identity, so the whole check collapses to one failure; FailureKey
// pairs the fixed identity with the step name, keeping it unique per check.
// Reason is deliberately constant: folding the command's volatile output into it
// would make every run differ from its baseline and block the boundary forever.
// The cost is real and intended — this adapter cannot distinguish one failure
// from another within a check, so declare a parseable adapter where one fits.
func parseCommandExit(output string, exitCode int) []IndividualFailure {
	if exitCode == 0 {
		return nil
	}
	return []IndividualFailure{{Identity: "command", Reason: "command reported failure", Evidence: output}}
}

func parseGoTest(output string) []IndividualFailure {
	packageName := ""
	seen := make(map[string]bool)
	var failures []IndividualFailure
	type pendingFailure struct {
		identity string
		header   string
		details  []string
	}
	var pending []*pendingFailure
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if match := goPackageHeader.FindStringSubmatch(trimmed); len(match) == 2 {
			packageName = match[1]
			continue
		}
		if match := goPackagePattern.FindStringSubmatch(trimmed); len(match) == 2 {
			packageName = match[1]
			if strings.HasPrefix(trimmed, "FAIL ") {
				for _, item := range pending {
					if !strings.Contains(item.identity, ":") {
						item.identity = packageName + ":" + item.identity
					}
				}
				packageName = ""
			}
		}
		if match := goTestFailurePattern.FindStringSubmatch(trimmed); len(match) == 2 {
			identity := match[1]
			if packageName != "" {
				identity = packageName + ":" + identity
			}
			pending = append(pending, &pendingFailure{identity: identity, header: trimmed})
			continue
		}
		if len(pending) > 0 && trimmed != "" && trimmed != "FAIL" && !strings.HasPrefix(trimmed, "FAIL ") && !strings.HasPrefix(trimmed, "ok ") {
			pending[len(pending)-1].details = append(pending[len(pending)-1].details, trimmed)
		}
	}
	for _, item := range pending {
		if seen[item.identity] {
			continue
		}
		seen[item.identity] = true
		reason := item.header
		if len(item.details) > 0 {
			reason = strings.Join(item.details, " ")
		}
		failures = append(failures, IndividualFailure{Identity: item.identity, Reason: NormalizeReason(reason), Evidence: strings.Join(append([]string{item.header}, item.details...), "\n")})
	}
	if len(failures) == 0 {
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if match := goPackagePattern.FindStringSubmatch(line); len(match) == 2 && strings.HasPrefix(line, "FAIL ") {
				identity := "package:" + match[1]
				failures = append(failures, IndividualFailure{Identity: identity, Reason: NormalizeReason(line), Evidence: line})
			}
		}
	}
	return failures
}

func parseDiagnostic(output string) []IndividualFailure {
	var failures []IndividualFailure
	seen := make(map[string]bool)
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		match := diagnosticPattern.FindStringSubmatch(line)
		if len(match) == 0 {
			continue
		}
		identity := match[1] + ":" + match[2]
		if match[3] != "" {
			identity += ":" + match[3]
		}
		if !seen[identity] {
			seen[identity] = true
			failures = append(failures, IndividualFailure{Identity: identity, Reason: NormalizeReason(match[4]), Evidence: line})
		}
	}
	return failures
}

func parseGitDiff(output string) []IndividualFailure {
	var failures []IndividualFailure
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		match := diagnosticPattern.FindStringSubmatch(line)
		if len(match) != 5 {
			continue
		}
		identity := "diff:" + match[1] + ":" + match[2]
		if match[3] != "" {
			identity += ":" + match[3]
		}
		failures = append(failures, IndividualFailure{Identity: identity, Reason: NormalizeReason(match[4]), Evidence: line})
	}
	return failures
}

func sortFailures(failures []IndividualFailure) []IndividualFailure {
	sort.SliceStable(failures, func(i, j int) bool { return failures[i].Identity < failures[j].Identity })
	return failures
}
