package main

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/VedranJanjetovic/gg/internal/config"
	"github.com/VedranJanjetovic/gg/internal/orchestrator"
	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/resume"
	"github.com/VedranJanjetovic/gg/internal/state"
)

type configuredRootsResolver interface {
	ConfiguredRoots(context.Context) ([]string, error)
}

type productionResumeSource struct{ roots config.RootResolver }

func (s productionResumeSource) Discover(ctx context.Context, runID string) ([]orchestrator.ResumeCandidate, error) {
	roots, err := configuredRoots(ctx, s.roots)
	if err != nil {
		return nil, err
	}
	candidates := make([]orchestrator.ResumeCandidate, 0)
	for _, root := range roots {
		store, err := state.NewFileStore(root)
		if err != nil {
			return nil, err
		}
		service := state.NewLifecycleService(store, nil, store.Locker())
		projects, err := service.List(ctx)
		if err != nil {
			return nil, fmt.Errorf("list projects in %s: %w", root, err)
		}
		for _, project := range projects {
			if project.Status.IsTerminal() {
				plan, subphases, maxAttempts, restoreErr := pipeline.RestoreExecution(project.PipelineConfig)
				if restoreErr != nil {
					return nil, fmt.Errorf("restore finished project %q execution snapshot: %w", project.Slug, restoreErr)
				}
				plan = plan.GitOpsOnly()
				if len(plan.Phases()) > 0 {
					if maxAttempts <= 0 {
						maxAttempts = 3
					}
					candidates = append(candidates, orchestrator.ResumeCandidate{Project: project, Kind: state.RerunResume, Execution: orchestrator.Request{Project: project, Pipeline: plan, PhaseContracts: plan.PhaseContracts(), Subphases: subphases, MaxIterations: maxAttempts, RunID: runID, GitOps: snapshotGitOpsForResume(project.PipelineConfig), ArtifactRoot: filepath.Clean(root), PullRequestURL: project.PullRequestURL}})
				} else {
					candidates = append(candidates, orchestrator.ResumeCandidate{Project: project, Kind: state.RerunFinished})
				}
				continue
			}
			if project.Interview != nil && !project.Interview.Done {
				// The project still owes the user its grooming interview:
				// auto-resume would start the pipeline past the unanswered
				// questions. It stays parked until the user answers (or opts
				// out) through an attach session.
				continue
			}
			if project.Status == state.StatusPending {
				candidates = append(candidates, orchestrator.ResumeCandidate{Project: project, Kind: state.RerunNew})
				continue
			}
			if project.Status == state.StatusRunning {
				// Only recover runs whose owning process is dead; a live run
				// owned by another gg process must not be clobbered.
				project, _, err = service.RecoverIfStale(ctx, project.Slug)
				if err != nil {
					return nil, fmt.Errorf("recover stale project %q: %w", project.Slug, err)
				}
			}
			if project.Status != state.StatusStopped && project.Status != state.StatusFailed {
				continue
			}
			projectSlug := project.Slug
			project, err = resume.Prepare(ctx, project, service)
			if err != nil {
				return nil, fmt.Errorf("prepare project %q for resume: %w", projectSlug, err)
			}
			plan, subphases, maxAttempts, err := pipeline.RestoreExecution(project.PipelineConfig)
			if err != nil {
				return nil, fmt.Errorf("restore project %q execution snapshot: %w", project.Slug, err)
			}
			if maxAttempts <= 0 {
				maxAttempts = 3
			}
			candidates = append(candidates, orchestrator.ResumeCandidate{Project: project, Kind: state.RerunResume, Execution: orchestrator.Request{Project: project, Pipeline: plan, PhaseContracts: plan.PhaseContracts(), Subphases: subphases, MaxIterations: maxAttempts, RunID: runID, GitOps: snapshotGitOpsForResume(project.PipelineConfig), ArtifactRoot: filepath.Clean(root), PullRequestURL: project.PullRequestURL}})
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Project.Slug < candidates[j].Project.Slug })
	return candidates, nil
}

func configuredRoots(ctx context.Context, resolver config.RootResolver) ([]string, error) {
	if many, ok := resolver.(configuredRootsResolver); ok {
		return many.ConfiguredRoots(ctx)
	}
	root, err := resolver.ConfiguredRoot(ctx)
	if err != nil {
		return nil, err
	}
	return []string{root}, nil
}

func snapshotGitOpsForResume(snapshot state.PipelineConfigSnapshot) config.GitOpsConfig {
	gitOps, err := pipeline.SnapshotGitOps(snapshot)
	if err != nil {
		return config.DefaultGitOpsConfig()
	}
	return gitOps
}

var _ orchestrator.ResumeSource = productionResumeSource{}
