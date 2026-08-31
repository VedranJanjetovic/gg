// Package resume contains shared preparation for explicit and production
// resume paths.
package resume

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/VedranJanjetovic/gg/internal/agent"
	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/state"
)

type contractSetter interface {
	SetVerificationContract(context.Context, string, state.VerificationContract, state.PipelineConfigSnapshot) (state.ProjectState, error)
}

type legacyContractMigrator interface {
	MigrateVerificationContract(context.Context, string, state.VerificationContract, state.PipelineConfigSnapshot) (state.ProjectState, error)
}

type planRecorder interface {
	RecordPlan(context.Context, string, []string, []string) (state.ProjectState, error)
}

type replanRequirer interface {
	RequireReplan(context.Context, string, string) (state.ProjectState, error)
}

// Prepare migrates legacy verification declarations from the completed
// Planning artifact and refreshes a failed Development plan from its current
// artifact. Existing completion names and phase history are never discarded.
func Prepare(ctx context.Context, project state.ProjectState, persistence any) (state.ProjectState, error) {
	if err := ctx.Err(); err != nil {
		return state.ProjectState{}, err
	}
	if project.Status != state.StatusStopped && project.Status != state.StatusFailed {
		return project, nil
	}
	// A pre-Planning legacy cursor has no verification contract to execute and
	// is still resumed by the original pipeline cursor. Planned Development
	// runs, and runs already inside Development, must migrate before dispatch.
	if project.Plan == nil && !strings.EqualFold(strings.TrimSpace(project.CurrentPhase), string(pipeline.PhaseDevelopment)) {
		return project, nil
	}
	trimmedSnapshot := bytes.TrimSpace(project.PipelineConfig.Data)
	if len(trimmedSnapshot) == 0 || bytes.Equal(trimmedSnapshot, []byte("{}")) {
		// Some in-process controller tests and pre-snapshot legacy cursors carry
		// the executable request outside ProjectState. There is no durable
		// schema-1 snapshot to migrate in that case.
		return project, nil
	}
	prepared, err := migrateLegacyVerification(ctx, project, persistence)
	if err != nil {
		return state.ProjectState{}, err
	}
	if !strings.EqualFold(strings.TrimSpace(prepared.CurrentPhase), string(pipeline.PhaseDevelopment)) {
		return prepared, nil
	}
	recorder, ok := persistence.(planRecorder)
	if !ok {
		return prepared, nil
	}
	phases, _ := agent.ReadPlanFrontmatter(prepared.WorktreePath, pipeline.PhasePlanning)
	if len(phases) == 0 {
		return prepared, nil
	}
	_, completed := agent.ReadPlanFrontmatter(prepared.WorktreePath, pipeline.PhaseDevelopment)
	updated, err := recorder.RecordPlan(ctx, prepared.Slug, phases, completed)
	if err != nil {
		return state.ProjectState{}, fmt.Errorf("refresh failed Development plan for resume: %w", err)
	}
	return updated, nil
}

// requireReplanAtPlanning persists a rewind of the resume cursor to Planning.
// It reports false when the rewind is not available — the persistence layer
// cannot record it, or the project's own pipeline has no Planning phase to
// rewind to — so the caller keeps its original error.
func requireReplanAtPlanning(ctx context.Context, project state.ProjectState, persistence any) (state.ProjectState, bool, error) {
	requirer, ok := persistence.(replanRequirer)
	if !ok {
		return state.ProjectState{}, false, nil
	}
	plan, _, _, err := pipeline.RestoreExecution(project.PipelineConfig)
	if err != nil {
		return state.ProjectState{}, false, nil
	}
	planningEnabled := false
	for _, executable := range plan.Phases() {
		if executable.Phase().ID() == pipeline.PhasePlanning {
			planningEnabled = true
			break
		}
	}
	if !planningEnabled {
		return state.ProjectState{}, false, nil
	}
	updated, err := requirer.RequireReplan(ctx, project.Slug, string(pipeline.PhasePlanning))
	if err != nil {
		return state.ProjectState{}, false, err
	}
	return updated, true, nil
}

func migrateLegacyVerification(ctx context.Context, project state.ProjectState, persistence any) (state.ProjectState, error) {
	contract, err := pipeline.SnapshotVerification(project.PipelineConfig)
	if err != nil {
		return state.ProjectState{}, fmt.Errorf("inspect project %q verification snapshot: %w", project.Slug, err)
	}
	if len(contract.Steps) > 0 {
		return project, nil
	}
	setter, setterOK := persistence.(contractSetter)
	_, migratorOK := persistence.(legacyContractMigrator)
	if !setterOK && !migratorOK {
		return state.ProjectState{}, errors.New("legacy resume requires a lifecycle service that can persist verification migration")
	}
	contract, err = agent.ReadVerificationContract(project.WorktreePath, pipeline.PhasePlanning)
	if err != nil {
		// The artifact cannot supply the contract — it predates the declaration
		// or the agent wrote it wrong. Only Planning can produce it, so rewind
		// there instead of leaving the project permanently unresumable.
		if replanned, ok, replanErr := requireReplanAtPlanning(ctx, project, persistence); replanErr != nil {
			return state.ProjectState{}, fmt.Errorf("rewind project %q to Planning for a missing verification contract: %w", project.Slug, replanErr)
		} else if ok {
			return replanned, nil
		}
		return state.ProjectState{}, fmt.Errorf("legacy project %q requires migration from .gg/plan.md; write valid gg_verification_steps and gg_repair_mode declarations, then resume: %w", project.Slug, err)
	}
	snapshot, err := pipeline.UpgradeLegacyExecutionSnapshot(project.PipelineConfig, contract)
	if err != nil {
		return state.ProjectState{}, fmt.Errorf("upgrade legacy project %q execution snapshot: %w", project.Slug, err)
	}
	if migrator, ok := persistence.(legacyContractMigrator); ok {
		updated, err := migrator.MigrateVerificationContract(ctx, project.Slug, contract, snapshot)
		if err != nil {
			return state.ProjectState{}, fmt.Errorf("persist legacy project %q verification migration: %w", project.Slug, err)
		}
		return updated, nil
	}
	updated, err := setter.SetVerificationContract(ctx, project.Slug, contract, snapshot)
	if err != nil {
		return state.ProjectState{}, fmt.Errorf("persist legacy project %q verification migration: %w", project.Slug, err)
	}
	return updated, nil
}
