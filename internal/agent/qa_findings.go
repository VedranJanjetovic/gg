package agent

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/VedranJanjetovic/gg/internal/pipeline"
)

// QAFinding is one structured issue reported by a QA run in the qa-report
// frontmatter. ID is the stable identity compared across attempts; New is the
// agent's own judgment of whether the finding was absent from the previous
// attempt's report.
type QAFinding struct {
	ID      string `json:"id"`
	Summary string `json:"summary,omitempty"`
	New     bool   `json:"new"`
}

// readQAFindings extracts the optional single-line JSON `gg_qa_findings`
// array from the QA report artifact's frontmatter. The key is optional and
// the reader degrades gracefully: a missing artifact, absent key, or a value
// that does not parse all yield nil findings, so a QA payload that predates
// the findings contract falls back to the ceiling-bounded loop instead of
// failing the run.
func readQAFindings(root string) []QAFinding {
	name, ok := pipeline.CanonicalArtifactName(pipeline.PhaseQA)
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
		if !cut || strings.TrimSpace(key) != "gg_qa_findings" {
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
	var findings []QAFinding
	if err := decoder.Decode(&findings); err != nil {
		return nil
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil
	}
	valid := make([]QAFinding, 0, len(findings))
	for _, finding := range findings {
		finding.ID = strings.TrimSpace(finding.ID)
		if finding.ID == "" {
			continue
		}
		valid = append(valid, finding)
	}
	if len(valid) == 0 {
		return nil
	}
	return valid
}
