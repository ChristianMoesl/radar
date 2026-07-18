package notification

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"radar/internal/protocol"
)

type sentNotification struct {
	title string
	body  string
}

type recordingSender struct {
	sent []sentNotification
	err  error
}

func (s *recordingSender) Send(_ context.Context, title, body string) error {
	s.sent = append(s.sent, sentNotification{title: title, body: body})
	return s.err
}

func TestNotifyTransitionsSendsForNewActionableTasks(t *testing.T) {
	sender := &recordingSender{}
	service := NewWithSender(discardLogger(), sender)

	service.NotifyTransitions(context.Background(), nil, []protocol.Task{
		{ID: 1, Title: "Review PR", Attention: "attention", Reason: "review requested"},
		{ID: 2, Title: "Fix build", Attention: "immediate", Reason: "checks failed"},
		{ID: 3, Title: "Write code", Attention: "in_progress"},
		{ID: 4, Title: "Old work", Attention: "done"},
		{ID: 5, Title: "Muted by priority", Attention: "low_priority"},
	})

	if len(sender.sent) != 2 {
		t.Fatalf("sent %d notifications, want 2: %#v", len(sender.sent), sender.sent)
	}
	if sender.sent[0] != (sentNotification{title: "Radar: Review PR", body: "review requested"}) {
		t.Fatalf("first notification = %#v", sender.sent[0])
	}
	if sender.sent[1] != (sentNotification{title: "Radar: Fix build", body: "checks failed"}) {
		t.Fatalf("second notification = %#v", sender.sent[1])
	}
}

func TestNotifyTransitionsSendsWhenTaskStartsNeedingAttention(t *testing.T) {
	sender := &recordingSender{}
	service := NewWithSender(discardLogger(), sender)

	service.NotifyTransitions(context.Background(),
		[]protocol.Task{{ID: 1, Attention: "in_progress"}},
		[]protocol.Task{{ID: 1, Title: "Review PR", Attention: "attention", Reason: "new comment"}},
	)

	if len(sender.sent) != 1 {
		t.Fatalf("sent %d notifications, want 1", len(sender.sent))
	}
}

func TestNotifyTransitionsDoesNotRepeatActionableTask(t *testing.T) {
	sender := &recordingSender{}
	service := NewWithSender(discardLogger(), sender)

	service.NotifyTransitions(context.Background(),
		[]protocol.Task{{ID: 1, Attention: "attention", Reason: "review requested"}},
		[]protocol.Task{{ID: 1, Attention: "immediate", Reason: "checks failed"}},
	)

	if len(sender.sent) != 0 {
		t.Fatalf("sent notifications = %#v, want none", sender.sent)
	}
}

func TestNotifyTransitionsUsesFallbackContent(t *testing.T) {
	sender := &recordingSender{}
	service := NewWithSender(discardLogger(), sender)

	service.NotifyTransitions(context.Background(), nil, []protocol.Task{{ID: 42, Attention: "attention"}})

	want := sentNotification{title: "Radar: Task 42", body: "Needs attention"}
	if len(sender.sent) != 1 || sender.sent[0] != want {
		t.Fatalf("sent notifications = %#v, want %#v", sender.sent, want)
	}
}

func TestNotifyTransitionsContinuesAfterSenderError(t *testing.T) {
	sender := &recordingSender{err: errors.New("unavailable")}
	service := NewWithSender(discardLogger(), sender)

	service.NotifyTransitions(context.Background(), nil, []protocol.Task{
		{ID: 1, Attention: "attention"},
		{ID: 2, Attention: "attention"},
	})

	if len(sender.sent) != 2 {
		t.Fatalf("sent %d notifications, want 2", len(sender.sent))
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
