package agent

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/VedranJanjetovic/gg/internal/config"
	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/state"
)

func TestRunRequestUsesExistingDomainContracts(t *testing.T) {
	request := RunRequest{
		Project:          state.ProjectState{Slug: "example", WorktreePath: "/worktree"},
		Phase:            pipeline.PhaseDevelopment,
		Subphase:         "implementation",
		Settings:         config.AgentSettings{Agent: config.AgentClaude, Model: "sonnet", Effort: config.EffortHigh},
		Prompt:           "execute the phase",
		WorkingDirectory: "/worktree",
	}

	if request.Project.Slug != "example" || request.Project.WorktreePath != request.WorkingDirectory {
		t.Fatalf("request did not preserve project worktree identity: %+v", request)
	}
	if request.Phase != pipeline.PhaseDevelopment || request.Settings.Agent != config.AgentClaude {
		t.Fatalf("request did not preserve existing phase/config contracts: %+v", request)
	}
}

func TestRunnerContractsCarryProcessOutcomeAndEvents(t *testing.T) {
	finished := time.Unix(2, 0).UTC()
	result := RunResult{
		ProjectSlug:   "example",
		Phase:         pipeline.PhaseDevelopment,
		Status:        state.StatusFinished,
		ExitCode:      0,
		StartedAt:     time.Unix(1, 0).UTC(),
		FinishedAt:    finished,
		Duration:      time.Second,
		ArtifactPaths: []string{"IMPLEMENTATION.md"},
	}
	event := Event{
		ProjectSlug: "example",
		Phase:       result.Phase,
		Type:        EventCompleted,
		At:          finished,
		Result:      &result,
	}

	if event.Result.ExitCode != 0 || event.Result.Status != state.StatusFinished {
		t.Fatalf("event did not carry the process result: %+v", event)
	}
	if event.Type != EventCompleted {
		t.Fatalf("unexpected event type: %q", event.Type)
	}
}

func TestIsSemanticFailureRejectsJoinedOperationalFailure(t *testing.T) {
	first := &SemanticFailureError{Phase: pipeline.PhaseQA, Disposition: DispositionFailed}
	second := &SemanticFailureError{Phase: pipeline.PhaseQA, Disposition: DispositionBlocked}
	if !IsSemanticFailure(first) {
		t.Fatal("direct semantic failure was not classified as semantic")
	}
	if !IsSemanticFailure(fmt.Errorf("run QA: %w", first)) {
		t.Fatal("wrapped semantic failure was not classified as semantic")
	}
	if !IsSemanticFailure(errors.Join(first, second)) {
		t.Fatal("joined semantic failures were not classified as semantic")
	}
	if IsSemanticFailure(errors.Join(first, errors.New("persist metadata"))) {
		t.Fatal("semantic failure joined with operational failure was classified as pure semantic")
	}
	if IsSemanticFailure(&SemanticFailureError{Phase: pipeline.PhaseQA, Disposition: DispositionPassed}) {
		t.Fatal("passing disposition was classified as a semantic failure")
	}
	var nilSemantic *SemanticFailureError
	if IsSemanticFailure(nilSemantic) {
		t.Fatal("nil semantic error was classified as a semantic failure")
	}
	if IsSemanticFailure(nil) {
		t.Fatal("nil error was classified as semantic")
	}
}

type contractRunner struct{}

func (contractRunner) Run(context.Context, RunRequest) (RunResult, error) { return RunResult{}, nil }

type contractProcess struct{}

func (contractProcess) Wait() (ProcessResult, error) { return ProcessResult{}, nil }
func (contractProcess) Cancel() error                { return nil }

type contractFactory struct{}

func (contractFactory) Start(context.Context, ProcessSpec) (Process, error) {
	return contractProcess{}, nil
}

type contractEventSink struct{}

func (contractEventSink) Publish(context.Context, Event) error { return nil }

type contractEventStore struct{}

func (contractEventStore) Append(context.Context, Event) error { return nil }

type contractResultStore struct{}

func (contractResultStore) Save(context.Context, RunResult) error { return nil }

var (
	_ Runner         = contractRunner{}
	_ Process        = contractProcess{}
	_ ProcessFactory = contractFactory{}
	_ EventSink      = contractEventSink{}
	_ EventStore     = contractEventStore{}
	_ ResultStore    = contractResultStore{}
)
