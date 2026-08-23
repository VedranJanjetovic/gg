package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// PlanningComplexity is the planner's self-assessed scope category. The
// category is evidence recorded by the planner; gg only validates the
// structural contract and the two hard count rules.
type PlanningComplexity string

const (
	PlanningTrivial  PlanningComplexity = "Trivial"
	PlanningSimple   PlanningComplexity = "Simple"
	PlanningModerate PlanningComplexity = "Moderate"
	PlanningComplex  PlanningComplexity = "Complex"

	MaxPlanningPhases   = 10
	MaxPlanningAttempts = 3
)

// PlanningPhaseBoundary is the human-readable reason for keeping one phase
// separate from the rest of the plan.
type PlanningPhaseBoundary struct {
	Phase         string `yaml:"phase" json:"phase"`
	Justification string `yaml:"justification" json:"justification"`
}

// PlanningArtifact is the structural portion of plan.md used by the
// Planning execution gate.
type PlanningArtifact struct {
	Complexity                  PlanningComplexity      `yaml:"gg_plan_complexity" json:"gg_plan_complexity"`
	Evidence                    []string                `yaml:"gg_plan_complexity_evidence" json:"gg_plan_complexity_evidence"`
	Phases                      []string                `yaml:"gg_plan_phases" json:"gg_plan_phases"`
	Boundaries                  []PlanningPhaseBoundary `yaml:"gg_plan_phase_boundaries" json:"gg_plan_phase_boundaries"`
	BodyCategory                PlanningComplexity      `json:"-"`
	BodyEvidence                []string                `json:"-"`
	BodyPhaseCount              int                     `json:"-"`
	BodyPhases                  []string                `json:"-"`
	BodyBoundaries              []PlanningPhaseBoundary `json:"-"`
	BodyHasComplexityAssessment bool                    `json:"-"`
	BodyHasSupportingEvidence   bool                    `json:"-"`
}

// PlanningContractError contains every deterministic violation found in one
// plan artifact. Artifact is retained so a corrective Planning invocation can
// inspect exactly what the previous invocation produced.
type PlanningContractError struct {
	ArtifactPath string
	Artifact     string
	Violations   []string
}

func (e *PlanningContractError) Error() string {
	if e == nil {
		return "planning contract validation failed"
	}
	if len(e.Violations) == 0 {
		return "planning contract validation failed"
	}
	return "planning contract validation failed: " + strings.Join(e.Violations, "; ")
}

// ParsePlanningArtifact parses the frontmatter and fixed structural body
// section of plan.md. It does not apply the project compatibility policy.
func ParsePlanningArtifact(data []byte) (PlanningArtifact, error) {
	artifact := PlanningArtifact{}
	frontmatter, body, err := splitPlanningFrontmatter(string(data))
	if err != nil {
		return artifact, err
	}
	var fields PlanningArtifact
	if err := yaml.Unmarshal([]byte(frontmatter), &fields); err != nil {
		return artifact, fmt.Errorf("parse planning frontmatter: %w", err)
	}
	artifact = fields
	artifact.BodyCategory = planningBodyCategory(body)
	artifact.BodyEvidence = planningBodyEvidence(body)
	artifact.BodyPhaseCount = planningBodyPhaseCount(body)
	artifact.BodyPhases = planningBodyPhases(body)
	artifact.BodyBoundaries = planningBodyBoundaries(body)
	artifact.BodyHasComplexityAssessment = hasPlanningComplexityAssessmentBody(body)
	artifact.BodyHasSupportingEvidence = hasPlanningSupportingEvidenceBody(body)
	return artifact, nil
}

