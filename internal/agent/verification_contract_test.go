package agent

import (
	"strings"
	"testing"
)

const validVerificationFrontmatter = `---
gg_run_id: "planning-run"
gg_disposition: passed
gg_verification_steps: [{"name":"tests","command":"go","args":["test","./..."],"env":{"GOTOOLCHAIN":"go1.22.12"},"adapter":"go-test"}]
gg_repair_mode: false
---
# Plan
`

func TestParseVerificationContractRequiresStrictExecutableDeclaration(t *testing.T) {
	contract, err := ParseVerificationContract([]byte(validVerificationFrontmatter))
	if err != nil {
		t.Fatal(err)
	}
	if len(contract.Steps) != 1 || contract.Steps[0].Name != "tests" || contract.Steps[0].Env["GOTOOLCHAIN"] != "go1.22.12" {
		t.Fatalf("contract = %#v", contract)
	}
	if contract.RepairMode {
		t.Fatalf("contract policy = %#v", contract)
	}
}

func TestParseVerificationContractRejectsMissingMalformedAndDuplicateEntries(t *testing.T) {
	duplicate := `---
gg_verification_steps: [{"name":"tests","command":"go","args":["test"],"adapter":"go-test"},{"name":"tests","command":"go","args":[],"adapter":"go-test"}]
gg_repair_mode: false
---
`
	tests := []struct {
		name string
		data string
		want string
	}{
		{name: "missing steps", data: "---\ngg_repair_mode: false\n---\n", want: "gg_verification_steps"},
		{name: "missing repair mode", data: "---\ngg_verification_steps: []\n---\n", want: "gg_repair_mode"},
		{name: "duplicate names", data: duplicate, want: "duplicated"},
		{name: "shell args", data: strings.Replace(validVerificationFrontmatter, "[\"test\",\"./...\"]", "\"test ./...\"", 1), want: "JSON array"},
		{name: "unsupported adapter", data: strings.Replace(validVerificationFrontmatter, "go-test", "shell", 1), want: "unsupported adapter"},
		{name: "implicit repair", data: strings.Replace(validVerificationFrontmatter, "gg_repair_mode: false", "gg_repair_mode: null", 1), want: "explicit boolean"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseVerificationContract([]byte(test.data))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ParseVerificationContract() error = %v, want %q", err, test.want)
			}
		})
	}
}
