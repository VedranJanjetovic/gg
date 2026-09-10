package orchestrator

import (
	"reflect"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/agent"
	"github.com/VedranJanjetovic/gg/internal/state"
)

func TestAdvanceQAFindingStrikesCountsRecurrenceAndNeverResets(t *testing.T) {
	// Attempt 1: two fresh findings.
	strikes, struck := advanceQAFindingStrikes(nil, []agent.QAFinding{
		{ID: "refund-flow-500", Summary: "refund returns 500", New: true},
		{ID: "missing-audit-log", Summary: "no audit entry", New: true},
	})
	if struck != nil {
		t.Fatalf("first attempt struck %#v", struck)
	}
	want := []state.QAFindingStrike{
		{ID: "refund-flow-500", Summary: "refund returns 500", Strikes: 1},
		{ID: "missing-audit-log", Summary: "no audit entry", Strikes: 1},
	}
	if !reflect.DeepEqual(strikes, want) {
		t.Fatalf("strikes = %#v", strikes)
	}

	// Attempt 2: one resolved, one persists, one new — progress, no strike-out.
	strikes, struck = advanceQAFindingStrikes(strikes, []agent.QAFinding{
		{ID: "refund-flow-500", New: false},
		{ID: "stale-cache", Summary: "cache not invalidated", New: true},
	})
	if struck != nil {
		t.Fatalf("second attempt struck %#v", struck)
	}
	if strikes[0].Strikes != 2 || strikes[1].Strikes != 1 || strikes[2].Strikes != 1 {
		t.Fatalf("strikes after progress = %#v", strikes)
	}

	// Attempt 3: the same finding a third time strikes out, even though other
	// findings were resolved in between.
	strikes, struck = advanceQAFindingStrikes(strikes, []agent.QAFinding{{ID: "refund-flow-500", New: false}})
	if struck == nil || struck.ID != "refund-flow-500" || struck.Strikes != MaxQAFindingStrikes {
		t.Fatalf("third recurrence struck = %#v", struck)
	}
	if strikes[0].Strikes != 3 {
		t.Fatalf("strikes after strike-out = %#v", strikes)
	}
}

func TestAdvanceQAFindingStrikesResumesCountForAFindingThatDisappearsAndReturns(t *testing.T) {
	strikes, _ := advanceQAFindingStrikes(nil, []agent.QAFinding{{ID: "flaky-endpoint", New: true}})
	// The finding vanishes for one attempt; its count is retained.
	strikes, struck := advanceQAFindingStrikes(strikes, []agent.QAFinding{{ID: "other", New: true}})
	if struck != nil {
		t.Fatalf("unexpected strike-out: %#v", struck)
	}
	strikes, _ = advanceQAFindingStrikes(strikes, []agent.QAFinding{{ID: "flaky-endpoint", New: true}})
	_, struck = advanceQAFindingStrikes(strikes, []agent.QAFinding{{ID: "flaky-endpoint", New: false}})
	if struck == nil || struck.ID != "flaky-endpoint" {
		t.Fatalf("returning finding did not resume its count: %#v", struck)
	}
}