// ValidatePlanningArtifact validates a new project's plan.md. It is separate
// from ReadPlanFrontmatter, whose tolerant behavior is retained for legacy
// display and resume state.
func ValidatePlanningArtifact(root string) (PlanningArtifact, error) {
	path := filepath.Join(filepath.Clean(root), ".gg", "plan.md")
	data, err := os.ReadFile(path)
	if err != nil {
		return PlanningArtifact{}, &PlanningContractError{ArtifactPath: ".gg/plan.md", Violations: []string{fmt.Sprintf("read %s: %v", ".gg/plan.md", err)}}
	}
	artifact, parseErr := ParsePlanningArtifact(data)
	if parseErr != nil {
		return artifact, &PlanningContractError{ArtifactPath: ".gg/plan.md", Artifact: string(data), Violations: []string{parseErr.Error()}}
	}
	frontmatter, _, _ := splitPlanningFrontmatter(string(data))
	violations := append(validatePlanningFrontmatterShape(frontmatter), validatePlanningArtifact(artifact)...)
	if len(violations) > 0 {
		return artifact, &PlanningContractError{ArtifactPath: ".gg/plan.md", Artifact: string(data), Violations: violations}
	}
	return artifact, nil
}

func validatePlanningFrontmatterShape(frontmatter string) []string {
	keys := []string{"gg_plan_complexity", "gg_plan_complexity_evidence", "gg_plan_phases", "gg_plan_phase_boundaries"}
	lines := strings.Split(frontmatter, "\n")
	violations := make([]string, 0)
	for _, key := range keys {
		for index, line := range lines {
			field, value, found := strings.Cut(strings.TrimSpace(line), ":")
			if !found || field != key {
				continue
			}
			if strings.TrimSpace(value) == "" {
				violations = append(violations, fmt.Sprintf("frontmatter %s must be a single-line value", key))
			} else {
				for _, continuation := range lines[index+1:] {
					trimmed := strings.TrimSpace(continuation)
					if trimmed == "" || strings.HasPrefix(trimmed, "#") {
						continue
					}
					field, _, isField := strings.Cut(trimmed, ":")
					if isField && field != "" && !strings.ContainsAny(field, " []{}") {
						break
					}
					violations = append(violations, fmt.Sprintf("frontmatter %s must not use a multiline value", key))
					break
				}
			}
			break
		}
	}
	return violations
}

