package state

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/VedranJanjetovic/gg/internal/proof"
)

func TestLifecyclePersistsVerificationContractAndFindings(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewLifecycleService(store, &testClock{now: time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)}, store.Locker())
	project := validProjectState()
	if err := svc.Create(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	contract := VerificationContract{Steps: []VerificationStep{{Name: "tests", Command: "go", Args: []string{"test"}, Adapter: VerificationAdapterGoTest}}, RepairMode: true}
	configured, err := svc.SetVerificationContract(context.Background(), project.Slug, contract, PipelineConfigSnapshot{SchemaVersion: 1, Data: json.RawMessage(`{"verificationSteps":[]}`)})
	if err != nil {
		t.Fatal(err)
	}
	if configured.Verification == nil || !configured.Verification.RepairMode || configured.PipelineConfig.SchemaVersion != 1 {
		t.Fatalf("configured project = %#v", configured)
	}
	baseline := []VerificationFinding{{CheckName: "tests", Identity: "pkg/Test", Reason: "failed", LogPath: ".gg/logs/tests.log"}}
	baselineResults := []VerificationCommandResult{{CheckName: "tests", Command: "go", Status: "failed", Failures: baseline}}
	if _, err := svc.RecordVerificationBaselineReport(context.Background(), project.Slug, baselineResults, baseline); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RecordVerificationResultReport(context.Background(), project.Slug, baselineResults, nil, []VerificationFinding{{CheckName: "tests", Identity: "pkg/Test", Reason: "failed", Classification: "unchanged_baseline"}}, "phase-2", 1, "repair the failing check"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PromoteVerificationIdentity(context.Background(), project.Slug, "pkg/Test"); err != nil {
		t.Fatal(err)
	}
	reloaded, err := store.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.Verification.ParentBaselineCaptured || len(reloaded.Verification.ParentBaseline) != 1 || len(reloaded.Verification.Warnings) != 1 || reloaded.Verification.BoundaryCursor != "phase-2" || reloaded.Verification.RemediationAttempts != 1 || len(reloaded.Verification.PromotedRequiredGreen) != 1 {
		t.Fatalf("reloaded verification state = %#v", reloaded.Verification)
	}
}

func TestLifecycleMigratesLegacyContractWithoutDroppingVerificationHistory(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewLifecycleService(store, nil, store.Locker())
	project := validProjectState()
	if err := svc.Create(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	oldContract := VerificationContract{Steps: []VerificationStep{{Name: "old", Command: "go", Args: []string{"test"}, Adapter: VerificationAdapterGoTest}}}
	if _, err := svc.SetVerificationContract(context.Background(), project.Slug, oldContract, PipelineConfigSnapshot{SchemaVersion: 1, Data: json.RawMessage(`{"legacy":true}`)}); err != nil {
		t.Fatal(err)
	}
	baseline := []VerificationFinding{{CheckName: "old", Identity: "pkg/Test", Reason: "panic", LogPath: "baseline.log"}}
	baselineResults := []VerificationCommandResult{{CheckName: "old", Command: "go", Status: "failed", Failures: baseline}}
	if _, err := svc.RecordVerificationBaselineReport(context.Background(), project.Slug, baselineResults, baseline); err != nil {
		t.Fatal(err)
	}
	current := []VerificationFinding{{CheckName: "old", Identity: "pkg/Test", Reason: "panic", Classification: "changed_reason"}}
	warnings := []VerificationFinding{{CheckName: "old", Identity: "pkg/Other", Reason: "flaky", Classification: "flaky"}}
	results := []VerificationCommandResult{{CheckName: "old", Command: "go", Status: "failed", Failures: current}}
	if _, err := svc.RecordVerificationResultReport(context.Background(), project.Slug, results, current, warnings, "phase-7", 2, "resume with fresh budget"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PromoteVerificationIdentity(context.Background(), project.Slug, "pkg/Test"); err != nil {
		t.Fatal(err)
	}

	newContract := VerificationContract{Steps: []VerificationStep{{Name: "new", Command: "go", Args: []string{"test", "./..."}, Adapter: VerificationAdapterGoTest}}, RepairMode: true}
	got, err := svc.MigrateVerificationContract(context.Background(), project.Slug, newContract, PipelineConfigSnapshot{SchemaVersion: 1, Data: json.RawMessage(`{"migrated":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Verification.ParentBaseline, baseline) ||
		!reflect.DeepEqual(got.Verification.ParentResults, baselineResults) ||
		!reflect.DeepEqual(got.Verification.CurrentFindings, current) ||
		!reflect.DeepEqual(got.Verification.Warnings, warnings) ||
		!reflect.DeepEqual(got.Verification.PromotedRequiredGreen, []string{"pkg/Test"}) ||
		got.Verification.BoundaryCursor != "phase-7" || got.Verification.RemediationAttempts != 2 ||
		got.Verification.NextAction != "resume with fresh budget" {
		t.Fatalf("legacy migration dropped verification history: %#v", got.Verification)
	}
	if !reflect.DeepEqual(got.Verification.PlannedSteps, newContract.Steps) || !got.Verification.RepairMode {
		t.Fatalf("migrated contract = %#v, want %#v", got.Verification, newContract)
	}
}

func TestLifecycleKeepsAnEmptyParentBaselineAcrossRepeatedCapture(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewLifecycleService(store, nil, store.Locker())
	project := validProjectState()
	if err := svc.Create(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	contract := VerificationContract{Steps: []VerificationStep{{Name: "tests", Command: "go", Args: []string{"test"}, Adapter: VerificationAdapterGoTest}}}
	if _, err := svc.SetVerificationContract(context.Background(), project.Slug, contract, PipelineConfigSnapshot{SchemaVersion: 1, Data: json.RawMessage(`{"snapshot":true}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RecordVerificationBaselineReport(context.Background(), project.Slug, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RecordVerificationBaselineReport(context.Background(), project.Slug, nil, []VerificationFinding{{CheckName: "tests", Identity: "pkg/Test"}}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := store.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.Verification.ParentBaselineCaptured || len(reloaded.Verification.ParentBaseline) != 0 {
		t.Fatalf("empty parent baseline was not preserved: %#v", reloaded.Verification)
	}
}

func TestLifecycleBoundsVerificationRemediationAndResetsWithoutReplacingBaseline(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewLifecycleService(store, nil, store.Locker())
	project := validProjectState()
	if err := svc.Create(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	contract := VerificationContract{Steps: []VerificationStep{{Name: "tests", Command: "go", Args: []string{"test"}, Adapter: VerificationAdapterGoTest}}}
	if _, err := svc.SetVerificationContract(context.Background(), project.Slug, contract, PipelineConfigSnapshot{SchemaVersion: 1, Data: json.RawMessage(`{"snapshot":true}`)}); err != nil {
		t.Fatal(err)
	}
	baseline := []VerificationFinding{{CheckName: "tests", Identity: "pkg/Test", Reason: "panic"}}
	if _, err := svc.RecordVerificationBaselineReport(context.Background(), project.Slug, nil, baseline); err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= MaxVerificationRemediationAttempts; attempt++ {
		got, beginErr := svc.BeginVerificationRemediation(context.Background(), project.Slug, "phase-6", "retry")
		if beginErr != nil || got.Verification.RemediationAttempts != attempt {
			t.Fatalf("attempt %d: state=%#v err=%v", attempt, got.Verification, beginErr)
		}
	}
	if _, err := svc.BeginVerificationRemediation(context.Background(), project.Slug, "phase-6", "retry"); err == nil {
		t.Fatal("fourth remediation attempt unexpectedly succeeded")
	}
	reset, err := svc.ResetVerificationRemediation(context.Background(), project.Slug, "resume with fresh budget")
	if err != nil {
		t.Fatal(err)
	}
	if reset.Verification.RemediationAttempts != 0 || !reflect.DeepEqual(reset.Verification.ParentBaseline, baseline) || reset.Verification.BoundaryCursor != "phase-6" {
		t.Fatalf("reset state=%#v, want zero attempts with immutable baseline and cursor", reset.Verification)
	}
}

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time    { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *testClock) Set(now time.Time) { c.mu.Lock(); c.now = now; c.mu.Unlock() }

func TestLifecycleTransitionsTable(t *testing.T) {
	cases := []struct {
		from, to LifecycleStatus
		valid    bool
	}{
		{StatusPending, StatusRunning, true}, {StatusPending, StatusTerminated, true},
		{StatusRunning, StatusStopped, true}, {StatusRunning, StatusFailed, true},
		{StatusRunning, StatusFinished, true}, {StatusRunning, StatusTerminated, true},
		{StatusStopped, StatusRunning, true}, {StatusStopped, StatusTerminated, true},
		{StatusFailed, StatusRunning, true}, {StatusFailed, StatusTerminated, true},
		{StatusFinished, StatusRunning, false}, {StatusTerminated, StatusRunning, false},
		{StatusPending, StatusFinished, false}, {StatusStopped, StatusFinished, false},
	}
	for _, tc := range cases {
		t.Run(string(tc.from)+"-"+string(tc.to), func(t *testing.T) {
			if got := canTransition(tc.from, tc.to); got != tc.valid {
				t.Fatalf("canTransition() = %v, want %v", got, tc.valid)
			}
		})
	}
}

func TestLifecycleCreateTransitionHistoryAndTimestamps(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	clock := &testClock{now: time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)}
	svc := NewLifecycleService(store, clock, store.Locker())
	state := validProjectState()
	state.CreatedAt, state.UpdatedAt, state.StatusChangedAt = time.Time{}, time.Time{}, time.Time{}
	state.PhaseHistory = nil
	if err := svc.Create(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	created, err := svc.Load(context.Background(), state.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != StatusPending || len(created.PhaseHistory) != 1 || created.PhaseHistory[0].Phase != state.CurrentPhase {
		t.Fatalf("created state = %#v", created)
	}
	clock.Set(clock.now.Add(time.Minute))
	got, err := svc.Transition(context.Background(), state.Slug, StatusRunning, "build", "compile", []string{"artifact.log"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusRunning || got.CurrentSubphase != "compile" || len(got.PhaseHistory) != 2 {
		t.Fatalf("running state = %#v", got)
	}
	if !got.StatusChangedAt.Equal(clock.now) || !got.UpdatedAt.Equal(clock.now) || got.PhaseHistory[0].CompletedAt == nil {
		t.Fatalf("timestamps/history not updated: %#v", got)
	}
	if got.WorktreePath != state.WorktreePath || got.BranchName != state.BranchName {
		t.Fatal("transition did not preserve execution metadata")
	}
	clock.Set(clock.now.Add(time.Minute))
	got, err = svc.Transition(context.Background(), state.Slug, StatusFinished, "build", "compile", []string{"artifact.log", "result.json"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.PhaseHistory) != 2 || got.PhaseHistory[1].CompletedAt == nil || len(got.ArtifactPaths) != 2 {
		t.Fatalf("finished history/artifacts = %#v", got)
	}
}

func TestLifecycleInvalidTransitionDoesNotWrite(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	clock := &testClock{now: time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)}
	svc := NewLifecycleService(store, clock, store.Locker())
	state := validProjectState()
	state.PhaseHistory = nil
	if err := svc.Create(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	created, err := svc.Load(context.Background(), state.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Transition(context.Background(), state.Slug, StatusFinished, "", "", nil); err == nil {
		t.Fatal("invalid transition unexpectedly succeeded")
	}
	got, err := svc.Load(context.Background(), state.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusPending || !got.UpdatedAt.Equal(created.UpdatedAt) {
		t.Fatalf("invalid transition changed state: %#v", got)
	}
}

func TestLifecycleCancellationAndClassification(t *testing.T) {
	if !StatusRunning.IsActive() || StatusFinished.IsActive() || !StatusFinished.IsTerminal() || StatusTerminated.IsActive() {
		t.Fatal("unexpected active/terminal classification")
	}
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewLifecycleService(store, &testClock{now: time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)}, store.Locker())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := svc.Create(ctx, validProjectState()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Create() error = %v", err)
	}
}

func TestLifecycleConcurrentTransitionsSerialize(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	clock := &testClock{now: time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)}
	first := NewLifecycleService(store, clock, store.Locker())
	state := validProjectState()
	state.PhaseHistory = nil
	if err := first.Create(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	second := NewLifecycleService(store, clock, store.Locker())
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, svc := range []*LifecycleService{first, second} {
		wg.Add(1)
		go func(svc *LifecycleService) {
			defer wg.Done()
			_, err := svc.Transition(context.Background(), state.Slug, StatusRunning, "build", "compile", nil)
			errs <- err
		}(svc)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := first.Load(context.Background(), state.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusRunning {
		t.Fatalf("final status = %s", got.Status)
	}
}

func TestLifecyclePruneSerializesCleanupWithTransition(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewLifecycleService(store, &testClock{now: time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)}, store.Locker())
	project := validProjectState()
	project.Status = StatusFinished
	if err := svc.Create(context.Background(), project); err != nil {
		t.Fatal(err)
	}

	cleanupStarted := make(chan struct{})
	releaseCleanup := make(chan struct{})
	pruneDone := make(chan error, 1)
	go func() {
		pruneDone <- svc.PruneProject(context.Background(), project.Slug, func(context.Context, ProjectState) error {
			close(cleanupStarted)
			<-releaseCleanup
			return nil
		})
	}()
	<-cleanupStarted

	transitionDone := make(chan error, 1)
	go func() {
		_, err := svc.Transition(context.Background(), project.Slug, StatusRunning, "pipeline", "run", nil)
		transitionDone <- err
	}()
	select {
	case err := <-transitionDone:
		t.Fatalf("transition completed while prune held lock: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseCleanup)
	if err := <-pruneDone; err != nil {
		t.Fatal(err)
	}
	if err := <-transitionDone; err == nil {
		t.Fatal("transition unexpectedly succeeded after prune deleted state")
	}
	if _, err := store.Load(context.Background(), project.Slug); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state load error = %v, want deleted state", err)
	}
}

func TestLifecycleRunReservationSerializesWithPrune(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewLifecycleService(store, &testClock{now: time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)}, store.Locker())
	project := validProjectState()
	project.Status = StatusStopped
	if err := svc.Create(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	validationStarted := make(chan struct{})
	releaseValidation := make(chan struct{})
	reservationDone := make(chan *RunReservation, 1)
	reservationErr := make(chan error, 1)
	go func() {
		_, reservation, err := svc.ReserveRun(context.Background(), project.Slug, func(context.Context, ProjectState) error {
			close(validationStarted)
			<-releaseValidation
			return nil
		})
		reservationDone <- reservation
		reservationErr <- err
	}()
	<-validationStarted
	cleanupCalled := make(chan struct{}, 1)
	pruneDone := make(chan error, 1)
	go func() {
		pruneDone <- svc.PruneProject(context.Background(), project.Slug, func(context.Context, ProjectState) error {
			cleanupCalled <- struct{}{}
			return nil
		})
	}()
	select {
	case <-pruneDone:
		t.Fatal("prune completed while run reservation held project lock")
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseValidation)
	if err := <-reservationErr; err != nil {
		t.Fatal(err)
	}
	if reservation := <-reservationDone; reservation == nil {
		t.Fatal("run reservation is nil")
	}
	if err := <-pruneDone; err != nil {
		t.Fatal(err)
	}
	select {
	case <-cleanupCalled:
		t.Fatal("prune cleaned up a project reserved for running")
	default:
	}
	got, err := store.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusRunning || got.WorktreePath != project.WorktreePath || got.BranchName != project.BranchName {
		t.Fatalf("reserved project = %#v, want running with preserved worktree metadata", got)
	}
}

func TestLifecycleRunReservationRollbackRestoresState(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewLifecycleService(store, &testClock{now: time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)}, store.Locker())
	project := validProjectState()
	project.Status = StatusStopped
	if err := svc.Create(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	reserved, reservation, err := svc.ReserveRun(context.Background(), project.Slug, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reserved.Status != StatusRunning {
		t.Fatalf("reserved status = %s", reserved.Status)
	}
	if err := reservation.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusStopped || got.WorktreePath != project.WorktreePath || got.BranchName != project.BranchName {
		t.Fatalf("rolled back project = %#v", got)
	}
}

func TestLifecycleRunReservationRollbackDoesNotOverwriteSameStatusUpdate(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	clock := &testClock{now: time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)}
	svc := NewLifecycleService(store, clock, store.Locker())
	project := validProjectState()
	project.Status = StatusStopped
	if err := svc.Create(context.Background(), project); err != nil {
		t.Fatal(err)
	}

	_, reservation, err := svc.ReserveRun(context.Background(), project.Slug, nil)
	if err != nil {
		t.Fatal(err)
	}
	clock.Set(clock.now.Add(time.Minute))
	updated, err := svc.Transition(context.Background(), project.Slug, StatusRunning, "implementation", "review", []string{"review.log"})
	if err != nil {
		t.Fatal(err)
	}
	if updated.RunReservationToken != "" {
		t.Fatalf("same-status transition retained reservation token %q", updated.RunReservationToken)
	}

	if err := reservation.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusRunning || got.CurrentPhase != "implementation" || got.CurrentSubphase != "review" {
		t.Fatalf("rollback changed newer running state: %#v", got)
	}
	if len(got.ArtifactPaths) != 1 || got.ArtifactPaths[0] != "review.log" {
		t.Fatalf("rollback changed newer artifacts: %#v", got.ArtifactPaths)
	}
	if len(got.PhaseHistory) != len(updated.PhaseHistory) || got.PhaseHistory[len(got.PhaseHistory)-1].Phase != "implementation" {
		t.Fatalf("rollback changed newer phase history: %#v", got.PhaseHistory)
	}
}

func TestLifecycleActiveReservationRejectsNestedReservationAndRollsBack(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	clock := &testClock{now: time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)}
	svc := NewLifecycleService(store, clock, store.Locker())
	project := validProjectState()
	project.Status = StatusStopped
	if err := svc.Create(context.Background(), project); err != nil {
		t.Fatal(err)
	}

	_, reservationA, err := svc.ReserveRun(context.Background(), project.Slug, nil)
	if err != nil {
		t.Fatal(err)
	}
	clock.Set(clock.now.Add(time.Minute))
	if _, _, err := svc.ReserveRun(context.Background(), project.Slug, nil); err == nil {
		t.Fatal("nested reservation replaced active ownership")
	}
	if err := reservationA.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	afterA, err := store.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if afterA.Status != StatusStopped || afterA.RunReservationToken != "" {
		t.Fatalf("owned reservation rollback did not restore stopped state: %#v", afterA)
	}
}

func TestLifecycleSaveRunningInvalidatesReservationToken(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	clock := &testClock{now: time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)}
	svc := NewLifecycleService(store, clock, store.Locker())
	project := validProjectState()
	project.Status = StatusRunning
	project.RunReservationToken = "caller-supplied-token"
	if err := svc.Save(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if got.RunReservationToken != "" {
		t.Fatalf("public Save retained reservation token %q", got.RunReservationToken)
	}
}

func TestLifecycleRunReservationCannotBeReplacedWhileRunning(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	clock := &testClock{now: time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)}
	svc := NewLifecycleService(store, clock, store.Locker())
	project := validProjectState()
	project.Status = StatusStopped
	if err := svc.Create(context.Background(), project); err != nil {
		t.Fatal(err)
	}

	reserved, reservationA, err := svc.ReserveRun(context.Background(), project.Slug, nil)
	if err != nil {
		t.Fatal(err)
	}
	clock.Set(clock.now.Add(time.Minute))
	if _, _, err := svc.ReserveRun(context.Background(), project.Slug, nil); err == nil {
		t.Fatal("second reservation replaced active ownership")
	}

	if err := reservationA.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusStopped || got.RunReservationToken != "" {
		t.Fatalf("reservation rollback did not restore prior state: reserved=%#v got=%#v", reserved, got)
	}
}

func TestLifecycleRunReservationRejectsTerminalProject(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewLifecycleService(store, &testClock{now: time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)}, store.Locker())
	project := validProjectState()
	project.Status = StatusFinished
	if err := svc.Create(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.ReserveRun(context.Background(), project.Slug, nil); err == nil {
		t.Fatal("terminal project was reserved")
	}
	got, err := store.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusFinished {
		t.Fatalf("terminal project changed to %s", got.Status)
	}
}

func TestLifecycleRecordFailedPhasePreservesRunUntilExplicitTerminalClosure(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	clock := &testClock{now: time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)}
	svc := NewLifecycleService(store, clock, store.Locker())
	project := validProjectState()
	project.Status = StatusRunning
	project.RunReservationToken = "active-reservation"
	if err := svc.Create(context.Background(), project); err != nil {
		t.Fatal(err)
	}

	clock.Set(clock.now.Add(time.Minute))
	got, err := svc.RecordPhase(context.Background(), project.Slug, "build", "compile", StatusFailed, &ExecutionOutcome{ExitCode: 1}, []string{"failure.log"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusRunning {
		t.Fatalf("project status = %s, want running retryable lifecycle", got.Status)
	}
	if got.RunReservationToken != "" {
		t.Fatalf("failed phase retained reservation token %q", got.RunReservationToken)
	}
	if got.StatusChangedAt.Equal(clock.now) {
		t.Fatalf("retryable phase failure changed project status timestamp to %s", got.StatusChangedAt)
	}
	last := got.PhaseHistory[len(got.PhaseHistory)-1]
	if last.Phase != "build" || last.Subphase != "compile" || last.Status != StatusFailed || last.CompletedAt == nil {
		t.Fatalf("failed phase history = %#v", last)
	}
	persisted, err := store.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != StatusRunning || persisted.RunReservationToken != "" {
		t.Fatalf("persisted retryable phase state = %#v", persisted)
	}
	if err := svc.CloseRun(context.Background(), project.Slug, StatusFailed); err != nil {
		t.Fatal(err)
	}
	persisted, err = store.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != StatusFailed {
		t.Fatalf("explicitly closed project status = %s, want failed", persisted.Status)
	}
}

func TestLifecycleRecordPhaseCarriesDeferredChecksIntoProjectHandoff(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	clock := &testClock{now: time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)}
	svc := NewLifecycleService(store, clock, store.Locker())
	project := validProjectState()
	project.Status = StatusRunning
	if err := svc.Create(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	check := proof.DeferredCheck{
		TestLocation: "internal/aws/handler_test.go", CheckName: "TestRemoteFlow",
		FlowScenario: "exercise the deployed API", ExpectedBehavior: "the API persists the request",
		RemoteOnlyReason: "requires AWS credentials", RepositoryEvidence: "config/aws.go uses AWS_ENDPOINT",
		RunInstructions: "run in CI with AWS secrets",
	}
	got, err := svc.RecordPhase(context.Background(), project.Slug, "qa", "", StatusFinished, &ExecutionOutcome{DeferredChecks: []proof.DeferredCheck{check}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.DeferredChecks) != 1 || len(got.PhaseHistory) == 0 || len(got.PhaseHistory[len(got.PhaseHistory)-1].DeferredChecks) != 1 {
		t.Fatalf("deferred handoff = %#v", got)
	}
	if got.PhaseHistory[len(got.PhaseHistory)-1].Outcome == nil || len(got.PhaseHistory[len(got.PhaseHistory)-1].Outcome.DeferredChecks) != 1 {
		t.Fatalf("deferred outcome = %#v", got.PhaseHistory[len(got.PhaseHistory)-1].Outcome)
	}
	reloaded, err := store.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.DeferredChecks) != 1 || reloaded.DeferredChecks[0] != check {
		t.Fatalf("persisted deferred handoff = %#v", reloaded.DeferredChecks)
	}
}

func TestLifecycleStopRequestIsDurableIdempotentAndRejectsStaleRuns(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewLifecycleService(store, &testClock{now: time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)}, store.Locker())
	project := validProjectState()
	if err := svc.Create(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Transition(context.Background(), project.Slug, StatusRunning, "pipeline", "run", nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.BeginRun(context.Background(), project.Slug, "run-1", ""); err != nil {
		t.Fatal(err)
	}
	if err := svc.RequestStop(context.Background(), project.Slug, "old-run"); !errors.Is(err, ErrStaleStopRequest) {
		t.Fatalf("stale stop error = %v", err)
	}
	if err := svc.RequestStop(context.Background(), project.Slug, ""); err != nil {
		t.Fatal(err)
	}
	if err := svc.RequestStop(context.Background(), project.Slug, "run-1"); err != nil {
		t.Fatalf("duplicate stop error = %v", err)
	}
	fresh, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := fresh.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if !persisted.StopRequested || persisted.StopRequestID != "run-1" || persisted.ActiveRunID != "run-1" {
		t.Fatalf("stop request not persisted: %#v", persisted)
	}
	if requested, err := svc.StopRequested(context.Background(), project.Slug, "run-1"); err != nil || !requested {
		t.Fatalf("StopRequested() = %v, %v", requested, err)
	}
	if _, err := svc.Transition(context.Background(), project.Slug, StatusStopped, "pipeline", "run", nil); err != nil {
		t.Fatal(err)
	}
	cleaned, err := fresh.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if cleaned.StopRequested || cleaned.StopRequestID != "" || cleaned.ActiveRunID != "" {
		t.Fatalf("stop request was not cleaned: %#v", cleaned)
	}
}

func TestLifecycleBeginRunRejectsDifferentConcurrentProjectRun(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	svc1 := NewLifecycleService(store, &testClock{now: time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)}, store.Locker())
	svc2 := NewLifecycleService(store, &testClock{now: time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)}, store.Locker())
	project := validProjectState()
	if err := svc1.Create(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	if _, err := svc1.Transition(context.Background(), project.Slug, StatusRunning, "pipeline", "run", nil); err != nil {
		t.Fatal(err)
	}
	if err := svc1.BeginRun(context.Background(), project.Slug, "run-a", ""); err != nil {
		t.Fatal(err)
	}
	if err := svc2.BeginRun(context.Background(), project.Slug, "run-b", ""); !errors.Is(err, ErrRunNotActive) {
		t.Fatalf("second BeginRun() error = %v, want active-run rejection", err)
	}
	got, err := svc1.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if got.ActiveRunID != "run-a" {
		t.Fatalf("active run identity = %q, want run-a", got.ActiveRunID)
	}
}

func TestLifecycleDispatchClaimLinearizesBeforeLaterStop(t *testing.T) {
	root := t.TempDir()
	store1, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	svc1 := NewLifecycleService(store1, nil, store1.Locker())
	project := validProjectState()
	if err := svc1.Create(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	if _, err := svc1.Transition(context.Background(), project.Slug, StatusRunning, "pipeline", "run", nil); err != nil {
		t.Fatal(err)
	}
	if err := svc1.BeginRun(context.Background(), project.Slug, "run-1", ""); err != nil {
		t.Fatal(err)
	}
	if err := svc1.ClaimDispatch(context.Background(), project.Slug, "run-1"); err != nil {
		t.Fatal(err)
	}
	store2, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	svc2 := NewLifecycleService(store2, nil, store2.Locker())
	if err := svc2.RequestStop(context.Background(), project.Slug, ""); err != nil {
		t.Fatal(err)
	}
	got, err := svc1.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if got.DispatchClaimRunID != "run-1" || !got.StopRequested || got.StopRequestID != "run-1" {
		t.Fatalf("claim/stop ordering state = %#v", got)
	}
}

func TestLifecycleRetryableFailurePreservesDispatchOwnershipAndQACursor(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	service := NewLifecycleService(store, nil, store.Locker())
	project := validProjectState()
	if err := service.Create(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Transition(context.Background(), project.Slug, StatusRunning, "pipeline", "run", nil); err != nil {
		t.Fatal(err)
	}
	if err := service.ConfigureOrchestration(context.Background(), project.Slug, 3); err != nil {
		t.Fatal(err)
	}
	if err := service.BeginRun(context.Background(), project.Slug, "run-1", ""); err != nil {
		t.Fatal(err)
	}
	if err := service.ClaimDispatch(context.Background(), project.Slug, "run-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecordPhase(context.Background(), project.Slug, "qa", "", StatusFailed, &ExecutionOutcome{ExitCode: 1}, []string{"qa-report.md"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateQALoopWithFixCursor(context.Background(), project.Slug, 1, "fix", "implementation", []string{"qa-report.md"}); err != nil {
		t.Fatal(err)
	}
	if err := service.ClaimDispatch(context.Background(), project.Slug, "run-1"); err != nil {
		t.Fatalf("retry dispatch claim failed: %v", err)
	}
	reloaded, err := service.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Status != StatusRunning || reloaded.ActiveRunID != "run-1" || reloaded.QACompletedAttempts != 1 || reloaded.QALoopStage != "fix" {
		t.Fatalf("retryable durable state = %#v", reloaded)
	}
	if !reflect.DeepEqual(reloaded.QAFeedbackArtifactPaths, []string{"qa-report.md"}) {
		t.Fatalf("feedback paths = %v", reloaded.QAFeedbackArtifactPaths)
	}
}

func TestRecordPlanReplacesPhasesAndMergesCompletions(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewLifecycleService(store, nil, store.Locker())
	project := validProjectState()
	if err := svc.Create(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	// Planning defines the phase list.
	updated, err := svc.RecordPlan(context.Background(), project.Slug, []string{"Phase 1", "Phase 2"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Plan == nil || !reflect.DeepEqual(updated.Plan.Phases, []string{"Phase 1", "Phase 2"}) {
		t.Fatalf("plan = %#v", updated.Plan)
	}
	// Development marks progress; repeats do not duplicate.
	if _, err := svc.RecordPlan(context.Background(), project.Slug, nil, []string{"Phase 1"}); err != nil {
		t.Fatal(err)
	}
	updated, err = svc.RecordPlan(context.Background(), project.Slug, nil, []string{"Phase 1", "Phase 2"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(updated.Plan.Completed, []string{"Phase 1", "Phase 2"}) {
		t.Fatalf("completed = %#v, want merged without duplicates", updated.Plan.Completed)
	}
	// A planning re-run replaces the phase list but keeps recorded progress.
	updated, err = svc.RecordPlan(context.Background(), project.Slug, []string{"Phase 1", "Phase 2", "Phase 3"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Plan.Phases) != 3 || len(updated.Plan.Completed) != 2 {
		t.Fatalf("plan after re-plan = %#v", updated.Plan)
	}
	// Durable: a fresh load sees the same plan.
	loaded, err := svc.Load(context.Background(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded.Plan, updated.Plan) {
		t.Fatalf("persisted plan = %#v, want %#v", loaded.Plan, updated.Plan)
	}
}

func TestRecoverIfStaleOnlyRecoversDeadOwners(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewLifecycleService(store, nil, store.Locker())
	project := validProjectState()
	project.Status = StatusRunning
	if err := svc.Create(context.Background(), project); err != nil {
		t.Fatal(err)
	}

	// Legacy state (no owner PID) counts as stale and recovers to stopped.
	recovered, changed, err := svc.RecoverIfStale(context.Background(), project.Slug)
	if err != nil || !changed || recovered.Status != StatusStopped {
		t.Fatalf("legacy recovery = %v %v %s", err, changed, recovered.Status)
	}

	// A running project owned by a live process is left untouched.
	live := recovered
	live.Status = StatusRunning
	live.RunOwnerPID = os.Getpid()
	if err := store.Save(context.Background(), live); err != nil {
		t.Fatal(err)
	}
	unchanged, changed, err := svc.RecoverIfStale(context.Background(), project.Slug)
	if err != nil || changed || unchanged.Status != StatusRunning {
		t.Fatalf("live-owner recovery = %v %v %s", err, changed, unchanged.Status)
	}

	// A dead PID recovers. PID 1 is init/launchd: alive but not ours — use an
	// impossible PID instead.
	dead := unchanged
	dead.RunOwnerPID = 1 << 30
	if err := store.Save(context.Background(), dead); err != nil {
		t.Fatal(err)
	}
	recovered, changed, err = svc.RecoverIfStale(context.Background(), project.Slug)
	if err != nil || !changed || recovered.Status != StatusStopped || recovered.RunOwnerPID != 0 {
		t.Fatalf("dead-owner recovery = %v %v %#v", err, changed, recovered.Status)
	}

	// Non-running projects are never touched.
	if _, changed, err := svc.RecoverIfStale(context.Background(), project.Slug); err != nil || changed {
		t.Fatalf("stopped project recovery = %v %v", err, changed)
	}
}

func TestRewindForFeedbackAppendsCriterionAndMovesCursor(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewLifecycleService(store, nil, store.Locker())
	project := validProjectState()
	project.Status = StatusFinished
	if err := svc.Create(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	updated, err := svc.BeginFeedbackLoop(context.Background(), project.Slug, "make it faster")
	if err != nil {
		t.Fatal(err)
	}
	last := updated.AcceptanceCriteria[len(updated.AcceptanceCriteria)-1]
	if last != "User feedback — make it faster" {
		t.Fatalf("criteria = %#v", updated.AcceptanceCriteria)
	}
	if updated.Status != StatusStopped || updated.CurrentPhase != "pipeline" || updated.CurrentSubphase != "" {
		t.Fatalf("cursor = %s %s/%s, want full rerun from pipeline start", updated.Status, updated.CurrentPhase, updated.CurrentSubphase)
	}
	if updated.Interview == nil || updated.Interview.Done {
		t.Fatalf("interview must re-open: %#v", updated.Interview)
	}
}
