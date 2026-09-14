package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/VedranJanjetovic/gg/internal/pipeline"
)

// FindingClosure is one Development fix pass's claim that an open QA finding
// is closed, declared in the development artifact's `gg_finding_closures`
// frontmatter. ID reuses the open finding's identity; Evidence names the
// command or test that now exercises it and the observed result; Covered
// enumerates what the fix covers, which is the only way a
// coverage-completeness finding can be shown closed.
type FindingClosure struct {
	ID       string   `json:"id"`
	Evidence string   `json:"evidence,omitempty"`
	Covered  []string `json:"covered,omitempty"`
}

// readFindingClosures extracts the single-line JSON `gg_finding_closures`
// array from the development artifact's frontmatter. The reader itself
// degrades to nil closures for a missing artifact, absent key, or unparseable
// value; unlike the QA findings key that is not tolerance, because
// findingClosureViolations turns nil closures into the contract breach that
// fails the fix pass.
func readFindingClosures(root string) []FindingClosure {
	name, ok := pipeline.CanonicalArtifactName(pipeline.PhaseDevelopment)
	if !ok {
		return nil
	}
	root, err := cleanExistingDirectory(root)
	if err != nil {
		return nil
	}
	path := filepath.Join(root, name)
	if !isRegularArtifact(root, path) {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if len(lines) < 2 || lines[0] != "---" {
		return nil
	}
	value, found := "", false
	for _, line := range lines[1:] {
		if line == "---" {
			break
		}
		key, rest, cut := strings.Cut(line, ":")
		if !cut || strings.TrimSpace(key) != "gg_finding_closures" {
			continue
		}
		if found {
			return nil
		}
		found, value = true, strings.TrimSpace(rest)
	}
	if !found || value == "" {
		return nil
	}
	decoder := json.NewDecoder(strings.NewReader(value))
	var closures []FindingClosure
	if err := decoder.Decode(&closures); err != nil {
		return nil
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil
	}
	valid := make([]FindingClosure, 0, len(closures))
	for _, closure := range closures {
		closure.ID = strings.TrimSpace(closure.ID)
		if closure.ID == "" {
			continue
		}
		valid = append(valid, closure)
	}
	if len(valid) == 0 {
		return nil
	}
	return valid
}

// findingClosureViolations reports the closure-contract breaches in a fix
// pass: an open finding with no closure entry, or an entry whose evidence is
// blank. The result is empty when the contract holds and is the
// SemanticFailureError detail otherwise.
func findingClosureViolations(open []string, closures []FindingClosure) string {
	declared := make(map[string]FindingClosure, len(closures))
	for _, closure := range closures {
		id := strings.TrimSpace(closure.ID)
		if id == "" {
			continue
		}
		declared[id] = closure
	}
	var missing, unevidenced []string
	for _, id := range open {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		closure, ok := declared[id]
		if !ok {
			missing = append(missing, id)
			continue
		}
		if strings.TrimSpace(closure.Evidence) == "" {
			unevidenced = append(unevidenced, id)
		}
	}
	var violations []string
	if len(missing) > 0 {
		violations = append(violations, fmt.Sprintf("gg_finding_closures does not close open QA finding(s) %s", quotedList(missing)))
	}
	if len(unevidenced) > 0 {
		violations = append(violations, fmt.Sprintf("gg_finding_closures declares no evidence for open QA finding(s) %s", quotedList(unevidenced)))
	}
	return strings.Join(violations, "; ")
}