func validatePlanningArtifact(artifact PlanningArtifact) []string {
	violations := make([]string, 0)
	if artifact.Complexity == "" {
		violations = append(violations, "frontmatter gg_plan_complexity is required")
	} else if !validPlanningComplexity(artifact.Complexity) {
		violations = append(violations, fmt.Sprintf("frontmatter gg_plan_complexity %q is invalid", artifact.Complexity))
	}
	if len(nonBlank(artifact.Evidence)) == 0 {
		violations = append(violations, "frontmatter gg_plan_complexity_evidence must contain at least one item")
	}
	if len(nonBlank(artifact.Phases)) == 0 {
		violations = append(violations, "frontmatter gg_plan_phases must contain at least one phase")
	}
	if len(artifact.Phases) > MaxPlanningPhases {
		violations = append(violations, fmt.Sprintf("phase-limit-exceeded: plan contains %d phases, maximum is %d", len(artifact.Phases), MaxPlanningPhases))
	}
	if artifact.Complexity == PlanningTrivial && len(artifact.Phases) != 1 {
		violations = append(violations, fmt.Sprintf("Trivial plans must contain exactly one phase, got %d", len(artifact.Phases)))
	}

	seen := make(map[string]bool, len(artifact.Phases))
	for index, phase := range artifact.Phases {
		phase = strings.TrimSpace(phase)
		if phase == "" {
			violations = append(violations, fmt.Sprintf("frontmatter gg_plan_phases item %d is blank", index+1))
			continue
		}
		if seen[phase] {
			violations = append(violations, fmt.Sprintf("frontmatter gg_plan_phases contains duplicate phase %q", phase))
		}
		seen[phase] = true
	}

	if len(artifact.Boundaries) != len(artifact.Phases) {
		violations = append(violations, fmt.Sprintf("frontmatter gg_plan_phase_boundaries must contain one explanation per phase, got %d for %d phases", len(artifact.Boundaries), len(artifact.Phases)))
	}
	boundaryByPhase := make(map[string]string, len(artifact.Boundaries))
	for index, boundary := range artifact.Boundaries {
		phase, justification := strings.TrimSpace(boundary.Phase), strings.TrimSpace(boundary.Justification)
		if phase == "" || justification == "" {
			violations = append(violations, fmt.Sprintf("frontmatter gg_plan_phase_boundaries item %d requires phase and justification", index+1))
		}
		if _, exists := boundaryByPhase[phase]; exists && phase != "" {
			violations = append(violations, fmt.Sprintf("frontmatter gg_plan_phase_boundaries contains duplicate phase %q", phase))
		}
		boundaryByPhase[phase] = justification
	}
	for index, phase := range artifact.Phases {
		if index >= len(artifact.Boundaries) {
			break
		}
		if got := strings.TrimSpace(artifact.Boundaries[index].Phase); got != strings.TrimSpace(phase) {
			violations = append(violations, fmt.Sprintf("frontmatter phase boundary %d names %q, want %q in plan order", index+1, got, phase))
		}
	}

	if artifact.BodyCategory == "" {
		violations = append(violations, "plan body is missing the Complexity assessment category")
	} else if artifact.BodyCategory != artifact.Complexity {
		violations = append(violations, fmt.Sprintf("body complexity %q does not match frontmatter %q", artifact.BodyCategory, artifact.Complexity))
	}
	if !hasPlanningComplexityAssessment(artifact) {
		violations = append(violations, "plan body is missing the Complexity assessment section")
	}
	if !hasPlanningSupportingEvidence(artifact) {
		violations = append(violations, "plan body is missing the Supporting evidence section")
	}
	if artifact.BodyPhaseCount == 0 {
		violations = append(violations, "plan body is missing the selected phase count")
	} else if artifact.BodyPhaseCount != len(artifact.Phases) {
		violations = append(violations, fmt.Sprintf("body selected phase count %d does not match frontmatter phase count %d", artifact.BodyPhaseCount, len(artifact.Phases)))
	}
	if !stringSlicesEqual(nonBlank(artifact.Evidence), nonBlank(artifact.BodyEvidence)) {
		violations = append(violations, "body supporting evidence does not match frontmatter gg_plan_complexity_evidence")
	}
	if !stringSlicesEqual(trimmed(artifact.Phases), trimmed(artifact.BodyPhases)) {
		violations = append(violations, "body phase names or order do not match frontmatter gg_plan_phases")
	}
	if !boundariesEqual(artifact.Boundaries, artifact.BodyBoundaries) {
		violations = append(violations, "body phase-boundary justifications do not match frontmatter gg_plan_phase_boundaries")
	}
	return violations
}

func validPlanningComplexity(value PlanningComplexity) bool {
	switch value {
	case PlanningTrivial, PlanningSimple, PlanningModerate, PlanningComplex:
		return true
	default:
		return false
	}
}

func splitPlanningFrontmatter(data string) (frontmatter, body string, err error) {
	data = strings.ReplaceAll(data, "\r\n", "\n")
	if !strings.HasPrefix(data, "---\n") {
		return "", data, errors.New("plan artifact must begin with YAML frontmatter")
	}
	end := strings.Index(data[4:], "\n---\n")
	if end < 0 {
		return "", data, errors.New("plan artifact has unterminated YAML frontmatter")
	}
	end += 4
	return data[4:end], data[end+5:], nil
}

var (
	bodyCategoryPattern = regexp.MustCompile(`(?m)^- Complexity category:\s*\*\*([^*\n]+)\*\*\s*$`)
	bodyCountPattern    = regexp.MustCompile(`(?m)^- Selected phase count:\s*\*\*([0-9]+)\*\*\s*$`)
	bodyPhasePattern    = regexp.MustCompile(`(?m)^##\s+(Phase\s+[0-9]+:\s+.+?)\s*$`)
	bodyBoundaryPattern = regexp.MustCompile(`(?m)^Boundary justification:\s*(.+?)\s*$`)
	bodySectionPattern  = regexp.MustCompile(`(?m)^##\s+(.+?)\s*$`)
)

