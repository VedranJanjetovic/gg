package orchestrator

import (
	"context"
	"errors"
	"github.com/VedranJanjetovic/gg/internal/ci"
	"github.com/VedranJanjetovic/gg/internal/pr"
	"github.com/VedranJanjetovic/gg/internal/state"
	"testing"
	"time"
)

type monitorPR struct {
	values []pr.PullRequestObservation
	calls  int
	err    error
}

func (f *monitorPR) Observe(context.Context, string, string) (pr.PullRequestObservation, error) {
	if f.err != nil {
		return pr.PullRequestObservation{}, f.err
	}
	v := f.values[f.calls]
	f.calls++
	return v, nil
}

type monitorCI struct {
	values []ci.CheckObservation
	calls  int
	err    error
}

func (f *monitorCI) Observe(context.Context, string, string) (ci.CheckObservation, error) {
	if f.err != nil {
		return ci.CheckObservation{}, f.err
	}
	v := f.values[f.calls]
	f.calls++
	return v, nil
}

type monitorRemediator struct{ calls []Remediation }

func (f *monitorRemediator) Remediate(_ context.Context, r Remediation) error {
	f.calls = append(f.calls, r)
	return nil
}

type monitorStore struct {
	updates []state.PRCIMonitorState
	merged  int
}

