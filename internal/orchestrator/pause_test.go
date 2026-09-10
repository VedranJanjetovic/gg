package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/state"
)

type pauseCloserState struct {
	PhaseState
	closedStatus     state.LifecycleStatus
	closeCalls       int
	pausedReason     string
	pausedNextAction string
	pauseCalls       int
}

func (s *pauseCloserState) CloseRun(_ context.Context, _ string, target state.LifecycleStatus) error {
	s.closeCalls++
	s.closedStatus = target
	return nil
}

func (s *pauseCloserState) CloseRunPaused(_ context.Context, _ string, reason, nextAction string) error {
	s.pauseCalls++
	s.pausedReason = reason
	s.pausedNextAction = nextAction
	return nil
}

type plainCloserState struct {
	PhaseState
	closedStatus state.LifecycleStatus
	closeCalls   int
}

func (s *plainCloserState) CloseRun(_ context.Context, _ string, target state.LifecycleStatus) error {
	s.closeCalls++
	s.closedStatus = target
	return nil
}

func TestCloseFailedRunParksAPauseWithItsDurableRecord(t *testing.T) {
	store := &pauseCloserState{}
	controller := NewController(WithPhaseState(store)).(*sequentialController)
	cause := &pauseError{reason: "same QA finding survived 3 fix attempts", nextAction: "fix it manually, then resume", cause: errors.New("qa exhausted")}

	if got := controller.closeFailedRun("demo", cause); !errors.Is(got, cause) {
		t.Fatalf("closeFailedRun returned %v, want the original cause", got)
	}
	if store.pauseCalls != 1 || store.closeCalls != 0 {
		t.Fatalf("pause close calls = %d, plain close calls = %d; want 1 paused close only", store.pauseCalls, store.closeCalls)
	}
	if store.pausedReason != "same QA finding survived 3 fix attempts" || store.pausedNextAction != "fix it manually, then resume" {
		t.Fatalf("persisted pause = %q / %q", store.pausedReason, store.pausedNextAction)
	}
}

func TestCloseFailedRunFallsBackToStoppedWithoutAPauseCloser(t *testing.T) {
	store := &plainCloserState{}
	controller := NewController(WithPhaseState(store)).(*sequentialController)
	cause := &pauseError{reason: "verification preflight blocked", cause: errors.New("blocked")}

	if got := controller.closeFailedRun("demo", cause); !errors.Is(got, cause) {
		t.Fatalf("closeFailedRun returned %v, want the original cause", got)
	}
	if store.closeCalls != 1 || store.closedStatus != state.StatusStopped {
		t.Fatalf("close calls = %d status = %s, want one stopped close", store.closeCalls, store.closedStatus)
	}
}

func TestCloseFailedRunKeepsNonPauseFailuresTerminal(t *testing.T) {
	store := &pauseCloserState{}
	controller := NewController(WithPhaseState(store)).(*sequentialController)
	cause := fmt.Errorf("agent stderr: %w", errors.New("segfault"))

	if got := controller.closeFailedRun("demo", cause); !errors.Is(got, cause) {
		t.Fatalf("closeFailedRun returned %v, want the original cause", got)
	}
	if store.pauseCalls != 0 || store.closeCalls != 1 || store.closedStatus != state.StatusFailed {
		t.Fatalf("pause calls = %d close calls = %d status = %s, want one failed close", store.pauseCalls, store.closeCalls, store.closedStatus)
	}
}
