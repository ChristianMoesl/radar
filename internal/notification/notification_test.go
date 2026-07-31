package notification

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"radar/internal/protocol"
)

type recordingSender struct {
	sent []Notification
	err  error
}

func (s *recordingSender) Send(_ context.Context, notification Notification) error {
	s.sent = append(s.sent, notification)
	return s.err
}

func TestNotifyTransitionsSendsForNewActionableTasks(t *testing.T) {
	sender := &recordingSender{}
	service := NewWithSender(discardLogger(), sender)

	service.NotifyTransitions(context.Background(), nil, []protocol.Task{
		{ID: 1, Title: "Review PR", URL: "https://github.com/example/repo/pull/1", Attention: "attention", Reason: "review requested"},
		{ID: 2, Title: "Fix build", URL: "https://github.com/example/repo/actions/runs/2", Attention: "immediate", Reason: "checks failed"},
		{ID: 3, Title: "Write code", Attention: "in_progress"},
		{ID: 4, Title: "Old work", Attention: "done"},
		{ID: 5, Title: "Muted by priority", Attention: "low_priority"},
	})

	if len(sender.sent) != 2 {
		t.Fatalf("sent %d notifications, want 2: %#v", len(sender.sent), sender.sent)
	}
	if sender.sent[0] != (Notification{Title: "Radar: Review PR", Body: "review requested", URL: "https://github.com/example/repo/pull/1"}) {
		t.Fatalf("first notification = %#v", sender.sent[0])
	}
	if sender.sent[1] != (Notification{Title: "Radar: Fix build", Body: "checks failed", URL: "https://github.com/example/repo/actions/runs/2"}) {
		t.Fatalf("second notification = %#v", sender.sent[1])
	}
}

func TestNotifyTransitionsUsesRelevantGitHubPullRequestURL(t *testing.T) {
	sender := &recordingSender{}
	service := NewWithSender(discardLogger(), sender)

	service.NotifyTransitions(context.Background(), nil, []protocol.Task{{
		ID: 1, Title: "CAP-42", URL: "https://jira.example/browse/CAP-42", Attention: "immediate", Reason: "checks failed",
		SourceRefs: []protocol.SourceRef{
			{ID: "github:pr:acme/app:41", Source: "github", Kind: "pull_request", URL: "https://github.com/acme/app/pull/41", Signal: "attention", Status: "review requested"},
			{ID: "github:pr:acme/app:42", Source: "github", Kind: "pull_request", Signal: "immediate", Status: "checks failed", Repo: "acme/app"},
		},
	}})

	if len(sender.sent) != 1 || sender.sent[0].URL != "https://github.com/acme/app/pull/42" {
		t.Fatalf("sent notifications = %#v, want relevant GitHub PR URL", sender.sent)
	}
}

func TestNotifyTransitionsLinksDatadogAlertToMonitor(t *testing.T) {
	sender := &recordingSender{}
	service := NewWithSender(discardLogger(), sender)
	monitorURL := "https://app.datadoghq.eu/monitors/42"

	service.NotifyTransitions(context.Background(), nil, []protocol.Task{{
		ID: 42, Title: "API errors", URL: monitorURL, Attention: "immediate", Reason: "Datadog monitor is alerting",
		SourceRefs: []protocol.SourceRef{{ID: "datadog:monitor:42", Source: "datadog", Kind: "monitor", URL: monitorURL}},
	}})

	want := Notification{Title: "Radar: API errors", Body: "Datadog monitor is alerting", URL: monitorURL}
	if len(sender.sent) != 1 || sender.sent[0] != want {
		t.Fatalf("sent notifications = %#v, want %#v", sender.sent, want)
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

	want := Notification{Title: "Radar: Task 42", Body: "Needs attention"}
	if len(sender.sent) != 1 || sender.sent[0] != want {
		t.Fatalf("sent notifications = %#v, want %#v", sender.sent, want)
	}
}

func TestNotifyGarbageCollectionSendsResult(t *testing.T) {
	sender := &recordingSender{}
	service := NewWithSender(discardLogger(), sender)

	service.NotifyGarbageCollection(context.Background(), protocol.GarbageCollectionResult{
		Deleted: []protocol.GarbageCollectionItem{{TaskID: 1}, {TaskID: 2}},
		Skipped: []protocol.GarbageCollectionItem{{TaskID: 3}},
	})

	want := Notification{Title: "Radar: Garbage collection", Body: "Deleted 2 workspace(s), skipped 1"}
	if len(sender.sent) != 1 || sender.sent[0] != want {
		t.Fatalf("sent notifications = %#v, want %#v", sender.sent, want)
	}
}

func TestNotifyGarbageCollectionSendsEmptyResult(t *testing.T) {
	sender := &recordingSender{}
	service := NewWithSender(discardLogger(), sender)

	service.NotifyGarbageCollection(context.Background(), protocol.GarbageCollectionResult{})

	want := Notification{Title: "Radar: Garbage collection", Body: "No workspaces eligible for cleanup"}
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