func (f *monitorStore) UpdatePRCIMonitor(_ context.Context, _ string, v state.PRCIMonitorState) (state.ProjectState, error) {
	f.updates = append(f.updates, v)
	return state.ProjectState{}, nil
}
func (f *monitorStore) MarkPullRequestMerged(context.Context, string, string) (state.ProjectState, error) {
	f.merged++
	return state.ProjectState{}, nil
}
func monitorFixture(p *monitorPR, c *monitorCI, r *monitorRemediator, s PRCIStateStore) *PRCILifecycleMonitor {
	return NewPRCILifecycleMonitor(p, c, r, s)
}
func TestPRCIMonitorMergedIsTerminalAndDoesNotRemediate(t *testing.T) {
	p := &monitorPR{values: []pr.PullRequestObservation{{State: pr.MergeStateMerged, Cursor: "m1"}}}
	c := &monitorCI{}
	r := &monitorRemediator{}
	s := &monitorStore{}
	got, err := monitorFixture(p, c, r, s).Monitor(context.Background(), PRCIRequest{ProjectSlug: "demo", PullRequestURL: "https://github.com/o/r/pull/1", MaxPolls: 3})
	if err != nil || !got.Merged || s.merged != 1 || len(r.calls) != 0 || c.calls != 0 {
		t.Fatalf("got=%+v err=%v merged=%d remediations=%d ci=%d", got, err, s.merged, len(r.calls), c.calls)
	}
}
func TestPRCIMonitorOpenCleanPollsBoundedly(t *testing.T) {
	p := &monitorPR{values: []pr.PullRequestObservation{{State: pr.MergeStateOpen, Cursor: "1"}, {State: pr.MergeStateOpen, Cursor: "2"}}}
	c := &monitorCI{values: []ci.CheckObservation{{Cursor: "c1", Pending: true}, {Cursor: "c2"}}}
	s := &monitorStore{}
	got, err := monitorFixture(p, c, &monitorRemediator{}, s).Monitor(context.Background(), PRCIRequest{ProjectSlug: "demo", PullRequestURL: "pr", MaxPolls: 2})
	if err != nil || got.Polls != 2 || !got.Clean || len(s.updates) != 2 {
		t.Fatalf("got=%+v err=%v updates=%d", got, err, len(s.updates))
	}
}
func TestPRCIMonitorRemediatesConflictAndFailedCIOnce(t *testing.T) {
	p := &monitorPR{values: []pr.PullRequestObservation{{State: pr.MergeStateOpen, MergeConflict: true, Cursor: "same"}, {State: pr.MergeStateOpen, MergeConflict: true, Cursor: "same"}}}
	c := &monitorCI{values: []ci.CheckObservation{{Cursor: "same", Failed: []string{"unit"}}, {Cursor: "same", Failed: []string{"unit"}}}}
	r := &monitorRemediator{}
	got, err := monitorFixture(p, c, r, &monitorStore{}).Monitor(context.Background(), PRCIRequest{ProjectSlug: "demo", PullRequestURL: "pr", MaxPolls: 2})
	if err != nil || got.Remediations != 2 || len(r.calls) != 2 {
		t.Fatalf("got=%+v err=%v calls=%#v", got, err, r.calls)
	}
}
func TestPRCIMonitorRemediationKeysSurviveRestart(t *testing.T) {
	root := t.TempDir()
	store, err := state.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	service := state.NewLifecycleService(store, nil, store.Locker())
	project := state.ProjectState{
		Name: "Demo", Slug: "demo", OriginalGoal: "goal", AcceptanceCriteria: []string{"criterion"},
		PipelineConfig: state.PipelineConfigSnapshot{SchemaVersion: 1, Data: []byte(`{}`)},
		CurrentPhase:   "pipeline", WorktreePath: t.TempDir(), BranchName: "agent/demo",
	}
	if err := service.Create(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Transition(context.Background(), project.Slug, state.StatusRunning, "pipeline", "", nil); err != nil {
		t.Fatal(err)
	}
	p := &monitorPR{values: []pr.PullRequestObservation{{State: pr.MergeStateOpen, MergeConflict: true, Cursor: "pr-cursor"}}}
	c := &monitorCI{values: []ci.CheckObservation{{Cursor: "ci-cursor", Failed: []string{"unit"}}}}
	firstRemediator := &monitorRemediator{}
	first := monitorFixture(p, c, firstRemediator, service)
	got, err := first.Monitor(context.Background(), PRCIRequest{ProjectSlug: project.Slug, PullRequestURL: "pr", MaxPolls: 1})
	if err != nil || got.Remediations != 2 || len(firstRemediator.calls) != 2 {
		t.Fatalf("first monitor got=%+v err=%v calls=%#v", got, err, firstRemediator.calls)
	}
	persisted, err := service.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.PRCIMonitor == nil || len(persisted.PRCIMonitor.RemediationKeys) != 2 {
		t.Fatalf("persisted remediation keys = %#v", persisted.PRCIMonitor)
	}

	restartedStore, err := state.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	restartedService := state.NewLifecycleService(restartedStore, nil, restartedStore.Locker())
	secondRemediator := &monitorRemediator{}
	second := monitorFixture(
		&monitorPR{values: []pr.PullRequestObservation{{State: pr.MergeStateOpen, MergeConflict: true, Cursor: "pr-cursor"}}},
		&monitorCI{values: []ci.CheckObservation{{Cursor: "ci-cursor", Failed: []string{"unit"}}}},
		secondRemediator, restartedService,
	)
	got, err = second.Monitor(context.Background(), PRCIRequest{ProjectSlug: project.Slug, PullRequestURL: "pr", MaxPolls: 1})
	if err != nil || got.Remediations != 0 || len(secondRemediator.calls) != 0 {
		t.Fatalf("restarted monitor got=%+v err=%v calls=%#v", got, err, secondRemediator.calls)
	}
}

func TestPRCIMonitorProviderErrorBackoffIsBounded(t *testing.T) {
	p := &monitorPR{err: errors.New("offline")}
	s := &monitorStore{}
	start := time.Now()
	_, err := monitorFixture(p, &monitorCI{}, &monitorRemediator{}, s).Monitor(context.Background(), PRCIRequest{ProjectSlug: "demo", PullRequestURL: "pr", MaxPolls: 2, Backoff: time.Millisecond})
	if err == nil || time.Since(start) > time.Second || len(s.updates) != 1 {
		t.Fatalf("err=%v elapsed=%s updates=%d", err, time.Since(start), len(s.updates))
	}
}
func TestPRCIMonitorCancellationDuringBackoff(t *testing.T) {
	p := &monitorPR{values: []pr.PullRequestObservation{{State: pr.MergeStateOpen, Cursor: "1"}}}
	c := &monitorCI{values: []ci.CheckObservation{{Cursor: "1", Pending: true}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := monitorFixture(p, c, &monitorRemediator{}, &monitorStore{}).Monitor(ctx, PRCIRequest{ProjectSlug: "demo", PullRequestURL: "pr", MaxPolls: 3, PollInterval: time.Hour})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}
