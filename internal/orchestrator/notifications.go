package orchestrator

import (
	"context"
	"errors"
	"sync"
)

// CompletionNotificationSubscriber translates the durable project-finished
// lifecycle event into one transport-neutral completion notification. It is
// deliberately independent of TUI attachment and owns delivery de-duplication
// at the project boundary.
type CompletionNotificationSubscriber struct {
	observer ProjectObserver
	sink     NotificationSink

	mu       sync.Mutex
	notified map[string]struct{}
}

// NewCompletionNotificationSubscriber constructs an optional completion
// subscriber. A nil observer or sink disables delivery without affecting the
// lifecycle event stream.
func NewCompletionNotificationSubscriber(observer ProjectObserver, sink NotificationSink) *CompletionNotificationSubscriber {
	return &CompletionNotificationSubscriber{
		observer: observer,
		sink:     sink,
		notified: make(map[string]struct{}),
	}
}

// Publish implements EventSink so the subscriber can be attached beside the
// durable event sink. Notification errors are returned to the caller so the
// event fan-out can deliberately isolate them from orchestration progress.
func (s *CompletionNotificationSubscriber) Publish(ctx context.Context, event Event) error {
	if s == nil || event.Type != EventProjectFinished || event.ProjectSlug == "" || s.observer == nil || s.sink == nil {
		return nil
	}

	s.mu.Lock()
	if _, exists := s.notified[event.ProjectSlug]; exists {
		s.mu.Unlock()
		return nil
	}
	// Claim before delivery: a transport failure is one failed delivery attempt,
	// not permission to emit duplicate completion notifications on replay.
	s.notified[event.ProjectSlug] = struct{}{}
	s.mu.Unlock()

	observations, err := s.observer.ObserveAll(ctx)
	if err != nil {
		return err
	}
	for _, observation := range observations {
		if observation.Project.Slug != event.ProjectSlug {
			continue
		}
		if !observation.Terminal || !observation.TerminalKind.IsCompletion() {
			return errors.New("project-finished event did not resolve to a completed terminal project")
		}
		return s.sink.Notify(ctx, Notification{
			Project:      observation,
			Kind:         NotificationProjectCompleted,
			At:           event.At,
			TerminalKind: observation.TerminalKind,
		})
	}
	return errors.New("project-finished event project was not found in durable observations")
}
