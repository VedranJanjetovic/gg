package pipeline

import (
	"fmt"
	"strings"
)

// DevelopmentSubphaseID is a stable machine-readable identifier for a generated
// Development subphase.
type DevelopmentSubphaseID string

const (
	DevelopmentSubphaseImplementation DevelopmentSubphaseID = "implementation"
	DevelopmentSubphaseTesting        DevelopmentSubphaseID = "testing"
	DevelopmentSubphaseReview         DevelopmentSubphaseID = "review"
)

// DevelopmentSubphaseMode determines how Development subphases are generated.
type DevelopmentSubphaseMode uint8

const (
	// DevelopmentSubphasesDefault uses the canonical Development subphases.
	DevelopmentSubphasesDefault DevelopmentSubphaseMode = iota
	// DevelopmentSubphasesOverride uses only caller-provided subphases.
	DevelopmentSubphasesOverride
	// DevelopmentSubphasesDisabled generates no Development subphases.
	DevelopmentSubphasesDisabled
)

// DevelopmentSubphaseDefinition is caller-provided data for one generated
// Development subphase.
type DevelopmentSubphaseDefinition struct {
	ID          DevelopmentSubphaseID `json:"id"`
	DisplayName string                `json:"displayName"`
}

// DevelopmentSubphaseGeneration selects the default, override, or disabled
// Development subphase sequence. It is intentionally independent of persisted
// phase configuration, which remains owned by config.
type DevelopmentSubphaseGeneration struct {
	Mode      DevelopmentSubphaseMode         `json:"mode"`
	Subphases []DevelopmentSubphaseDefinition `json:"subphases,omitempty"`
}

// DevelopmentSubphase is one generated Development subphase.
type DevelopmentSubphase struct {
	id          DevelopmentSubphaseID
	displayName string
}

// ID returns the subphase's stable identifier.
func (s DevelopmentSubphase) ID() DevelopmentSubphaseID {
	return s.id
}

// DisplayName returns the subphase's human-readable name.
func (s DevelopmentSubphase) DisplayName() string {
	return s.displayName
}

var defaultDevelopmentSubphases = [...]DevelopmentSubphase{
	{id: DevelopmentSubphaseImplementation, displayName: "Implementation"},
	{id: DevelopmentSubphaseTesting, displayName: "Testing"},
	{id: DevelopmentSubphaseReview, displayName: "Review"},
}

// GenerateDevelopmentSubphases returns Development subphases in deterministic
// execution order. The returned slice does not share mutable storage with the
// generator or caller input.
func GenerateDevelopmentSubphases(generation DevelopmentSubphaseGeneration) ([]DevelopmentSubphase, error) {
	switch generation.Mode {
	case DevelopmentSubphasesDefault:
		if len(generation.Subphases) != 0 {
			return nil, fmt.Errorf("default Development subphases cannot include overrides")
		}
		return copyDevelopmentSubphases(defaultDevelopmentSubphases[:]), nil
	case DevelopmentSubphasesDisabled:
		if len(generation.Subphases) != 0 {
			return nil, fmt.Errorf("disabled Development subphases cannot include overrides")
		}
		return nil, nil
	case DevelopmentSubphasesOverride:
		return generateDevelopmentSubphaseOverrides(generation.Subphases)
	default:
		return nil, fmt.Errorf("unknown Development subphase mode %d", generation.Mode)
	}
}

func generateDevelopmentSubphaseOverrides(definitions []DevelopmentSubphaseDefinition) ([]DevelopmentSubphase, error) {
	if len(definitions) == 0 {
		return nil, fmt.Errorf("Development subphase override cannot be empty")
	}

	subphases := make([]DevelopmentSubphase, len(definitions))
	seen := make(map[DevelopmentSubphaseID]struct{}, len(definitions))
	for index, definition := range definitions {
		if definition.ID == "" {
			return nil, fmt.Errorf("Development subphase %d has an empty ID", index)
		}
		if _, exists := seen[definition.ID]; exists {
			return nil, fmt.Errorf("duplicate Development subphase ID %q", definition.ID)
		}
		if err := validateDevelopmentSubphaseDisplayName(definition.DisplayName); err != nil {
			return nil, fmt.Errorf("Development subphase %q: %w", definition.ID, err)
		}
		seen[definition.ID] = struct{}{}
		subphases[index] = DevelopmentSubphase{id: definition.ID, displayName: definition.DisplayName}
	}
	return subphases, nil
}

func validateDevelopmentSubphaseDisplayName(displayName string) error {
	wordCount := len(strings.Fields(displayName))
	if wordCount < 1 || wordCount > 3 {
		return fmt.Errorf("display name must contain one to three words")
	}
	return nil
}

func copyDevelopmentSubphases(source []DevelopmentSubphase) []DevelopmentSubphase {
	subphases := make([]DevelopmentSubphase, len(source))
	copy(subphases, source)
	return subphases
}
