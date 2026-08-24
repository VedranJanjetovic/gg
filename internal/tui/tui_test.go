package tui

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/VedranJanjetovic/gg/internal/config"
	"github.com/VedranJanjetovic/gg/internal/pipeline"
	"github.com/VedranJanjetovic/gg/internal/state"
	tea "github.com/charmbracelet/bubbletea"
)

func TestViewSnapshotsCoverLifecycleStates(t *testing.T) {
	snapshot := testSnapshot(t)
	tests := []struct {
		name    string
		project state.ProjectState
		want    string
	}{
		{
			name:    "pending",
			project: testProject(snapshot, state.StatusPending, "pipeline", "", nil),
			want: `gg · Demo project
Status: pending

  ○ Acceptance criteria
  ○ Development
      ○ Implementation
      ○ Testing
      ○ Review
  ○ Rebase
  ○ Test/Document

Progress  ░░░░░░░░░░░░░░░░░░░░░░░░░   0%  0/4 · 4 phases remaining

q quit
Keys: i interactive  c code  t terminal  r resume  q quit
`,
		},
		{
			name: "running",
			project: testProject(snapshot, state.StatusRunning, string(pipeline.PhaseDevelopment), "testing", []state.PhaseRecord{
				{Phase: string(pipeline.PhaseAcceptanceCriteria), Status: state.StatusFinished},
				{Phase: string(pipeline.PhaseDevelopment), Subphase: "implementation", Status: state.StatusFinished},
				{Phase: string(pipeline.PhaseDevelopment), Subphase: "testing", Status: state.StatusRunning},
			}),
			want: `gg · Demo project
Status: running

  ✓ Acceptance criteria
  ⠋ Development
      ✓ Implementation
      ⠋ Testing
      ○ Review
  ○ Rebase
  ○ Test/Document

Progress  ██████░░░░░░░░░░░░░░░░░░░  25%  1/4 · 3 phases remaining

s stop  q quit
Keys: i interactive  c code  t terminal  r resume  s stop  q quit
`,
		},
		{
			name:    "succeeded",
			project: testProject(snapshot, state.StatusFinished, string(pipeline.PhaseTestDocument), "", nil),
			want: `gg · Demo project
Status: succeeded

  ✓ Acceptance criteria
  ✓ Development
      ✓ Implementation
      ✓ Testing
      ✓ Review
  ✓ Rebase
  ✓ Test/Document

Progress  █████████████████████████ 100%  4/4 · 0 phases remaining

q quit
Keys: i interactive  c code  t terminal  r resume  q quit
`,
		},
		{
			name:    "failed",
			project: testProject(snapshot, state.StatusFailed, string(pipeline.PhaseRebase), "", completedThroughDevelopment()),
			want: `gg · Demo project
Status: failed

  ✓ Acceptance criteria
  ✓ Development
      ✓ Implementation
      ✓ Testing
      ✓ Review
  ✗ Rebase (failed)
  ○ Test/Document

Progress  █████████████░░░░░░░░░░░░  50%  2/4 · 2 phases remaining

r resume  q quit
Keys: i interactive  c code  t terminal  r resume  q quit
`,
		},
		{
			name: "stopped",
			project: testProject(snapshot, state.StatusStopped, string(pipeline.PhaseDevelopment), "testing", []state.PhaseRecord{
				{Phase: string(pipeline.PhaseAcceptanceCriteria), Status: state.StatusFinished},
				{Phase: string(pipeline.PhaseDevelopment), Subphase: "implementation", Status: state.StatusFinished},
				{Phase: string(pipeline.PhaseDevelopment), Subphase: "testing", Status: state.StatusStopped},
			}),
			want: `gg · Demo project
Status: stopped

  ✓ Acceptance criteria
  ■ Development (stopped)
      ✓ Implementation
      ■ Testing (stopped)
      ○ Review
  ○ Rebase
  ○ Test/Document

Progress  ██████░░░░░░░░░░░░░░░░░░░  25%  1/4 · 3 phases remaining

Type r to continue pipeline
q quit
Keys: i interactive  c code  t terminal  r resume  q quit
`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model, err := NewModel(context.Background(), test.project, nil, Actions{}, WithColor(false))
			if err != nil {
				t.Fatal(err)
			}
			view := model.View()
			if view != test.want {
				t.Errorf("view snapshot mismatch\nwant:\n%s\ngot:\n%s", test.want, view)
			}
			if test.name == "stopped" && strings.Count(view, "Type r to continue pipeline") != 1 {
				t.Errorf("stopped continuation prompt count != 1:\n%s", view)
			}
		})
	}
}

func TestProjectionUsesPersistedEnabledPhaseOrderAndDevelopmentSubphases(t *testing.T) {
	snapshot := testConfiguredSnapshot(t)
	project := testProject(snapshot, state.StatusRunning, string(pipeline.PhaseDevelopment), "verify", nil)
	model, err := NewModel(context.Background(), project, nil, Actions{}, WithColor(false))
	if err != nil {
		t.Fatal(err)
	}
	phases := model.Phases()
	wantIDs := []string{"acceptance_criteria", "grooming", "development", "rebase", "qa", "test_document"}
	if len(phases) != len(wantIDs) {
		t.Fatalf("phase count = %d, want %d: %#v", len(phases), len(wantIDs), phases)
	}
	for index, want := range wantIDs {
		if phases[index].ID != want {
			t.Fatalf("phase %d = %q, want %q", index, phases[index].ID, want)
		}
	}
	if got := []string{phases[2].Subphases[0].ID, phases[2].Subphases[1].ID}; strings.Join(got, ",") != "build,verify" {
		t.Fatalf("Development subphases = %v", got)
	}
	if strings.Contains(model.View(), "Planning") || strings.Contains(model.View(), "Build checker") {
		t.Fatalf("disabled phases rendered:\n%s", model.View())
	}
}

func TestEmptySnapshotFallbackIsExplicitAndPendingOnly(t *testing.T) {
	empty := state.PipelineConfigSnapshot{SchemaVersion: 1, Data: []byte(`{}`)}
	pending := testProject(empty, state.StatusPending, "pipeline", "", nil)
	if _, err := NewModel(context.Background(), pending, nil, Actions{}); err == nil || !strings.Contains(err.Error(), "explicit pending pipeline") {
		t.Fatalf("pending empty snapshot error = %v", err)
	}
	model, err := NewModel(context.Background(), pending, nil, Actions{}, WithPendingPipeline(DefaultPendingPipeline()), WithColor(false))
	if err != nil {
		t.Fatal(err)
	}
	if len(model.Phases()) != len(pipeline.DefaultPipeline().Phases()) {
		t.Fatalf("fallback phases = %d", len(model.Phases()))
	}
	running := testProject(empty, state.StatusRunning, "pipeline", "", nil)
	if _, err := NewModel(context.Background(), running, nil, Actions{}, WithPendingPipeline(DefaultPendingPipeline())); err == nil || !strings.Contains(err.Error(), "restore project pipeline") {
		t.Fatalf("running empty snapshot error = %v", err)
	}
}

