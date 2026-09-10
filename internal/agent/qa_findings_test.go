package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func writeQAReport(t *testing.T, root, frontmatter string) {
	t.Helper()
	dir := filepath.Join(root, ".gg")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\n" + frontmatter + "---\n\n# QA report\n"
	if err := os.WriteFile(filepath.Join(dir, "qa-report.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadQAFindingsParsesTheStructuredContract(t *testing.T) {
	root := t.TempDir()
	writeQAReport(t, root, "gg_run_id: \"run-1\"\ngg_qa_findings: [{\"id\": \"refund-flow-500\", \"summary\": \"refund returns 500\", \"new\": true}, {\"id\": \"stale-cache\", \"new\": false}]\n")
	findings := readQAFindings(root)
	if len(findings) != 2 {
		t.Fatalf("findings = %#v", findings)
	}
	if findings[0].ID != "refund-flow-500" || findings[0].Summary != "refund returns 500" || !findings[0].New {
		t.Fatalf("first finding = %#v", findings[0])
	}
	if findings[1].ID != "stale-cache" || findings[1].New {
		t.Fatalf("second finding = %#v", findings[1])
	}
}

func TestReadQAFindingsDegradesGracefullyOnLegacyOrMalformedReports(t *testing.T) {
	for _, test := range []struct {
		name        string
		frontmatter string
	}{
		{name: "key absent (legacy payload)", frontmatter: "gg_run_id: \"run-1\"\n"},
		{name: "empty value", frontmatter: "gg_qa_findings:\n"},
		{name: "malformed JSON", frontmatter: "gg_qa_findings: [{\"id\": broken\n"},
		{name: "trailing JSON", frontmatter: "gg_qa_findings: [] []\n"},
		{name: "entries without ids", frontmatter: "gg_qa_findings: [{\"summary\": \"no id\", \"new\": true}]\n"},
		{name: "repeated key", frontmatter: "gg_qa_findings: [{\"id\": \"a\"}]\ngg_qa_findings: [{\"id\": \"b\"}]\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeQAReport(t, root, test.frontmatter)
			if findings := readQAFindings(root); findings != nil {
				t.Fatalf("findings = %#v, want nil fallback", findings)
			}
		})
	}
	if findings := readQAFindings(t.TempDir()); findings != nil {
		t.Fatalf("missing report produced findings %#v", findings)
	}
}
