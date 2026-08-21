package state

import "time"

// TerminalKind describes why a project can no longer advance. It is separate
// from LifecycleStatus so a pipeline-complete project is not confused with a
// pull request that has been merged.
type TerminalKind string

const (
	TerminalNone              TerminalKind = ""
	TerminalPipelineComplete  TerminalKind = "pipeline_complete"
	TerminalPullRequestMerged TerminalKind = "pull_request_merged"
	TerminalTerminated        TerminalKind = "terminated"
)

// TerminalState is an optional durable terminal marker. It is omitted from
// legacy state and should only be set for terminal lifecycle statuses.
type TerminalState struct {
	Kind           TerminalKind `json:"kind"`
	At             time.Time    `json:"at"`
	PullRequestURL string       `json:"pullRequestUrl,omitempty"`
}

// IsValid reports whether the terminal kind is supported.
func (kind TerminalKind) IsValid() bool {
	switch kind {
	case TerminalNone, TerminalPipelineComplete, TerminalPullRequestMerged, TerminalTerminated:
		return true
	default:
		return false
	}
}

// IsCompletion reports whether kind represents successful completion rather
// than termination or an unfinished pipeline.
func (kind TerminalKind) IsCompletion() bool {
	return kind == TerminalPipelineComplete || kind == TerminalPullRequestMerged
}

// ProjectObservation is the read model shared by global status, TUI attachment,
// notifications, and restart-safe consumers. Project remains the source of
// truth; the derived fields avoid each consumer reimplementing terminal rules.
type ProjectObservation struct {
	Project      ProjectState `json:"project"`
	Terminal     bool         `json:"terminal"`
	TerminalKind TerminalKind `json:"terminalKind"`
}

// Observe derives one stable observation without mutating persisted state.
func Observe(project ProjectState) ProjectObservation {
	kind := TerminalNone
	if project.Terminal != nil {
		kind = project.Terminal.Kind
	}
	if kind == TerminalNone {
		switch project.Status {
		case StatusFinished:
			kind = TerminalPipelineComplete
		case StatusTerminated:
			kind = TerminalTerminated
		}
	}
	return ProjectObservation{Project: project, Terminal: project.Status.IsTerminal(), TerminalKind: kind}
}

// ObserveAll creates observations in the supplied order. Store.List already
// provides deterministic slug ordering; this helper intentionally preserves it.
func ObserveAll(projects []ProjectState) []ProjectObservation {
	observations := make([]ProjectObservation, len(projects))
	for i, project := range projects {
		observations[i] = Observe(project)
	}
	return observations
}

// RerunKind describes whether a request starts fresh work, resumes durable
// work, or deliberately reruns a terminal project.
type RerunKind string

const (
	RerunNew      RerunKind = "new"
	RerunResume   RerunKind = "resume"
	RerunFinished RerunKind = "finished"
)

// ClassifyRerun maps persisted lifecycle state to the later command/TUI action.
// Terminal projects are explicitly rerun; stopped and failed projects resume
// their durable cursor. Pending projects start normally.
func ClassifyRerun(project ProjectState) RerunKind {
	if project.Status.IsTerminal() {
		return RerunFinished
	}
	if project.Status == StatusStopped || project.Status == StatusFailed {
		return RerunResume
	}
	return RerunNew
}
