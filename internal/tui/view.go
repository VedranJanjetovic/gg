package tui

import (
	"fmt"
	"strings"

	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/state"
)

// View renders the interactive project progress screen.
func (m Model) View() string { return m.render(true) }

func (m Model) statusView() string { return m.render(false) }

func (m Model) render(interactive bool) string {
	width := m.width
	if width <= 0 {
		width = 80
	}
	var output strings.Builder
	skipAvailable, _ := m.skipTarget()
	fmt.Fprintf(&output, "%s\n", m.styles.title.Render("gg · "+m.project.Name))
	fmt.Fprintf(&output, "Status: %s\n\n", projectStatus(m.project.Status))
	for _, line := range verificationLines(m.project, width) {
		fmt.Fprintf(&output, "  %s\n", line)
	}
	if line := m.interviewLine(); line != "" {
		fmt.Fprintf(&output, "  %s\n", line)
	}
	for _, phase := range m.phases {
		display := phase
		if phase.ID == string(pipeline.PhaseDevelopment) {
			display.Name = m.developmentName(phase.Name)
		}
		fmt.Fprintf(&output, "  %s\n", m.phaseLine(display, interactive))
		for _, line := range m.failureLines(phase, phase.ID, "", width) {
			fmt.Fprintf(&output, "      %s\n", line)
		}
		for _, subphase := range phase.Subphases {
			fmt.Fprintf(&output, "      %s\n", m.phaseLine(subphase, interactive))
			for _, line := range m.failureLines(subphase, phase.ID, subphase.ID, width) {
				fmt.Fprintf(&output, "          %s\n", line)
			}
		}
	}

	completed := completedPhases(m.phases)
	total := len(m.phases)
	remaining := total - completed
	percent := 0.0
	if total > 0 {
		percent = float64(completed) / float64(total)
	}
	fmt.Fprintf(&output, "\nProgress  %s  %d/%d · %s\n", m.progress.ViewAs(percent), completed, total, remainingLabel(remaining))
	if totalTokens := m.totalTokens(); totalTokens > 0 {
		usage := "Tokens: " + formatTokens(totalTokens)
		if totalCost := m.totalCost(); totalCost > 0 {
			usage += "  ·  " + formatUSD(totalCost)
		}
		fmt.Fprintf(&output, "%s\n", m.styles.muted.Render(usage+"  (d details)"))
		if m.showTokenDetail {
			for _, line := range m.tokenDetailLines() {
				fmt.Fprintf(&output, "  %s\n", m.styles.muted.Render(line))
			}
		}
	}
	if m.interviewOpen() {
		fmt.Fprintf(&output, "\n%s\n", m.styles.running.Render(wrapToWidth("Waiting for grooming answers — press g to answer the questions", width-2)))
	}
	if m.lastErr != nil {
		fmt.Fprintf(&output, "\n%s\n", m.styles.errorText.Render(wrapToWidth("Error: "+m.lastErr.Error(), width-2)))
	}
	if m.notice != "" {
		fmt.Fprintf(&output, "\n%s\n", m.styles.muted.Render(wrapToWidth(m.notice, width-2)))
	}
	if interactive {
		output.WriteString("\n")
		if m.actionPending() {
			if m.stopPending {
				output.WriteString("Stopping pipeline…\n")
				return output.String()
			}
			if m.skipPending {
				output.WriteString("Skipping failed execution…\n")
				return output.String()
			}
			if m.codePending {
				output.WriteString("Opening code editor…\n")
				return output.String()
			}
			if m.terminalPending {
				output.WriteString("Opening terminal…\n")
				return output.String()
			}
			switch m.project.Status {
			case state.StatusRunning:
				fmt.Fprintf(&output, "%s stop\n", m.styles.key.Render("s"))
				output.WriteString("Keys: c code  t terminal  s stop  q detach\n")
			case state.StatusStopped:
				if m.resumePending {
					output.WriteString("Continuing pipeline…\n")
				} else {
					output.WriteString("Stopping pipeline…\n")
				}
			default:
				if m.resumePending {
					output.WriteString("Continuing pipeline…\n")
				} else {
					output.WriteString("Starting pipeline…\n")
				}
			}
			return output.String()
		}
		if m.skipConfirm {
			output.WriteString("Confirm skip of " + m.skipLabel + "?  y/Enter confirm  n/Esc cancel\n")
		} else {
			switch m.project.Status {
			case state.StatusRunning:
				fmt.Fprintf(&output, "%s stop  %s quit\n", m.styles.key.Render("s"), m.styles.key.Render("q"))
			case state.StatusStopped:
				output.WriteString("Type r to continue pipeline\n")
				if m.actions.Configure != nil {
					fmt.Fprintf(&output, "%s configure  %s quit\n", m.styles.key.Render("e"), m.styles.key.Render("q"))
				} else {
					fmt.Fprintf(&output, "%s quit\n", m.styles.key.Render("q"))
				}
			case state.StatusFailed:
				configure := ""
				if m.actions.Configure != nil {
					configure = fmt.Sprintf("  %s configure", m.styles.key.Render("e"))
				}
				if m.actions.Skip != nil && skipAvailable {
					fmt.Fprintf(&output, "%s skip  %s resume%s  %s quit\n", m.styles.key.Render("s"), m.styles.key.Render("r"), configure, m.styles.key.Render("q"))
				} else {
					fmt.Fprintf(&output, "%s resume%s  %s quit\n", m.styles.key.Render("r"), configure, m.styles.key.Render("q"))
				}
			default:
				fmt.Fprintf(&output, "%s quit\n", m.styles.key.Render("q"))
			}
			if m.interviewOpen() {
				keys := "Keys: g answer questions  c code  t terminal  r resume"
				if m.project.Status == state.StatusRunning {
					keys += "  s stop"
				} else if m.project.Status == state.StatusFailed && skipAvailable {
					keys += "  s skip"
				}
				if (m.project.Status == state.StatusFailed || m.project.Status == state.StatusStopped) && m.actions.Configure != nil {
					keys += "  e configure"
				}
				output.WriteString(keys + "  q quit\n")
			} else {
				keys := "Keys: i interactive  c code  t terminal  r resume"
				if m.project.Status == state.StatusRunning {
					keys += "  s stop"
				} else if m.project.Status == state.StatusFailed && m.actions.Skip != nil && skipAvailable {
					keys += "  s skip"
				}
				output.WriteString(keys + "  q quit\n")
			}
		}
	}
	return output.String()
}

