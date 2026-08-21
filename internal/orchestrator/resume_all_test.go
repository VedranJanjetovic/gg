package orchestrator_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/VedranJanjetovic/gg/internal/orchestrator"
	"github.com/VedranJanjetovic/gg/internal/state"
)

type resumeSource struct {
	candidates []orchestrator.ResumeCandidate
}

func (s resumeSource) Discover(context.Context, string) ([]orchestrator.ResumeCandidate, error) {
	return s.candidates, nil
}

type resumeController struct {
	mu      sync.Mutex
	calls   []string
	started chan struct{}
	release chan struct{}
	fail    string
}

func (c *resumeController) Execute(context.Context, orchestrator.Request) ([]orchestrator.PhaseOutcome, error) {
	return nil, nil
}
func (c *resumeController) Stop(context.Context, orchestrator.StopRequest) error { return nil }
func (c *resumeController) Resume(ctx context.Context, req orchestrator.ResumeRequest) ([]orchestrator.PhaseOutcome, error) {
	c.mu.Lock()
	c.calls = append(c.calls, req.ProjectSlug)
	c.mu.Unlock()
	if c.started != nil {
		select {
		case c.started <- struct{}{}:
		default:
		}
	}
	if c.release != nil {
		select {
		case <-c.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if req.ProjectSlug == c.fail {
		return nil, errors.New("boom")
	}
	return nil, nil
}
func candidate(slug string, kind state.RerunKind) orchestrator.ResumeCandidate {
	return orchestrator.ResumeCandidate{Project: state.ProjectState{Slug: slug}, Kind: kind, Execution: orchestrator.Request{Project: state.ProjectState{Slug: slug}}}
}

func TestResumeAllDispatchesEligibleProjectsOnceAndIsolatesFailure(t *testing.T) {
	controller := &resumeController{}
	coordinator, err := orchestrator.NewResumeCoordinator(resumeSource{candidates: []orchestrator.ResumeCandidate{candidate("b", state.RerunResume), candidate("a", state.RerunResume), candidate("a", state.RerunResume), candidate("done", state.RerunFinished), candidate("new", state.RerunNew)}}, controller, orchestrator.ResumeCoordinatorOptions{Concurrency: 2})
	if err != nil {
		t.Fatal(err)
	}
	controller.fail = "b"
	results, err := coordinator.ResumeAll(context.Background(), orchestrator.ResumeAllRequest{RunID: "restart-1"})
	if err == nil || len(results) != 4 {
		t.Fatalf("results=%#v err=%v", results, err)
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	counts := map[string]int{}
	for _, call := range controller.calls {
		counts[call]++
	}
	if counts["a"] != 1 || counts["b"] != 1 || counts["done"] != 0 || counts["new"] != 0 {
		t.Fatalf("calls=%v", controller.calls)
	}
}

func TestResumeAllCancellationIsBounded(t *testing.T) {
	controller := &resumeController{started: make(chan struct{}, 1), release: make(chan struct{})}
	coordinator, err := orchestrator.NewResumeCoordinator(resumeSource{candidates: []orchestrator.ResumeCandidate{candidate("a", state.RerunResume), candidate("b", state.RerunResume)}}, controller, orchestrator.ResumeCoordinatorOptions{Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, e := coordinator.ResumeAll(ctx, orchestrator.ResumeAllRequest{}); done <- e }()
	select {
	case <-controller.started:
		cancel()
	case <-time.After(time.Second):
		t.Fatal("resume did not dispatch")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not return")
	}
}
