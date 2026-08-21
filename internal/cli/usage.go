package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/VedranJanjetovic/gg/internal/state"
)

// usageCommand prints agent usage per project: the summed agent-reported
// token count and USD cost across every recorded phase execution, including
// finished projects. Costs are agent-reported only (claude's total_cost_usd);
// agents that report no cost contribute tokens alone, so the cost column is
// a lower bound for mixed-agent projects.
func (a *App) usageCommand(ctx context.Context, stdout io.Writer, args []string) error {
	if err := rejectArgs("usage", args); err != nil {
		return err
	}
	if err := a.requireConfiguredProject(); err != nil {
		return err
	}
	projects, err := a.Projects(ctx)
	if err != nil {
		return fmt.Errorf("list projects: %w", err)
	}
	if len(projects) == 0 {
		_, err := fmt.Fprintln(stdout, "gg usage: no projects")
		return err
	}
	writer := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "NAME\tSTATUS\tTOKENS\tCOST\t"); err != nil {
		return err
	}
	var totalTokens int64
	var totalCost float64
	for _, project := range projects {
		tokens, cost := projectUsage(project)
		totalTokens += tokens
		totalCost += cost
		if _, err := fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t\n", project.Name, project.Status, usageTokens(tokens), usageCost(cost)); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(writer, "TOTAL\t\t%s\t%s\t\n", usageTokens(totalTokens), usageCost(totalCost)); err != nil {
		return err
	}
	return writer.Flush()
}

// projectUsage sums the agent-reported tokens and USD cost across all
// recorded phase executions of one project.
func projectUsage(project state.ProjectState) (tokens int64, cost float64) {
	for _, record := range project.PhaseHistory {
		if record.Outcome == nil {
			continue
		}
		tokens += record.Outcome.TokensUsed
		cost += record.Outcome.CostUSD
	}
	return tokens, cost
}

// usageTokens renders a token count with thousands separators; zero shows as
// a dash so unused projects read as such.
func usageTokens(value int64) string {
	if value == 0 {
		return "-"
	}
	digits := fmt.Sprintf("%d", value)
	var b strings.Builder
	for i, r := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// usageCost renders an agent-reported dollar amount; sub-cent totals stay
// visible and zero (not reported) shows as a dash.
func usageCost(value float64) string {
	if value == 0 {
		return "-"
	}
	if value < 0.01 {
		return fmt.Sprintf("$%.4f", value)
	}
	return fmt.Sprintf("$%.2f", value)
}
