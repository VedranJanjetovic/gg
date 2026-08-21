package agent

import (
	"testing"

	"github.com/VedranJanjetovic/gg/internal/config"
)

func TestParseTokenUsageFromAgentOutput(t *testing.T) {
	codexStderr := "thinking...\ntokens used\n41,228\n"
	if got := parseTokenUsage(config.AgentCodex, "", codexStderr); got != 41228 {
		t.Fatalf("codex tokens = %d, want 41228", got)
	}
	if got := parseTokenUsage(config.AgentCodex, "", "no usage marker\n"); got != 0 {
		t.Fatalf("codex without marker = %d, want 0", got)
	}
	// Marker and count on the same line also parses.
	if got := parseTokenUsage(config.AgentCodex, "", "tokens used: 1,024\n"); got != 1024 {
		t.Fatalf("codex inline count = %d, want 1024", got)
	}

	claudeStdout := `{"type":"result","result":"done","usage":{"input_tokens":100,"cache_creation_input_tokens":20,"cache_read_input_tokens":30,"output_tokens":50}}`
	if got := parseTokenUsage(config.AgentClaude, claudeStdout, ""); got != 200 {
		t.Fatalf("claude tokens = %d, want 200", got)
	}
	if got := parseTokenUsage(config.AgentClaude, "plain text output", ""); got != 0 {
		t.Fatalf("claude plain output = %d, want 0", got)
	}
}

func TestParseClaudeErrorResultExtractsFailureMessageOnly(t *testing.T) {
	cases := []struct {
		name   string
		stdout string
		want   string
	}{
		{"api error envelope", `{"type":"result","is_error":true,"result":"There's an issue with the selected model (global-model)."}`, "There's an issue with the selected model (global-model)."},
		{"successful envelope", `{"type":"result","is_error":false,"result":"done"}`, ""},
		{"non-json output", "tokens used\n1234", ""},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseClaudeErrorResult(tc.stdout); got != tc.want {
				t.Fatalf("parseClaudeErrorResult = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseClaudeCostExtractsReportedUSD(t *testing.T) {
	cases := []struct {
		name   string
		stdout string
		want   float64
	}{
		{"reported cost", `{"type":"result","total_cost_usd":0.4275,"usage":{"output_tokens":10}}`, 0.4275},
		{"zero cost", `{"type":"result","total_cost_usd":0}`, 0},
		{"missing field", `{"type":"result"}`, 0},
		{"not json", "tokens used\n1234", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseClaudeCost(tc.stdout); got != tc.want {
				t.Fatalf("parseClaudeCost = %v, want %v", got, tc.want)
			}
		})
	}
}
