package pipeline

// PhaseID is a stable machine-readable identifier for a pipeline phase.
type PhaseID string

const (
	PhaseAcceptanceCriteria PhaseID = "acceptance_criteria"
	PhaseGrooming           PhaseID = "grooming"
	PhasePlanning           PhaseID = "planning"
	PhaseDevelopment        PhaseID = "development"
	PhaseQA                 PhaseID = "qa"
	PhaseRebase             PhaseID = "rebase"
	PhaseTestDocument       PhaseID = "test_document"
	PhaseBuildChecker       PhaseID = "build_checker"
	PhasePR                 PhaseID = "pr"
	PhaseCI                 PhaseID = "ci"
)

var canonicalArtifactNames = map[PhaseID]string{
	PhaseAcceptanceCriteria: "acceptance-criteria.md",
	PhaseGrooming:           "grooming.md",
	PhasePlanning:           "plan.md",
	PhaseDevelopment:        "development.md",
	PhaseQA:                 "qa-report.md",
	PhaseRebase:             "rebase-report.md",
	PhaseTestDocument:       "test-document.md",
	PhaseBuildChecker:       "build-checker.md",
	PhasePR:                 "pr.md",
	PhaseCI:                 "ci-report.md",
}

// ArtifactDirectory is the worktree subdirectory holding every phase
// artifact. It carries a self-ignoring .gitignore so artifacts never clutter
// the repository, commits, or pull requests.
const ArtifactDirectory = ".gg"

// CanonicalArtifactName returns the worktree-relative path of the fixed
// phase output declared by the canonical phase contract.
func CanonicalArtifactName(id PhaseID) (string, bool) {
	name, ok := canonicalArtifactNames[id]
	if !ok {
		return "", false
	}
	return ArtifactDirectory + "/" + name, true
}

// CanonicalArtifactBaseNames returns the bare artifact file names of every
// canonical phase, used to migrate legacy root-level artifacts.
func CanonicalArtifactBaseNames() []string {
	names := make([]string, 0, len(canonicalArtifactNames))
	for _, name := range canonicalArtifactNames {
		names = append(names, name)
	}
	return names
}

// PhaseMetadata contains the human-readable properties of a phase.
type PhaseMetadata struct {
	DisplayName string
	Optional    bool
}

// Phase is one canonical pipeline phase.
type Phase struct {
	id       PhaseID
	metadata PhaseMetadata
}

// NewPhase constructs a phase definition. Resolve validates that a definition
// belongs to the canonical pipeline before it can be executed.
func NewPhase(id PhaseID, metadata PhaseMetadata) Phase {
	return Phase{id: id, metadata: metadata}
}

// ID returns the stable identifier for the phase.
func (p Phase) ID() PhaseID {
	return p.id
}

// Metadata returns the display metadata for the phase.
func (p Phase) Metadata() PhaseMetadata {
	return p.metadata
}

// Pipeline is an ordered canonical sequence of phases.
type Pipeline struct {
	phases []Phase
}

// NewPipeline constructs a pipeline from phase definitions.
func NewPipeline(phases []Phase) Pipeline {
	copied := make([]Phase, len(phases))
	copy(copied, phases)
	return Pipeline{phases: copied}
}

// DefaultPipeline constructs the canonical gg pipeline in its fixed order.
func DefaultPipeline() Pipeline {
	return NewPipeline([]Phase{
		NewPhase(PhaseAcceptanceCriteria, PhaseMetadata{DisplayName: "Acceptance criteria"}),
		NewPhase(PhaseGrooming, PhaseMetadata{DisplayName: "Grooming", Optional: true}),
		NewPhase(PhasePlanning, PhaseMetadata{DisplayName: "Planning", Optional: true}),
		NewPhase(PhaseDevelopment, PhaseMetadata{DisplayName: "Development"}),
		NewPhase(PhaseQA, PhaseMetadata{DisplayName: "QA", Optional: true}),
		NewPhase(PhaseRebase, PhaseMetadata{DisplayName: "Rebase"}),
		NewPhase(PhaseTestDocument, PhaseMetadata{DisplayName: "Test/Document"}),
		NewPhase(PhaseBuildChecker, PhaseMetadata{DisplayName: "Build checker", Optional: true}),
		NewPhase(PhasePR, PhaseMetadata{DisplayName: "PR", Optional: true}),
		NewPhase(PhaseCI, PhaseMetadata{DisplayName: "CI", Optional: true}),
	})
}

// Phases returns the canonical phases in execution order.
func (p Pipeline) Phases() []Phase {
	phases := make([]Phase, len(p.phases))
	copy(phases, p.phases)
	return phases
}