func planningComplexitySection(body string) (string, bool) {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	start := -1
	for index, line := range lines {
		if strings.TrimSpace(line) == "## Complexity assessment" {
			start = index + 1
			break
		}
	}
	if start < 0 {
		return "", false
	}
	end := len(lines)
	for index := start; index < len(lines); index++ {
		if bodySectionPattern.MatchString(lines[index]) {
			end = index
			break
		}
	}
	return strings.Join(lines[start:end], "\n"), true
}

func hasPlanningComplexityAssessment(artifact PlanningArtifact) bool {
	return artifact.BodyHasComplexityAssessment
}

func hasPlanningSupportingEvidence(artifact PlanningArtifact) bool {
	return artifact.BodyHasSupportingEvidence
}

func hasPlanningComplexityAssessmentBody(body string) bool {
	_, ok := planningComplexitySection(body)
	return ok
}

func hasPlanningSupportingEvidenceBody(body string) bool {
	section, ok := planningComplexitySection(body)
	return ok && strings.Contains(section, "Supporting evidence:")
}

func planningBodyCategory(body string) PlanningComplexity {
	section, ok := planningComplexitySection(body)
	if !ok {
		return ""
	}
	match := bodyCategoryPattern.FindStringSubmatch(section)
	if len(match) == 2 {
		return PlanningComplexity(strings.TrimSpace(match[1]))
	}
	return ""
}

func planningBodyEvidence(body string) []string {
	section, ok := planningComplexitySection(body)
	if !ok {
		return nil
	}
	start := strings.Index(section, "Supporting evidence:")
	if start < 0 {
		return nil
	}
	rest := section[start+len("Supporting evidence:"):]
	rest = strings.TrimLeft(rest, "\n")
	if end := strings.Index(rest, "\n\n"); end >= 0 {
		rest = rest[:end]
	}
	return numberedBodyValues(rest)
}

func planningBodyPhaseCount(body string) int {
	section, ok := planningComplexitySection(body)
	if !ok {
		return 0
	}
	match := bodyCountPattern.FindStringSubmatch(section)
	if len(match) != 2 {
		return 0
	}
	count, err := strconv.Atoi(match[1])
	if err != nil {
		return 0
	}
	return count
}

func planningBodyPhases(body string) []string {
	matches := bodyPhasePattern.FindAllStringSubmatch(body, -1)
	phases := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) == 2 {
			phases = append(phases, strings.TrimSpace(match[1]))
		}
	}
	return phases
}

func planningBodyBoundaries(body string) []PlanningPhaseBoundary {
	justifications := bodyBoundaryPattern.FindAllStringSubmatch(body, -1)
	phases := planningBodyPhases(body)
	boundaries := make([]PlanningPhaseBoundary, 0, len(justifications))
	for index, match := range justifications {
		if len(match) != 2 {
			continue
		}
		phase := ""
		if index < len(phases) {
			phase = phases[index]
		}
		boundaries = append(boundaries, PlanningPhaseBoundary{Phase: phase, Justification: strings.TrimSpace(match[1])})
	}
	return boundaries
}

func numberedBodyValues(value string) []string {
	lines := strings.Split(value, "\n")
	values := make([]string, 0)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if len(line) < 3 || line[0] < '0' || line[0] > '9' {
			continue
		}
		period := strings.IndexByte(line, '.')
		if period < 1 {
			continue
		}
		if value, err := strconv.Atoi(line[:period]); err != nil || value < 1 {
			continue
		}
		if item := strings.TrimSpace(line[period+1:]); item != "" {
			values = append(values, item)
		}
	}
	return values
}

func nonBlank(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func trimmed(values []string) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = strings.TrimSpace(value)
	}
	return result
}

func stringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if strings.TrimSpace(left[index]) != strings.TrimSpace(right[index]) {
			return false
		}
	}
	return true
}

func boundariesEqual(left, right []PlanningPhaseBoundary) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if strings.TrimSpace(left[index].Phase) != strings.TrimSpace(right[index].Phase) || strings.TrimSpace(left[index].Justification) != strings.TrimSpace(right[index].Justification) {
			return false
		}
	}
	return true
}