func TestMalformedSnapshotsAndInvalidOptionsFailClearly(t *testing.T) {
	tests := []struct {
		name     string
		snapshot state.PipelineConfigSnapshot
		want     string
	}{
		{name: "wrapper version", snapshot: state.PipelineConfigSnapshot{SchemaVersion: 2, Data: []byte(`{}`)}, want: "unsupported pipeline snapshot wrapper version 2"},
		{name: "malformed JSON", snapshot: state.PipelineConfigSnapshot{SchemaVersion: 1, Data: []byte(`{"schemaVersion":`)}, want: "decode pipeline execution snapshot"},
		{name: "unknown fields", snapshot: state.PipelineConfigSnapshot{SchemaVersion: 1, Data: []byte(`{"schemaVersion":1,"unexpected":true}`)}, want: "unknown field"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			project := testProject(test.snapshot, state.StatusRunning, "pipeline", "", nil)
			if _, err := NewModel(context.Background(), project, nil, Actions{}); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("NewModel error = %v, want %q", err, test.want)
			}
		})
	}

	pending := testProject(state.PipelineConfigSnapshot{SchemaVersion: 1, Data: []byte(`{}`)}, state.StatusPending, "pipeline", "", nil)
	badOrder := PendingPipeline{
		Phases:               []pipeline.PhaseID{pipeline.PhaseDevelopment, pipeline.PhaseAcceptanceCriteria},
		DevelopmentSubphases: pipeline.DevelopmentSubphaseGeneration{},
	}
	if _, err := NewModel(context.Background(), pending, nil, Actions{}, WithPendingPipeline(badOrder)); err == nil || !strings.Contains(err.Error(), "canonical order") {
		t.Fatalf("invalid pending pipeline error = %v", err)
	}
	if _, err := NewModel(context.Background(), testProject(testSnapshot(t), state.StatusPending, "pipeline", "", nil), nil, Actions{}, WithPollInterval(0)); err == nil || !strings.Contains(err.Error(), "poll interval must be positive") {
		t.Fatalf("invalid poll interval error = %v", err)
	}
}

func TestKeyControlsInvokeOnlyContextualActionsAndAlwaysQuit(t *testing.T) {
	snapshot := testSnapshot(t)
	running := testProject(snapshot, state.StatusRunning, string(pipeline.PhaseDevelopment), "testing", nil)
	stops, resumes := 0, 0
	actions := Actions{
		Stop:   func(context.Context) error { stops++; return nil },
		Resume: func(context.Context) error { resumes++; return nil },
	}
	model, err := NewModel(context.Background(), running, nil, actions, WithColor(false))
	if err != nil {
		t.Fatal(err)
	}
	updated, command := model.Update(key('r'))
	if command != nil || resumes != 0 {
		t.Fatal("r was active for a running project")
	}
	updated, command = updated.(Model).Update(key('s'))
	if command == nil {
		t.Fatal("s did not produce a stop command")
	}
	message := command()
	if stops != 1 {
		t.Fatalf("stop calls = %d", stops)
	}
	updated, _ = updated.(Model).Update(message)
	if _, command = updated.(Model).Update(key('q')); command == nil {
		t.Fatal("q did not quit")
	} else if _, ok := command().(tea.QuitMsg); !ok {
		t.Fatalf("q command returned %T", command())
	}
	if _, command = updated.(Model).Update(tea.KeyMsg{Type: tea.KeyCtrlC}); command == nil {
		t.Fatal("ctrl+c did not quit")
	}

	stopped := testProject(snapshot, state.StatusStopped, string(pipeline.PhaseDevelopment), "testing", nil)
	model, err = NewModel(context.Background(), stopped, nil, actions, WithColor(false))
	if err != nil {
		t.Fatal(err)
	}
	_, command = model.Update(key('r'))
	if command == nil {
		t.Fatal("r did not produce a resume command")
	}
	command()
	if resumes != 1 {
		t.Fatalf("resume calls = %d", resumes)
	}
	if _, command = model.Update(key('s')); command != nil {
		t.Fatal("s was active for a stopped project")
	}
}

func TestStartupOwnsForegroundWhilePollingStillEnablesStop(t *testing.T) {
	snapshot := testSnapshot(t)
	pending := testProject(snapshot, state.StatusPending, "pipeline", "", nil)
	stops := 0
	model, err := NewModel(context.Background(), pending, nil, Actions{
		Start: func(context.Context) error { return nil },
		Stop:  func(context.Context) error { stops++; return nil },
	}, WithColor(false))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(model.View(), "Starting pipeline…") || strings.Contains(model.View(), "q quit") {
		t.Fatalf("pending startup help is misleading:\n%s", model.View())
	}
	for _, quitKey := range []tea.KeyMsg{key('q'), {Type: tea.KeyCtrlC}} {
		_, command := model.Update(quitKey)
		if command == nil {
			t.Fatalf("quit key %q did not detach during startup", quitKey.String())
		}
		if _, ok := command().(tea.QuitMsg); !ok {
			t.Fatalf("quit key %q returned %T, want quit", quitKey.String(), command())
		}
	}

	running := testProject(snapshot, state.StatusRunning, string(pipeline.PhaseDevelopment), "testing", nil)
	updated, _ := model.Update(projectLoadedMsg{project: running})
	duringStart := updated.(Model)
	if !strings.Contains(duringStart.View(), "s stop") || !strings.Contains(duringStart.View(), "q detach") {
		t.Fatalf("running startup help did not expose stop and detach:\n%s", duringStart.View())
	}
	// Detaching while Start owns the foreground is allowed: the run keeps
	// executing in-process and the session can re-attach.
	if _, command := duringStart.Update(key('q')); command == nil {
		t.Fatal("q did not detach while Start owned the foreground")
	}
	updated, stopCommand := duringStart.Update(key('s'))
	if stopCommand == nil {
		t.Fatal("running startup did not allow stop")
	}
	stopResult := stopCommand()
	if stops != 1 {
		t.Fatalf("stop calls = %d, want 1", stops)
	}

	updated, _ = updated.(Model).Update(actionResultMsg{kind: actionStart})
	updated, _ = updated.(Model).Update(stopResult)
	finishedStart := updated.(Model)
	if !strings.Contains(finishedStart.View(), "q quit") {
		t.Fatalf("normal help did not return after Start completed:\n%s", finishedStart.View())
	}
	if _, command := finishedStart.Update(key('q')); command == nil {
		t.Fatal("q remained disabled after Start completed")
	}
}

