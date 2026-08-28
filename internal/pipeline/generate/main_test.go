package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadPhaseID(t *testing.T) {
	tests := []struct {
		name      string
		source    string
		wantID    string
		wantPhase bool
		wantErr   string
	}{
		{
			name:      "phase contract",
			source:    "---\nphase_id: acceptance_criteria\n---\nbody\n",
			wantID:    "acceptance_criteria",
			wantPhase: true,
		},
		{
			name:      "CRLF phase contract",
			source:    "---\r\nphase_id: qa\r\n---\r\nbody\r\n",
			wantID:    "qa",
			wantPhase: true,
		},
		{
			name:      "EOF terminated phase contract",
			source:    "---\nphase_id: qa\n---",
			wantID:    "qa",
			wantPhase: true,
		},
		{
			name:   "non phase markdown",
			source: "# coding patterns\n",
		},
		{
			name:   "frontmatter without phase",
			source: "---\nname: gg-example\n---\nbody\n",
		},
		{
			name:    "unterminated frontmatter",
			source:  "---\nphase_id: qa\n",
			wantErr: "unterminated YAML frontmatter",
		},
		{
			name:      "non string phase",
			source:    "---\nphase_id: 7\n---\n",
			wantPhase: true,
			wantErr:   "phase_id must be a string",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gotID, gotPhase, err := readPhaseID([]byte(test.source))
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("readPhaseID() error = %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("readPhaseID() error = %v, want containing %q", err, test.wantErr)
			}
			if gotID != test.wantID || gotPhase != test.wantPhase {
				t.Fatalf("readPhaseID() = (%q, %v), want (%q, %v)", gotID, gotPhase, test.wantID, test.wantPhase)
			}
		})
	}
}

func TestLoadContractsRejectsDuplicateAndEmptyPhaseIDs(t *testing.T) {
	tests := []struct {
		name    string
		files   map[string]string
		wantErr string
	}{
		{
			name: "duplicate",
			files: map[string]string{
				"gg-first/gg-first.md":   "---\nphase_id: qa\n---\nfirst\n",
				"gg-second/gg-second.md": "---\nphase_id: qa\n---\nsecond\n",
			},
			wantErr: `phase_id "qa" is declared by both`,
		},
		{
			name: "empty",
			files: map[string]string{
				"gg-empty/gg-empty.md": "---\nphase_id: \"\"\n---\n",
			},
			wantErr: "declares an empty phase_id",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := writeCanonicalFixture(t, test.files)
			_, err := loadContracts(root)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("loadContracts() error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}

func TestGenerateUsesSortedPhaseSourcesAndPreservesBytes(t *testing.T) {
	root := t.TempDir()
	first := "---\nphase_id: qa\n---\n# QA\n\nbody\r\n"
	second := "---\nphase_id: acceptance_criteria\n---\n# Acceptance\n\nbody\n"
	writeFile(t, filepath.Join(root, "skills", "canonical", "gg-qa", "gg-qa.md"), first)
	writeFile(t, filepath.Join(root, "skills", "canonical", "gg-acceptance-criteria", "gg-acceptance-criteria.md"), second)
	writeFile(t, filepath.Join(root, "skills", "canonical", "gg-coding-patterns.md"), "# ignored\n")

	destination := filepath.Join(root, "internal", "pipeline", "contract_text.go")
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := generate(root, destination); err != nil {
		t.Fatal(err)
	}
	generated, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	text := string(generated)
	if !strings.Contains(text, `PhaseID("acceptance_criteria")`) || !strings.Contains(text, `PhaseID("qa")`) {
		t.Fatalf("generated output is missing phase entries:\n%s", text)
	}
	if strings.Index(text, `PhaseID("acceptance_criteria")`) > strings.Index(text, `PhaseID("qa")`) {
		t.Fatalf("phase entries are not sorted:\n%s", text)
	}
	if !strings.Contains(text, `"---\nphase_id: qa\n---\n# QA\n\nbody\r\n"`) {
		t.Fatalf("generated output did not preserve source bytes:\n%s", text)
	}
	if strings.Contains(text, "ignored") {
		t.Fatalf("non-phase Markdown was embedded:\n%s", text)
	}
}

func writeCanonicalFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, contents := range files {
		writeFile(t, filepath.Join(root, name), contents)
	}
	return root
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
