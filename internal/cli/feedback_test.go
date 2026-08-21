package cli

import (
	"strings"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/state"
)

func TestQASessionPromptCarriesFullProjectContextAndForbidsChanges(t *testing.T) {
	project := interviewProject()
	project.AcceptanceCriteria = append(project.AcceptanceCriteria, "Clarification — Q: DB? A: PostgreSQL")
	project.Plan = &state.PlanState{Phases: []string{"P1", "P2"}, Completed: []string{"P1"}}
	prompt := qaSessionPrompt(&project)
	for _, want := range []string{
		"supermario", "PostgreSQL", "P2", "completed: P1",
		".gg directory", "Do NOT change any file",
		"exit and use the gg feedback loop",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("QA session prompt missing %q:\n%s", want, prompt)
		}
	}
}
