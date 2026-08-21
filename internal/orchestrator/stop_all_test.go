package orchestrator_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/orchestrator"
	"github.com/VedranJanjetovic/gg/internal/state"
)

type stopAllLister struct {
	projects []state.ProjectState
	err      error
}

func (l stopAllLister) List(context.Context) ([]state.ProjectState, error) {
	return l.projects, l.err
}

type stopAllStopper struct {
	calls  []orchestrator.StopRequest
	failAt map[string]error
}

func (s *stopAllStopper) Stop(_ context.Context, request orchestrator.StopRequest) error {
	s.calls = append(s.calls, request)
	return s.failAt[request.ProjectSlug]
}

func TestStopAllSelectsOnlyRunningProjects(t *testing.T) {
	cases := []struct {
		name         string
		projects     []state.ProjectState
		failAt       map[string]error
		wantCalls    []orchestrator.StopRequest
		wantRunning  int
		wantStopped  int
		wantFailures int
		wantSummary  string
	}{
		{
			name:        "zero active",
			projects:    []state.ProjectState{{Slug: "pending", Status: state.StatusPending}, {Slug: "stopped", Status: state.StatusStopped}, {Slug: "failed", Status: state.StatusFailed}, {Slug: "finished", Status: state.StatusFinished}, {Slug: "terminated", Status: state.StatusTerminated}},
			wantSummary: "no running projects to stop",
		},
		{
			name:        "one running",
			projects:    []state.ProjectState{{Slug: "one", ActiveRunID: "run-one", Status: state.StatusRunning}},
			wantCalls:   []orchestrator.StopRequest{{ProjectSlug: "one", RunID: "run-one"}},
			wantRunning: 1,
			wantStopped: 1,
			wantSummary: "stop requested for 1 running project(s)",
		},
		{
			name: "multiple running",
			projects: []state.ProjectState{
				{Slug: "first", ActiveRunID: "run-first", Status: state.StatusRunning},
				{Slug: "second", ActiveRunID: "run-second", Status: state.StatusRunning},
			},
			wantCalls:   []orchestrator.StopRequest{{ProjectSlug: "first", RunID: "run-first"}, {ProjectSlug: "second", RunID: "run-second"}},
			wantRunning: 2,
			wantStopped: 2,
			wantSummary: "stop requested for 2 running project(s)",
		},
		{
			name: "mixed statuses",
			projects: []state.ProjectState{
				{Slug: "pending", Status: state.StatusPending},
				{Slug: "running", ActiveRunID: "run", Status: state.StatusRunning},
				{Slug: "stopped", Status: state.StatusStopped},
				{Slug: "failed", Status: state.StatusFailed},
				{Slug: "finished", Status: state.StatusFinished},
				{Slug: "terminated", Status: state.StatusTerminated},
			},
			wantCalls:   []orchestrator.StopRequest{{ProjectSlug: "running", RunID: "run"}},
			wantRunning: 1,
			wantStopped: 1,
			wantSummary: "stop requested for 1 running project(s)",
		},
		{
			name: "partial failure continues",
			projects: []state.ProjectState{
				{Slug: "first", ActiveRunID: "run-first", Status: state.StatusRunning},
				{Slug: "broken", ActiveRunID: "run-broken", Status: state.StatusRunning},
				{Slug: "last", ActiveRunID: "run-last", Status: state.StatusRunning},
			},
			failAt:       map[string]error{"broken": errors.New("durable request failed")},
			wantCalls:    []orchestrator.StopRequest{{ProjectSlug: "first", RunID: "run-first"}, {ProjectSlug: "broken", RunID: "run-broken"}, {ProjectSlug: "last", RunID: "run-last"}},
			wantRunning:  3,
			wantStopped:  2,
			wantFailures: 1,
			wantSummary:  "stop requested for 2 of 3 running project(s); 1 failed",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stopper := &stopAllStopper{failAt: tc.failAt}
			result, err := orchestrator.StopAll(context.Background(), stopAllLister{projects: tc.projects}, stopper)
			if tc.wantFailures == 0 && err != nil {
				t.Fatalf("StopAll() error = %v", err)
			}
			if tc.wantFailures > 0 {
				var stopErr *orchestrator.StopAllError
				if !errors.As(err, &stopErr) || len(stopErr.Failures) != tc.wantFailures {
					t.Fatalf("StopAll() error = %v, want %d failures", err, tc.wantFailures)
				}
				if !strings.Contains(err.Error(), "broken") {
					t.Fatalf("partial error = %q, want project name", err)
				}
			}
			if result.Running != tc.wantRunning || result.Stopped != tc.wantStopped || len(result.Failures) != tc.wantFailures {
				t.Fatalf("result = %#v, want running=%d stopped=%d failures=%d", result, tc.wantRunning, tc.wantStopped, tc.wantFailures)
			}
			if !reflect.DeepEqual(stopper.calls, tc.wantCalls) {
				t.Fatalf("stop calls = %#v, want %#v", stopper.calls, tc.wantCalls)
			}
			if got := result.Summary(); got != tc.wantSummary {
				t.Fatalf("summary = %q, want %q", got, tc.wantSummary)
			}
		})
	}
}

func TestStopAllPropagatesListFailureWithoutStopping(t *testing.T) {
	listErr := errors.New("state unavailable")
	stopper := &stopAllStopper{}
	_, err := orchestrator.StopAll(context.Background(), stopAllLister{err: listErr}, stopper)
	if !errors.Is(err, listErr) {
		t.Fatalf("error = %v, want list error", err)
	}
	if len(stopper.calls) != 0 {
		t.Fatalf("stop calls = %#v, want none", stopper.calls)
	}
}