func verificationLines(project state.ProjectState, width int) []string {
	findings := state.VerificationDisplay(project)
	if len(findings) == 0 && (project.Verification == nil || strings.TrimSpace(project.Verification.NextAction) == "") {
		return nil
	}
	lines := []string{"Verification:"}
	for _, finding := range findings {
		label := "finding"
		if finding.Warning {
			label = "warning"
		}
		part := fmt.Sprintf("%s: check=%s command=%s identity=%s reason=%s classification=%s attempts=%d/%d log=%s", label, displayVerificationValue(finding.CheckName), displayVerificationValue(finding.Command), displayVerificationValue(finding.Identity), displayVerificationValue(finding.Reason), displayVerificationValue(finding.Classification), finding.Attempts, finding.MaxAttempts, displayVerificationValue(finding.LogPath))
		for _, wrapped := range strings.Split(wrapToWidth(part, width-4), "\n") {
			lines = append(lines, wrapped)
		}
	}
	if project.Verification != nil && strings.TrimSpace(project.Verification.NextAction) != "" {
		lines = append(lines, "Next action: "+project.Verification.NextAction)
	}
	return lines
}

func displayVerificationValue(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

// totalTokens sums the agent-reported token usage across all recorded phase
// executions.
func (m Model) totalTokens() int64 {
	var total int64
	for _, record := range m.project.PhaseHistory {
		if record.Outcome != nil {
			total += record.Outcome.TokensUsed
		}
	}
	return total
}

// totalCost sums the agent-reported USD cost across all recorded phase
// executions; agents that do not report cost contribute zero.
func (m Model) totalCost() float64 {
	var total float64
	for _, record := range m.project.PhaseHistory {
		if record.Outcome != nil {
			total += record.Outcome.CostUSD
		}
	}
	return total
}

// tokenDetailLines aggregates token usage (and reported USD cost) per phase
// and subphase in first-appearance order, counting repeated runs.
func (m Model) tokenDetailLines() []string {
	type usage struct {
		name   string
		tokens int64
		cost   float64
		runs   int
	}
	var order []string
	byName := make(map[string]*usage)
	for _, record := range m.project.PhaseHistory {
		if record.Outcome == nil || record.Outcome.TokensUsed == 0 {
			continue
		}
		name := record.Phase
		if record.Subphase != "" {
			name += "/" + record.Subphase
		}
		entry, ok := byName[name]
		if !ok {
			entry = &usage{name: name}
			byName[name] = entry
			order = append(order, name)
		}
		entry.tokens += record.Outcome.TokensUsed
		entry.cost += record.Outcome.CostUSD
		entry.runs++
	}
	lines := make([]string, 0, len(order))
	for _, name := range order {
		entry := byName[name]
		line := fmt.Sprintf("%-32s %12s", entry.name, formatTokens(entry.tokens))
		if entry.cost > 0 {
			line += fmt.Sprintf("  %8s", formatUSD(entry.cost))
		}
		if entry.runs > 1 {
			line += fmt.Sprintf("  (%d runs)", entry.runs)
		}
		lines = append(lines, line)
	}
	return lines
}

// formatUSD renders an agent-reported dollar amount; sub-cent totals stay
// visible instead of rounding to $0.00.
func formatUSD(value float64) string {
	if value < 0.01 {
		return fmt.Sprintf("$%.4f", value)
	}
	return fmt.Sprintf("$%.2f", value)
}

// formatTokens renders a token count with thousands separators.
func formatTokens(value int64) string {
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

// failureLines renders the persisted reason of a failed phase's most recent
// execution, wrapped to the screen, so the screen answers "why did it fail"
// without digging through log files.
func (m Model) failureLines(view PhaseView, phaseID, subphaseID string, width int) []string {
	if view.Status != PhaseFailed && view.Status != PhaseSkipped {
		return nil
	}
	reason := ""
	for i := len(m.project.PhaseHistory) - 1; i >= 0; i-- {
		record := m.project.PhaseHistory[i]
		if record.Phase != phaseID || record.Subphase != subphaseID {
			continue
		}
		if record.Status == state.StatusFailed && record.Outcome != nil {
			reason = record.Outcome.Error
		}
		break
	}
	if reason == "" {
		return nil
	}
	var lines []string
	for _, part := range strings.Split(reason, "\n") {
		if part = strings.TrimSpace(part); part == "" {
			continue
		}
		for _, wrapped := range strings.Split(wrapToWidth(part, width-12), "\n") {
			lines = append(lines, m.styles.failed.Render(wrapped))
		}
	}
	return lines
}

// developmentName annotates the Development phase with the plan progress
// mirrored from the planning artifact — "Development (phase 2/4 — Gameplay)"
// names the plan phase currently being implemented out of the plan's total.
func (m Model) developmentName(name string) string {
	plan := m.project.Plan
	if plan == nil || len(plan.Phases) == 0 {
		return name
	}
	done := make(map[string]bool, len(plan.Completed))
	for _, completed := range plan.Completed {
		done[completed] = true
	}
	current, completed := "", 0
	for _, phaseName := range plan.Phases {
		if done[phaseName] {
			completed++
		} else if current == "" {
			current = phaseName
		}
	}
	if current == "" {
		return fmt.Sprintf("%s (phase %d/%d)", name, completed, len(plan.Phases))
	}
	return fmt.Sprintf("%s (phase %d/%d — %s)", name, completed+1, len(plan.Phases), current)
}

// interviewLine renders the pre-pipeline grooming interview as its own step
// so the questions the user answered are visibly distinct from the pipeline's
// agent-run Grooming phase below.
func (m Model) interviewLine() string {
	if m.project.Interview == nil {
		return ""
	}
	if m.project.Interview.Done {
		return m.styles.success.Render("✓ Grooming interview (questions answered)")
	}
	if m.interviewOpen() {
		return m.styles.running.Render("● Grooming interview — waiting for your answers (g)")
	}
	// Undone but not enterable right now (pipeline running/finished).
	return m.styles.muted.Render("○ Grooming interview (not completed)")
}

func (m Model) phaseLine(phase PhaseView, interactive bool) string {
	name := phase.Name
	if phase.Warning != "" {
		name += " (" + phase.Warning + ")"
	}
	if phase.SkipCount > 0 {
		suffix := " skipped execution"
		if phase.SkipCount != 1 {
			suffix += "s"
		}
		name += fmt.Sprintf(" (%d%s)", phase.SkipCount, suffix)
	}
	var marker string
	switch phase.Status {
	case PhaseSucceeded:
		return m.styles.success.Render("✓ " + name)
	case PhaseRunning:
		if interactive {
			marker = m.spinner.View()
		} else {
			marker = "▶"
		}
		return m.styles.running.Render(marker + " " + name)
	case PhaseFailed:
		return m.styles.failed.Render("✗ " + name + " (failed)")
	case PhaseSkipped:
		return m.styles.muted.Render("• " + name + " (skipped)")
	case PhaseStopped:
		return m.styles.stopped.Render("■ " + name + " (stopped)")
	default:
		return m.styles.muted.Render("○ " + name)
	}
}

func completedPhases(phases []PhaseView) int {
	completed := 0
	for _, phase := range phases {
		if phase.Status == PhaseSucceeded || phase.Status == PhaseSkipped {
			completed++
		}
	}
	return completed
}

func remainingLabel(remaining int) string {
	if remaining == 1 {
		return "1 phase remaining"
	}
	return fmt.Sprintf("%d phases remaining", remaining)
}

func projectStatus(status state.LifecycleStatus) string {
	if status == state.StatusFinished {
		return string(PhaseSucceeded)
	}
	if status == state.StatusTerminated {
		return string(PhaseFailed)
	}
	return string(status)
}