func TestStartupFailureRestoresQuitAndSurfacesError(t *testing.T) {
	snapshot := testSnapshot(t)
	pending := testProject(snapshot, state.StatusPending, "pipeline", "", nil)
	model, err := NewModel(context.Background(), pending, nil, Actions{Start: func(context.Context) error { return nil }}, WithColor(false))
	if err != nil {
		t.Fatal(err)
	}
	startErr := errors.New("startup failed")
	updated, _ := model.Update(actionResultMsg{kind: actionStart, err: startErr})
	failed := updated.(Model)
	if !errors.Is(failed.LastError(), startErr) || !strings.Contains(failed.View(), "Error: startup failed") || !strings.Contains(failed.View(), "q quit") {
		t.Fatalf("startup failure did not restore normal error/help state:\n%s", failed.View())
	}
	for _, quitKey := range []tea.KeyMsg{key('q'), {Type: tea.KeyCtrlC}} {
		if _, command := failed.Update(quitKey); command == nil {
			t.Fatalf("startup failure did not restore %q", quitKey.String())
		}
	}
}

func TestStoppedStartupWaitsForStartToReturnBeforeResume(t *testing.T) {
	snapshot := testSnapshot(t)
	pending := testProject(snapshot, state.StatusPending, "pipeline", "", nil)
	resumes := 0
	model, err := NewModel(context.Background(), pending, nil, Actions{
		Start:  func(context.Context) error { return nil },
		Resume: func(context.Context) error { resumes++; return nil },
	}, WithColor(false))
	if err != nil {
		t.Fatal(err)
	}

	stopped := testProject(snapshot, state.StatusStopped, string(pipeline.PhaseDevelopment), "testing", nil)
	updated, _ := model.Update(projectLoadedMsg{project: stopped})
	duringStop := updated.(Model)
	if !strings.Contains(duringStop.View(), "Stopping pipeline…") || strings.Contains(duringStop.View(), "Type r to continue pipeline") || strings.Contains(duringStop.View(), "q quit") {
		t.Fatalf("start-owned stopped help advertised an unsafe action:\n%s", duringStop.View())
	}
	if _, command := duringStop.Update(key('r')); command != nil || resumes != 0 {
		t.Fatal("resume was accepted while Start was still unwinding")
	}

	updated, _ = duringStop.Update(actionResultMsg{kind: actionStart})
	afterStart := updated.(Model)
	if strings.Count(afterStart.View(), "Type r to continue pipeline") != 1 || !strings.Contains(afterStart.View(), "q quit") {
		t.Fatalf("stopped controls did not return after Start completed:\n%s", afterStart.View())
	}
	_, resumeCommand := afterStart.Update(key('r'))
	if resumeCommand == nil {
		t.Fatal("resume remained disabled after Start completed")
	}
	resumeCommand()
	if resumes != 1 {
		t.Fatalf("resume calls = %d, want 1", resumes)
	}
}

func TestResumeOwnsForegroundUntilItReturns(t *testing.T) {
	snapshot := testSnapshot(t)
	stopped := testProject(snapshot, state.StatusStopped, string(pipeline.PhaseDevelopment), "testing", nil)
	resumes, stops := 0, 0
	model, err := NewModel(context.Background(), stopped, nil, Actions{
		Resume: func(context.Context) error { resumes++; return nil },
		Stop:   func(context.Context) error { stops++; return nil },
	}, WithColor(false))
	if err != nil {
		t.Fatal(err)
	}

	updated, resumeCommand := model.Update(key('r'))
	if resumeCommand == nil {
		t.Fatal("r did not start Resume")
	}
	duringResume := updated.(Model)
	if !strings.Contains(duringResume.View(), "Continuing pipeline…") || strings.Contains(duringResume.View(), "Type r to continue pipeline") || strings.Contains(duringResume.View(), "q quit") {
		t.Fatalf("in-flight Resume advertised an unsafe action:\n%s", duringResume.View())
	}
	if _, duplicate := duringResume.Update(key('r')); duplicate != nil {
		t.Fatal("duplicate Resume was accepted")
	}
	for _, quitKey := range []tea.KeyMsg{key('q'), {Type: tea.KeyCtrlC}} {
		_, command := duringResume.Update(quitKey)
		if command == nil {
			t.Fatalf("quit key %q did not detach during Resume", quitKey.String())
		}
		if _, ok := command().(tea.QuitMsg); !ok {
			t.Fatalf("quit key %q returned %T, want quit", quitKey.String(), command())
		}
	}
	resumeResult := resumeCommand()
	if resumes != 1 {
		t.Fatalf("resume calls = %d, want 1", resumes)
	}

	running := testProject(snapshot, state.StatusRunning, string(pipeline.PhaseDevelopment), "testing", nil)
	updated, _ = duringResume.Update(projectLoadedMsg{project: running})
	runningResume := updated.(Model)
	if !strings.Contains(runningResume.View(), "s stop") || strings.Contains(runningResume.View(), "q quit") || strings.Contains(runningResume.View(), "Continuing pipeline…") {
		t.Fatalf("running Resume did not expose only stop:\n%s", runningResume.View())
	}
	updated, stopCommand := runningResume.Update(key('s'))
	if stopCommand == nil {
		t.Fatal("running Resume did not allow stop")
	}
	stopResult := stopCommand()
	if stops != 1 {
		t.Fatalf("stop calls = %d, want 1", stops)
	}

	updated, _ = updated.(Model).Update(resumeResult)
	updated, _ = updated.(Model).Update(stopResult)
	afterResume := updated.(Model)
	if !strings.Contains(afterResume.View(), "s stop  q quit") {
		t.Fatalf("normal running controls did not return after Resume:\n%s", afterResume.View())
	}
	if _, command := afterResume.Update(key('q')); command == nil {
		t.Fatal("q remained disabled after Resume returned")
	}
}

