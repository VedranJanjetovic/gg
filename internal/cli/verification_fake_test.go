package cli

import (
	"context"

	"github.com/VedranJanjetovic/gg/internal/verification"
)

// passingVerification reports every planned step as passed. The boundary gate
// requires the report to name exactly the planned steps, so this echoes the
// requested steps back rather than returning a fixed result.
type passingVerification struct{}

func (passingVerification) Verify(_ context.Context, _ string, steps []verification.Step) (verification.Report, error) {
	results := make([]verification.CommandResult, 0, len(steps))
	for _, step := range steps {
		results = append(results, verification.CommandResult{
			StepName: step.Name, Command: step.Command,
			Args: append([]string(nil), step.Args...), Status: verification.CommandPassed,
		})
	}
	return verification.Report{Results: results}, nil
}
