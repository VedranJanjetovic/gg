package orchestrator_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/VedranJanjetovic/gg/internal/orchestrator"
	"github.com/VedranJanjetovic/gg/internal/state"
)

type notificationObservationSource struct {
	observations []state.ProjectObservation
	calls        int
}

func (s *notificationObservationSource) ObserveAll(context.Context) ([]state.ProjectObservation, error) {
	s.calls++
	return append([]state.ProjectObservation(nil), s.observations...), nil
}

type recordingNotificationSink struct {
	notifications []orchestrator.Notification
	err           error
}

func (s *recordingNotificationSink) Notify(_ context.Context, notification orchestrator.Notification) error {
	s.notifications = append(s.notifications, notification)
	return s.err
}

func completedObservation(slug string, kind state.TerminalKind) state.ProjectObservation {
	project := state.ProjectState{Slug: slug, Status: state.StatusFinished}
	if kind != state.TerminalPipelineComplete {
		project.Terminal = &state.TerminalState{Kind: kind, At: time.Unix(10, 0).UTC()}
	}
	return state.Observe(project)
}

func TestCompletionNotificationSubscriberNotifiesMergedProjectExactlyOnce(t *testing.T) {
	source := &notificationObservationSource{observations: []state.ProjectObservation{completedObservation("merged", state.TerminalPullRequestMerged)}}
	sink := &recordingNotificationSink{}
	subscriber := orchestrator.NewCompletionNotificationSubscriber(source, sink)
	event := orchestrator.Event{ProjectSlug: "merged", Type: orchestrator.EventProjectFinished, At: time.Unix(20, 0).UTC()}

	if err := subscriber.Publish(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if err := subscriber.Publish(context.Background(), event); err != nil {
		t.Fatal(err)
	}

	if len(sink.notifications) != 1 {
		t.Fatalf("notifications = %d, want exactly one", len(sink.notifications))
	}
	got := sink.notifications[0]
	if got.Kind != orchestrator.NotificationProjectCompleted || got.TerminalKind != state.TerminalPullRequestMerged || got.Project.Project.Slug != "merged" {
		t.Fatalf("notification = %#v", got)
	}
	if source.calls != 1 {
		t.Fatalf("durable observations = %d, want one lookup", source.calls)
	}
}

func TestCompletionNotificationSubscriberHandlesPipelineCompletionWithoutTUI(t *testing.T) {
	source := &notificationObservationSource{observations: []state.ProjectObservation{completedObservation("standalone", state.TerminalPipelineComplete)}}
	sink := &recordingNotificationSink{}
	subscriber := orchestrator.NewCompletionNotificationSubscriber(source, sink)

	if err := subscriber.Publish(context.Background(), orchestrator.Event{ProjectSlug: "standalone", Type: orchestrator.EventProjectFinished, At: time.Unix(30, 0).UTC()}); err != nil {
		t.Fatal(err)
	}
	if len(sink.notifications) != 1 || sink.notifications[0].Project.Project.Slug != "standalone" {
		t.Fatalf("notifications = %#v", sink.notifications)
	}
}

func TestCompletionNotificationFailureDoesNotBlockControllerCompletion(t *testing.T) {
	source := &notificationObservationSource{observations: []state.ProjectObservation{completedObservation("failure-isolated", state.TerminalPipelineComplete)}}
	sink := &recordingNotificationSink{err: errors.New("notification transport unavailable")}
	subscriber := orchestrator.NewCompletionNotificationSubscriber(source, sink)
	store := &persistedResumeState{project: state.ProjectState{Slug: "failure-isolated", Status: state.StatusPending}}
	execution := request(t, resolvedPipeline(t))
	execution.Project.Slug = "failure-isolated"
	controller := orchestrator.NewController(
		orchestrator.WithRunner(&fakeSeqRunner{}),
		orchestrator.WithPhaseState(store),
		orchestrator.WithCompletionNotificationSubscriber(subscriber),
		orchestrator.WithPromptBuilder(fakePrompt{}),
	)

	if _, err := controller.Execute(context.Background(), execution); err != nil {
		t.Fatalf("Execute() returned notification failure: %v", err)
	}
	if got := store.snapshot().Status; got != state.StatusFinished {
		t.Fatalf("durable project status = %s, want finished", got)
	}
	if len(sink.notifications) != 1 {
		t.Fatalf("notifications = %d, want one failed delivery attempt", len(sink.notifications))
	}
}

func TestCompletionNotificationIsIndependentOfTUIEventSinkAttachment(t *testing.T) {
	for _, test := range []struct {
		name      string
		eventSink orchestrator.EventSink
	}{
		{name: "unattached"},
		{name: "attached", eventSink: &fakeEvents{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := &notificationObservationSource{observations: []state.ProjectObservation{completedObservation(test.name, state.TerminalPipelineComplete)}}
			sink := &recordingNotificationSink{}
			subscriber := orchestrator.NewCompletionNotificationSubscriber(source, sink)
			store := &persistedResumeState{project: state.ProjectState{Slug: test.name, Status: state.StatusPending}}
			execution := request(t, resolvedPipeline(t))
			execution.Project.Slug = test.name
			options := []orchestrator.ControllerOption{
				orchestrator.WithRunner(&fakeSeqRunner{}),
				orchestrator.WithPhaseState(store),
				orchestrator.WithCompletionNotificationSubscriber(subscriber),
				orchestrator.WithPromptBuilder(fakePrompt{}),
			}
			if test.eventSink != nil {
				options = append(options, orchestrator.WithEventSink(test.eventSink))
			}
			if _, err := orchestrator.NewController(options...).Execute(context.Background(), execution); err != nil {
				t.Fatal(err)
			}
			if len(sink.notifications) != 1 {
				t.Fatalf("notifications = %d, want one", len(sink.notifications))
			}
		})
	}
}

func TestCompletionNotificationSubscriberIgnoresNonCompletionEvents(t *testing.T) {
	source := &notificationObservationSource{observations: []state.ProjectObservation{completedObservation("demo", state.TerminalPipelineComplete)}}
	sink := &recordingNotificationSink{}
	subscriber := orchestrator.NewCompletionNotificationSubscriber(source, sink)
	if err := subscriber.Publish(context.Background(), orchestrator.Event{ProjectSlug: "demo", Type: orchestrator.EventPhaseSucceeded}); err != nil {
		t.Fatal(err)
	}
	if len(sink.notifications) != 0 || source.calls != 0 {
		t.Fatalf("non-completion event was delivered: notifications=%#v observations=%d", sink.notifications, source.calls)
	}
}