func TestResumeFailureRestoresStoppedControlsAndSurfacesError(t *testing.T) {
	snapshot := testSnapshot(t)
	stopped := testProject(snapshot, state.StatusStopped, string(pipeline.PhaseDevelopment), "testing", nil)
	resumeErr := errors.New("resume failed")
	model, err := NewModel(context.Background(), stopped, nil, Actions{Resume: func(context.Context) error { return resumeErr }}, WithColor(false))
	if err != nil {
		t.Fatal(err)
	}
	updated, command := model.Update(key('r'))
	if command == nil {
		t.Fatal("r did not start Resume")
	}
	updated, _ = updated.(Model).Update(command())
	failed := updated.(Model)
	if !errors.Is(failed.LastError(), resumeErr) || !strings.Contains(failed.View(), "Error: resume failed") || strings.Count(failed.View(), "Type r to continue pipeline") != 1 || !strings.Contains(failed.View(), "q quit") {
		t.Fatalf("Resume failure did not restore stopped controls/error:\n%s", failed.View())
	}
	for _, quitKey := range []tea.KeyMsg{key('q'), {Type: tea.KeyCtrlC}} {
		if _, quit := failed.Update(quitKey); quit == nil {
			t.Fatalf("Resume failure did not restore %q", quitKey.String())
		}
	}
}

func TestStopOwnsActionUntilItReturns(t *testing.T) {
	snapshot := testSnapshot(t)
	running := testProject(snapshot, state.StatusRunning, string(pipeline.PhaseDevelopment), "testing", nil)
	stops, resumes := 0, 0
	model, err := NewModel(context.Background(), running, nil, Actions{
		Stop:   func(context.Context) error { stops++; return nil },
		Resume: func(context.Context) error { resumes++; return nil },
	}, WithColor(false))
	if err != nil {
		t.Fatal(err)
	}

	updated, stopCommand := model.Update(key('s'))
	if stopCommand == nil {
		t.Fatal("s did not start Stop")
	}
	duringStop := updated.(Model)
	if !strings.Contains(duringStop.View(), "Stopping pipeline…") || strings.Contains(duringStop.View(), "s stop") || strings.Contains(duringStop.View(), "q quit") {
		t.Fatalf("in-flight Stop advertised an unsafe action:\n%s", duringStop.View())
	}
	if _, duplicate := duringStop.Update(key('s')); duplicate != nil {
		t.Fatal("duplicate Stop was accepted")
	}
	for _, quitKey := range []tea.KeyMsg{key('q'), {Type: tea.KeyCtrlC}} {
		_, command := duringStop.Update(quitKey)
		if command == nil {
			t.Fatalf("quit key %q did not detach during Stop", quitKey.String())
		}
		if _, ok := command().(tea.QuitMsg); !ok {
			t.Fatalf("quit key %q returned %T, want quit", quitKey.String(), command())
		}
	}
	stopResult := stopCommand()
	if stops != 1 {
		t.Fatalf("stop calls = %d, want 1", stops)
	}

	stopped := testProject(snapshot, state.StatusStopped, string(pipeline.PhaseDevelopment), "testing", nil)
	updated, _ = duringStop.Update(projectLoadedMsg{project: stopped})
	stoppedDuringAction := updated.(Model)
	if !strings.Contains(stoppedDuringAction.View(), "Stopping pipeline…") || strings.Contains(stoppedDuringAction.View(), "Type r to continue pipeline") {
		t.Fatalf("persisted stopped state became actionable before Stop returned:\n%s", stoppedDuringAction.View())
	}
	if _, command := stoppedDuringAction.Update(key('r')); command != nil || resumes != 0 {
		t.Fatal("Resume was accepted while Stop still owned the action")
	}

	updated, _ = stoppedDuringAction.Update(stopResult)
	afterStop := updated.(Model)
	if strings.Count(afterStop.View(), "Type r to continue pipeline") != 1 || !strings.Contains(afterStop.View(), "q quit") {
		t.Fatalf("stopped controls did not return after Stop completed:\n%s", afterStop.View())
	}
	_, resumeCommand := afterStop.Update(key('r'))
	if resumeCommand == nil {
		t.Fatal("Resume remained disabled after Stop returned")
	}
	resumeCommand()
	if resumes != 1 {
		t.Fatalf("resume calls = %d, want 1", resumes)
	}
}

func TestLifecycleActionsAreDeduplicatedAndFailuresSurface(t *testing.T) {
	snapshot := testSnapshot(t)
	tests := []struct {
		name    string
		status  state.LifecycleStatus
		key     rune
		actions func(*int, error) Actions
	}{
		{
			name: "stop", status: state.StatusRunning, key: 's',
			actions: func(calls *int, actionErr error) Actions {
				return Actions{Stop: func(context.Context) error { *calls++; return actionErr }}
			},
		},
		{
			name: "resume", status: state.StatusStopped, key: 'r',
			actions: func(calls *int, actionErr error) Actions {
				return Actions{Resume: func(context.Context) error { *calls++; return actionErr }}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actionErr := errors.New(test.name + " failed")
			calls := 0
			project := testProject(snapshot, test.status, string(pipeline.PhaseDevelopment), "testing", nil)
			model, err := NewModel(context.Background(), project, nil, test.actions(&calls, actionErr), WithColor(false))
			if err != nil {
				t.Fatal(err)
			}
			pending, command := model.Update(key(test.key))
			if command == nil {
				t.Fatalf("%c did not start %s", test.key, test.name)
			}
			if _, duplicate := pending.(Model).Update(key(test.key)); duplicate != nil {
				t.Fatalf("%s action was accepted twice while pending", test.name)
			}
			result := command()
			if calls != 1 {
				t.Fatalf("%s calls = %d, want 1", test.name, calls)
			}
			updated, _ := pending.(Model).Update(result)
			if !errors.Is(updated.(Model).LastError(), actionErr) || !strings.Contains(updated.(Model).View(), "Error: "+actionErr.Error()) {
				t.Fatalf("%s error was not surfaced:\n%s", test.name, updated.(Model).View())
			}
			if _, retry := updated.(Model).Update(key(test.key)); retry == nil {
				t.Fatalf("%s could not be retried after failure", test.name)
			}
		})
	}
}

func TestCompletedFailedAndStoppedViewsWaitForQuit(t *testing.T) {
	snapshot := testSnapshot(t)
	for _, status := range []state.LifecycleStatus{state.StatusFinished, state.StatusFailed, state.StatusStopped} {
		t.Run(string(status), func(t *testing.T) {
			project := testProject(snapshot, status, string(pipeline.PhaseRebase), "", nil)
			model, err := NewModel(context.Background(), project, nil, Actions{}, WithColor(false))
			if err != nil {
				t.Fatal(err)
			}
			updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
			if command != nil || updated.(Model).Project().Status != status {
				t.Fatalf("%s view exited or changed without q", status)
			}
			for _, quitKey := range []tea.KeyMsg{key('q'), {Type: tea.KeyCtrlC}} {
				_, command = updated.(Model).Update(quitKey)
				if command == nil {
					t.Fatalf("%s did not quit on %q", status, quitKey.String())
				}
				if _, ok := command().(tea.QuitMsg); !ok {
					t.Fatalf("%s quit command returned %T", status, command())
				}
			}
		})
	}
}

func TestPollingIsBoundedAndTestableWithoutSleeping(t *testing.T) {
	snapshot := testSnapshot(t)
	initial := testProject(snapshot, state.StatusRunning, string(pipeline.PhaseDevelopment), "testing", nil)
	latest := testProject(snapshot, state.StatusStopped, string(pipeline.PhaseDevelopment), "testing", nil)
	loads := 0
	var intervals []time.Duration
	scheduler := func(interval time.Duration, _ func(time.Time) tea.Msg) tea.Cmd {
		intervals = append(intervals, interval)
		return func() tea.Msg { return pollTickMsg{} }
	}
	model, err := NewModel(context.Background(), initial, func(context.Context) (state.ProjectState, error) {
		loads++
		return latest, nil
	}, Actions{}, withPollScheduler(scheduler), WithColor(false))
	if err != nil {
		t.Fatal(err)
	}
	model.Init()
	if len(intervals) != 1 || intervals[0] != DefaultPollInterval {
		t.Fatalf("initial poll intervals = %v", intervals)
	}
	updated, command := model.Update(pollTickMsg{})
	if command == nil {
		t.Fatal("poll tick did not request a load")
	}
	loaded := command()
	updated, command = updated.(Model).Update(loaded)
	if loads != 1 || updated.(Model).Project().Status != state.StatusStopped {
		t.Fatalf("loads=%d project=%#v", loads, updated.(Model).Project())
	}
	if command == nil || len(intervals) != 2 {
		t.Fatalf("next poll not scheduled once: intervals=%v", intervals)
	}
}

func TestWriteStatusLoadsOnceWithoutANSIOrControls(t *testing.T) {
	snapshot := testSnapshot(t)
	project := testProject(snapshot, state.StatusRunning, string(pipeline.PhaseDevelopment), "testing", nil)
	loads := 0
	var output bytes.Buffer
	err := WriteStatus(context.Background(), &output, project, func(context.Context) (state.ProjectState, error) {
		loads++
		return project, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if loads != 1 {
		t.Fatalf("loads = %d, want 1", loads)
	}
	view := output.String()
	if strings.Contains(view, "\x1b[") || strings.Contains(view, "q quit") || strings.Contains(view, "s stop") {
		t.Fatalf("non-interactive output contains ANSI/controls:\n%s", view)
	}
	want := `gg · Demo project
Status: running

  ○ Acceptance criteria
  ▶ Development
      ○ Implementation
      ▶ Testing
      ○ Review
  ○ Rebase
  ○ Test/Document

Progress  ░░░░░░░░░░░░░░░░░░░░░░░░░   0%  0/4 · 4 phases remaining
`
	if view != want {
		t.Fatalf("non-interactive snapshot mismatch\nwant:\n%s\ngot:\n%s", want, view)
	}
}

func TestRefreshErrorIsVisibleAndKeepsLastGoodState(t *testing.T) {
	snapshot := testSnapshot(t)
	project := testProject(snapshot, state.StatusRunning, string(pipeline.PhaseDevelopment), "testing", nil)
	var intervals []time.Duration
	scheduler := func(interval time.Duration, _ func(time.Time) tea.Msg) tea.Cmd {
		intervals = append(intervals, interval)
		return func() tea.Msg { return pollTickMsg{} }
	}
	model, err := NewModel(context.Background(), project, func(context.Context) (state.ProjectState, error) {
		return state.ProjectState{}, errors.New("state unavailable")
	}, Actions{}, WithColor(false), withPollScheduler(scheduler))
	if err != nil {
		t.Fatal(err)
	}
	updated, nextPoll := model.Update(projectLoadedMsg{err: errors.New("state unavailable")})
	got := updated.(Model)
	if got.Project().Status != state.StatusRunning || !strings.Contains(got.View(), "Error: refresh project: state unavailable") {
		t.Fatalf("refresh error model:\n%s", got.View())
	}
	if nextPoll == nil || len(intervals) != 1 || intervals[0] != DefaultPollInterval {
		t.Fatalf("refresh error did not schedule one bounded retry: %v", intervals)
	}
}

func TestNonInteractiveRunStartsPendingProjectThenLoadsStatusOnce(t *testing.T) {
	snapshot := testSnapshot(t)
	initial := testProject(snapshot, state.StatusPending, "pipeline", "", nil)
	latest := testProject(snapshot, state.StatusFinished, string(pipeline.PhaseTestDocument), "", nil)
	starts, loads := 0, 0
	var output bytes.Buffer
	err := Run(context.Background(), initial, func(context.Context) (state.ProjectState, error) {
		loads++
		return latest, nil
	}, Actions{Start: func(context.Context) error {
		starts++
		return nil
	}}, bytes.NewBuffer(nil), &output)
	if err != nil {
		t.Fatal(err)
	}
	if starts != 1 || loads != 1 {
		t.Fatalf("starts=%d loads=%d, want one each", starts, loads)
	}
	if !strings.Contains(output.String(), "Status: succeeded") || strings.Contains(output.String(), "q quit") || strings.Contains(output.String(), "\x1b[") {
		t.Fatalf("non-interactive run output:\n%s", output.String())
	}
}

func TestNonInteractiveStoppedProjectResumesOnceBeforeStatusLoad(t *testing.T) {
	snapshot := testSnapshot(t)
	initial := testProject(snapshot, state.StatusStopped, string(pipeline.PhaseDevelopment), "testing", nil)
	latest := initial
	latest.Status = state.StatusRunning
	resumes, loads := 0, 0
	var output bytes.Buffer
	err := Run(context.Background(), initial, func(context.Context) (state.ProjectState, error) {
		loads++
		return latest, nil
	}, Actions{Resume: func(context.Context) error {
		resumes++
		latest.Status = state.StatusFinished
		latest.CurrentPhase = string(pipeline.PhaseTestDocument)
		return nil
	}}, bytes.NewBuffer(nil), &output)
	if err != nil {
		t.Fatal(err)
	}
	if resumes != 1 || loads != 1 {
		t.Fatalf("resume/load calls = %d/%d, want one each", resumes, loads)
	}
	if !strings.Contains(output.String(), "Status: succeeded") {
		t.Fatalf("non-TTY resumed output = %q", output.String())
	}
}

func TestNonInteractiveRunReturnsStartAndLoadErrors(t *testing.T) {
	snapshot := testSnapshot(t)
	initial := testProject(snapshot, state.StatusPending, "pipeline", "", nil)
	startErr := errors.New("cannot start")
	loads := 0
	var output bytes.Buffer
	err := Run(context.Background(), initial, func(context.Context) (state.ProjectState, error) {
		loads++
		return initial, nil
	}, Actions{Start: func(context.Context) error { return startErr }}, bytes.NewBuffer(nil), &output)
	if !errors.Is(err, startErr) || !strings.Contains(err.Error(), "start project") {
		t.Fatalf("start error = %v", err)
	}
	if loads != 0 || output.Len() != 0 {
		t.Fatalf("start failure loads=%d output=%q", loads, output.String())
	}

	loadErr := errors.New("cannot load")
	running := testProject(snapshot, state.StatusRunning, string(pipeline.PhaseDevelopment), "testing", nil)
	err = Run(context.Background(), running, func(context.Context) (state.ProjectState, error) {
		return state.ProjectState{}, loadErr
	}, Actions{}, bytes.NewBuffer(nil), &output)
	if !errors.Is(err, loadErr) || !strings.Contains(err.Error(), "load project status") {
		t.Fatalf("load error = %v", err)
	}
}

func TestToolKeysDispatchActionsAndPropagateErrors(t *testing.T) {
	snapshot := testSnapshot(t)
	project := testProject(snapshot, state.StatusRunning, string(pipeline.PhaseDevelopment), "testing", nil)
	codeErr := errors.New("code unavailable")
	terminalErr := errors.New("terminal unavailable")
	codeCalls, terminalCalls := 0, 0
	model, err := NewModel(context.Background(), project, nil, Actions{
		OpenCode:     func(context.Context) error { codeCalls++; return codeErr },
		OpenTerminal: func(context.Context) error { terminalCalls++; return terminalErr },
	}, WithColor(false))
	if err != nil {
		t.Fatal(err)
	}

	updated, codeCommand := model.Update(key('c'))
	if codeCommand == nil || codeCalls != 0 {
		t.Fatalf("c command=%v calls=%d, want pending command and deferred action", codeCommand != nil, codeCalls)
	}
	pendingCode := updated.(Model)
	if _, duplicate := pendingCode.Update(key('t')); duplicate != nil {
		t.Fatal("terminal action started while code action was pending")
	}
	updated, _ = pendingCode.Update(codeCommand())
	if codeCalls != 1 || !errors.Is(updated.(Model).LastError(), codeErr) {
		t.Fatalf("code calls=%d error=%v, want one call and propagated error", codeCalls, updated.(Model).LastError())
	}
	if _, terminalCommand := updated.(Model).Update(key('t')); terminalCommand == nil {
		t.Fatal("t did not dispatch after code action completed")
	} else {
		pendingTerminal := updated.(Model)
		updated, _ = pendingTerminal.Update(terminalCommand())
	}
	if terminalCalls != 1 || !errors.Is(updated.(Model).LastError(), terminalErr) {
		t.Fatalf("terminal calls=%d error=%v, want one call and propagated error", terminalCalls, updated.(Model).LastError())
	}
}

func TestInteractiveLegendIsAbsentFromStatusOutput(t *testing.T) {
	snapshot := testSnapshot(t)
	project := testProject(snapshot, state.StatusRunning, string(pipeline.PhaseDevelopment), "testing", nil)
	model, err := NewModel(context.Background(), project, nil, Actions{}, WithColor(false))
	if err != nil {
		t.Fatal(err)
	}
	legend := "Keys: i interactive  c code  t terminal  r resume  s stop  q quit"
	if !strings.Contains(model.View(), legend) {
		t.Fatalf("interactive view missing legend:\n%s", model.View())
	}
	if strings.Contains(model.statusView(), legend) {
		t.Fatalf("status view contains interactive legend:\n%s", model.statusView())
	}
}

func key(value rune) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{value}} }

func testSnapshot(t *testing.T) state.PipelineConfigSnapshot {
	t.Helper()
	settings := config.AgentSettings{Agent: config.AgentCodex, Model: "gpt-5", Effort: config.EffortMedium}
	resolved := config.ResolvedConfig{Defaults: settings, Phases: make(map[config.Phase]config.ResolvedPhase)}
	for _, phase := range config.RemovablePhases() {
		resolved.Phases[phase] = config.ResolvedPhase{Enabled: false, AgentSettings: settings}
	}
	plan, err := pipeline.Resolve(pipeline.DefaultPipeline(), resolved)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := pipeline.SnapshotExecution(plan, pipeline.DevelopmentSubphaseGeneration{}, 3)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func testConfiguredSnapshot(t *testing.T) state.PipelineConfigSnapshot {
	t.Helper()
	settings := config.AgentSettings{Agent: config.AgentCodex, Model: "gpt-5", Effort: config.EffortMedium}
	resolved := config.ResolvedConfig{Defaults: settings, Phases: make(map[config.Phase]config.ResolvedPhase)}
	for _, phase := range config.RemovablePhases() {
		resolved.Phases[phase] = config.ResolvedPhase{Enabled: phase == config.PhaseGrooming || phase == config.PhaseQA, AgentSettings: settings}
	}
	plan, err := pipeline.Resolve(pipeline.DefaultPipeline(), resolved)
	if err != nil {
		t.Fatal(err)
	}
	generation := pipeline.DevelopmentSubphaseGeneration{
		Mode: pipeline.DevelopmentSubphasesOverride,
		Subphases: []pipeline.DevelopmentSubphaseDefinition{
			{ID: "build", DisplayName: "Build"},
			{ID: "verify", DisplayName: "Verify"},
		},
	}
	snapshot, err := pipeline.SnapshotExecution(plan, generation, 3)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func testProject(snapshot state.PipelineConfigSnapshot, status state.LifecycleStatus, phase, subphase string, history []state.PhaseRecord) state.ProjectState {
	return state.ProjectState{
		Name: "Demo project", Slug: "demo-project", PipelineConfig: snapshot,
		Status: status, CurrentPhase: phase, CurrentSubphase: subphase,
		PhaseHistory: history,
	}
}

func completedThroughDevelopment() []state.PhaseRecord {
	return []state.PhaseRecord{
		{Phase: string(pipeline.PhaseAcceptanceCriteria), Status: state.StatusFinished},
		{Phase: string(pipeline.PhaseDevelopment), Subphase: "implementation", Status: state.StatusFinished},
		{Phase: string(pipeline.PhaseDevelopment), Subphase: "testing", Status: state.StatusFinished},
		{Phase: string(pipeline.PhaseDevelopment), Subphase: "review", Status: state.StatusFinished},
		{Phase: string(pipeline.PhaseRebase), Status: state.StatusFailed},
	}
}

func TestProjectModelBackReturnsWithoutInvokingLifecycleAction(t *testing.T) {
	project := testProject(testSnapshot(t), state.StatusRunning, string(pipeline.PhaseDevelopment), "testing", nil)
	model, err := NewModel(context.Background(), project, nil, Actions{}, WithColor(false))
	if err != nil {
		t.Fatal(err)
	}
	_, command := model.Update(key('b'))
	if command == nil {
		t.Fatal("b did not return from project view")
	}
	if _, ok := command().(tea.QuitMsg); !ok {
		t.Fatalf("b command returned %T", command())
	}
}

func TestViewShowsGroomingInterviewStep(t *testing.T) {
	snapshot := testSnapshot(t)
	project := testProject(snapshot, state.StatusPending, "pipeline", "", nil)
	project.Interview = &state.InterviewState{Pending: []string{"Anything?"}}
	model, err := NewModel(context.Background(), project, nil, Actions{}, WithColor(false), WithGroomingPending(true))
	if err != nil {
		t.Fatal(err)
	}
	view := model.View()
	if !strings.Contains(view, "● Grooming interview — waiting for your answers (g)") {
		t.Fatalf("pending interview step missing from view:\n%s", view)
	}
	if !strings.Contains(view, "g answer questions") {
		t.Fatalf("g key hint missing from view:\n%s", view)
	}

	project.Interview = &state.InterviewState{Done: true}
	model, err = NewModel(context.Background(), project, nil, Actions{}, WithColor(false))
	if err != nil {
		t.Fatal(err)
	}
	view = model.View()
	if !strings.Contains(view, "✓ Grooming interview (questions answered)") {
		t.Fatalf("finished interview step missing from view:\n%s", view)
	}
}

func TestViewAnnotatesDevelopmentWithPlanProgress(t *testing.T) {
	snapshot := testSnapshot(t)
	project := testProject(snapshot, state.StatusRunning, "development", "implementation", nil)
	project.Plan = &state.PlanState{
		Phases:    []string{"Phase 1: core loop", "Phase 2: entities", "Phase 3: polish"},
		Completed: []string{"Phase 1: core loop"},
	}
	model, err := NewModel(context.Background(), project, nil, Actions{}, WithColor(false))
	if err != nil {
		t.Fatal(err)
	}
	view := model.View()
	if !strings.Contains(view, "Development (phase 2/3 — Phase 2: entities)") {
		t.Fatalf("development line missing plan progress:\n%s", view)
	}
	if strings.Contains(view, "Plan (") {
		t.Fatalf("verbose plan rows must be replaced by the header annotation:\n%s", view)
	}

	project.Plan.Completed = project.Plan.Phases
	model, err = NewModel(context.Background(), project, nil, Actions{}, WithColor(false))
	if err != nil {
		t.Fatal(err)
	}
	if view := model.View(); !strings.Contains(view, "Development (phase 3/3)") {
		t.Fatalf("completed plan annotation missing:\n%s", view)
	}
}

func TestViewShowsTokenTotalsAndDetailToggle(t *testing.T) {
	snapshot := testSnapshot(t)
	project := testProject(snapshot, state.StatusRunning, "development", "implementation", nil)
	project.PhaseHistory = append(project.PhaseHistory,
		state.PhaseRecord{Phase: "grooming", Status: state.StatusFinished, Outcome: &state.ExecutionOutcome{TokensUsed: 37452, CostUSD: 0.42}},
		state.PhaseRecord{Phase: "development", Subphase: "implementation", Status: state.StatusFailed, Outcome: &state.ExecutionOutcome{TokensUsed: 41228}},
		state.PhaseRecord{Phase: "development", Subphase: "implementation", Status: state.StatusFinished, Outcome: &state.ExecutionOutcome{TokensUsed: 1000}},
	)
	model, err := NewModel(context.Background(), project, nil, Actions{}, WithColor(false))
	if err != nil {
		t.Fatal(err)
	}
	view := model.View()
	if !strings.Contains(view, "Tokens: 79,680") || !strings.Contains(view, "$0.42") {
		t.Fatalf("view missing token total or reported cost:\n%s", view)
	}
	if strings.Contains(view, "grooming") && strings.Contains(view, "(2 runs)") {
		t.Fatalf("detail shown before toggling:\n%s", view)
	}
	toggled, _ := model.Update(key('d'))
	view = toggled.(Model).View()
	if !strings.Contains(view, "37,452") || !strings.Contains(view, "42,228") || !strings.Contains(view, "(2 runs)") {
		t.Fatalf("token detail missing after d:\n%s", view)
	}
	if strings.Count(view, "$0.42") != 2 {
		t.Fatalf("detail must show the grooming run's cost alongside the total:\n%s", view)
	}
	hidden, _ := toggled.(Model).Update(key('d'))
	if strings.Contains(hidden.(Model).View(), "(2 runs)") {
		t.Fatal("d did not toggle the detail off")
	}
}

func TestFailedPhaseViewShowsPersistedFailureReason(t *testing.T) {
	history := []state.PhaseRecord{
		{Phase: string(pipeline.PhaseAcceptanceCriteria), Status: state.StatusFailed, Outcome: &state.ExecutionOutcome{ExitCode: 1, Error: "exit status 1\nagent error: There's an issue with the selected model (global-model)."}},
	}
	project := testProject(testSnapshot(t), state.StatusFailed, string(pipeline.PhaseAcceptanceCriteria), "", history)
	model, err := NewModel(context.Background(), project, nil, Actions{}, WithColor(false))
	if err != nil {
		t.Fatal(err)
	}
	view := model.View()
	if !strings.Contains(view, "selected model (global-model)") {
		t.Fatalf("failed phase view does not surface the failure reason:\n%s", view)
	}
}

func TestSkipRequiresNamedConfirmationAndStartsOnlyAfterConfirmation(t *testing.T) {
	whenSkipped := 0
	project := testProject(testSnapshot(t), state.StatusFailed, string(pipeline.PhaseQA), "", []state.PhaseRecord{{
		Phase: string(pipeline.PhaseQA), Status: state.StatusFailed, OccurrenceID: "qa-1",
		CompletedAt: ptrTime(time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)),
		Outcome:     &state.ExecutionOutcome{Error: "remote check failed"},
	}})
	model, err := NewModel(context.Background(), project, nil, Actions{
		Skip: func(context.Context) error { whenSkipped++; return nil }, SkipAvailable: true, SkipLabel: "QA",
	}, WithColor(false))
	if err != nil {
		t.Fatal(err)
	}
	updated, command := model.Update(key('s'))
	model = updated.(Model)
	if command != nil || !model.skipConfirm || whenSkipped != 0 {
		t.Fatalf("skip opened confirmation=%t command=%v calls=%d", model.skipConfirm, command, whenSkipped)
	}
	if view := model.View(); !strings.Contains(view, "Confirm skip of QA?") || !strings.Contains(view, "y/Enter confirm") || strings.Contains(view, "s skip") {
		t.Fatalf("confirmation view missing exact action:\n%s", view)
	}
	updated, command = model.Update(key('n'))
	model = updated.(Model)
	if command != nil || model.skipConfirm || whenSkipped != 0 || !strings.Contains(model.View(), "Skip cancelled.") {
		t.Fatalf("cancel state = %#v command=%v view=%q", model, command, model.View())
	}
	updated, _ = model.Update(key('s'))
	model = updated.(Model)
	updated, command = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if command == nil || !model.skipPending {
		t.Fatalf("confirm did not start skip: pending=%t command=%v", model.skipPending, command)
	}
	message := command()
	if whenSkipped != 1 {
		t.Fatalf("skip calls = %d, want 1", whenSkipped)
	}
	updated, _ = model.Update(message)
	model = updated.(Model)
	if model.skipPending || !strings.Contains(model.View(), "continuation started") {
		t.Fatalf("completed skip state = %#v view=%q", model, model.View())
	}
	updated, command = model.Update(key('s'))
	model = updated.(Model)
	if command != nil || model.skipConfirm {
		t.Fatal("completed skip reopened confirmation before durable polling refresh")
	}
}

func TestSkipConfirmationUsesPlanPhaseAndPollingCancelsStalePrompt(t *testing.T) {
	project := testProject(testSnapshot(t), state.StatusFailed, string(pipeline.PhaseDevelopment), "testing", []state.PhaseRecord{{
		Phase: string(pipeline.PhaseDevelopment), Subphase: "testing", Status: state.StatusFailed, OccurrenceID: "testing-1",
	}})
	project.Plan = &state.PlanState{Phases: []string{"Phase 2: docs"}}
	model, err := NewModel(context.Background(), project, nil, Actions{
		Skip: func(context.Context) error { t.Fatal("stale skip invoked"); return nil }, SkipAvailable: true,
		SkipLabel: "Development / Phase 2: docs / Testing",
	}, WithColor(false))
	if err != nil {
		t.Fatal(err)
	}
	updated, _ := model.Update(key('s'))
	model = updated.(Model)
	if !strings.Contains(model.View(), "Confirm skip of Development / Phase 2: docs / Testing?") {
		t.Fatalf("confirmation omitted plan phase:\n%s", model.View())
	}
	changed := project
	changed.PhaseHistory = []state.PhaseRecord{{
		Phase: string(pipeline.PhaseDevelopment), Subphase: "testing", Status: state.StatusFailed, OccurrenceID: "testing-2",
	}}
	updated, _ = model.Update(projectLoadedMsg{project: changed})
	model = updated.(Model)
	if model.skipConfirm || !strings.Contains(model.View(), "Skip cancelled because the failed execution changed.") {
		t.Fatalf("stale confirmation remained active: %#v\n%s", model, model.View())
	}
}

func TestSkippedExecutionShowsStickyCountImmediately(t *testing.T) {
	project := testProject(testConfiguredSnapshot(t), state.StatusFailed, string(pipeline.PhaseQA), "", []state.PhaseRecord{{
		Phase: string(pipeline.PhaseQA), Status: state.StatusFailed, OccurrenceID: "qa-1",
		Skip: &state.SkipResolution{Cleanup: state.SkipCleanup{Status: state.SkipCleanupNotRequired}},
	}})
	model, err := NewModel(context.Background(), project, nil, Actions{}, WithColor(false))
	if err != nil {
		t.Fatal(err)
	}
	view := model.View()
	if !strings.Contains(view, "QA (1 skipped execution)") || !strings.Contains(view, "(skipped)") {
		t.Fatalf("immediate sticky skip visibility missing:\n%s", view)
	}
}

func TestSkipIsNotOfferedForIneligibleOrStoppedFailures(t *testing.T) {
	for _, status := range []state.LifecycleStatus{state.StatusFailed, state.StatusStopped} {
		project := testProject(testSnapshot(t), status, string(pipeline.PhaseDevelopment), "implementation", []state.PhaseRecord{{
			Phase: string(pipeline.PhaseDevelopment), Subphase: "implementation", Status: state.StatusFailed,
		}})
		model, err := NewModel(context.Background(), project, nil, Actions{Skip: func(context.Context) error { t.Fatal("skip invoked"); return nil }, SkipAvailable: false}, WithColor(false))
		if err != nil {
			t.Fatal(err)
		}
		updated, command := model.Update(key('s'))
		if command != nil || strings.Contains(updated.(Model).View(), "s skip") {
			t.Fatalf("status %s offered skip:\n%s", status, updated.(Model).View())
		}
	}
}

func TestSkippedExecutionKeepsStickyCountAfterLaterPass(t *testing.T) {
	project := testProject(testSnapshot(t), state.StatusRunning, string(pipeline.PhaseDevelopment), "review", []state.PhaseRecord{
		{Phase: string(pipeline.PhaseDevelopment), Subphase: "testing", Status: state.StatusFailed, OccurrenceID: "testing-1", Skip: &state.SkipResolution{Cleanup: state.SkipCleanup{Status: state.SkipCleanupNotRequired}}},
		{Phase: string(pipeline.PhaseDevelopment), Subphase: "testing", Status: state.StatusFinished, OccurrenceID: "testing-2"},
		{Phase: string(pipeline.PhaseDevelopment), Subphase: "review", Status: state.StatusRunning},
	})
	model, err := NewModel(context.Background(), project, nil, Actions{}, WithColor(false))
	if err != nil {
		t.Fatal(err)
	}
	view := model.View()
	if !strings.Contains(view, "Testing (1 skipped execution)") || strings.Contains(view, "Testing (skipped)") {
		t.Fatalf("sticky skip projection missing or stale:\n%s", view)
	}
}

func ptrTime(value time.Time) *time.Time { return &value }
