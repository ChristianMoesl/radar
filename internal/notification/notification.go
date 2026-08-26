package notification

import (
	"context"
	"fmt"
	"log/slog"

	"radar/internal/protocol"
)

// Notification describes a host operating system notification and its optional
// click destination.
type Notification struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url,omitempty"`
}

// Sender delivers one host operating system notification.
type Sender interface {
	Send(context.Context, Notification) error
}

// Service delivers Radar events through host operating system notifications.
type Service struct {
	sender Sender
	logger *slog.Logger
}

func New(logger *slog.Logger) Service {
	return Service{sender: newPlatformSender(), logger: logger}
}

func NewWithSender(logger *slog.Logger, sender Sender) Service {
	return Service{sender: sender, logger: logger}
}

func (s Service) NotifyTransitions(ctx context.Context, previous, current []protocol.Task) {
	if s.sender == nil {
		return
	}
	for _, task := range newlyActionable(previous, current) {
		title := task.Title
		if title == "" {
			title = fmt.Sprintf("Task %d", task.ID)
		}
		body := task.Reason
		if body == "" {
			body = "Needs attention"
		}
		notification := Notification{Title: "Radar: " + title, Body: body, URL: notificationURL(task)}
		if err := s.sender.Send(ctx, notification); err != nil {
			s.logger.Warn("could not send notification", "task_id", task.ID, "error", err)
		}
	}
}

func (s Service) NotifyGarbageCollection(ctx context.Context, result protocol.GarbageCollectionResult) {
	if s.sender == nil {
		return
	}
	body := fmt.Sprintf("Deleted %d workspace(s), skipped %d", len(result.Deleted), len(result.Skipped))
	if len(result.Deleted) == 0 && len(result.Skipped) == 0 {
		body = "No workspaces eligible for cleanup"
	}
	notification := Notification{Title: "Radar: Garbage collection", Body: body}
	if err := s.sender.Send(ctx, notification); err != nil {
		s.logger.Warn("could not send garbage collection notification", "error", err)
	}
}

func notificationURL(task protocol.Task) string {
	for _, ref := range task.SourceRefs {
		if ref.URL != "" && ref.Signal == task.Attention && ref.Status == task.Reason {
			return ref.URL
		}
	}
	for _, ref := range task.SourceRefs {
		if ref.URL != "" && ref.Signal == task.Attention {
			return ref.URL
		}
	}
	for _, ref := range task.SourceRefs {
		if ref.URL != "" {
			return ref.URL
		}
	}
	return task.URL
}

func newlyActionable(previous, current []protocol.Task) []protocol.Task {
	previousByID := make(map[int]protocol.Task, len(previous))
	for _, task := range previous {
		previousByID[task.ID] = task
	}

	result := make([]protocol.Task, 0)
	for _, task := range current {
		if !needsAttention(task) {
			continue
		}
		previousTask, existed := previousByID[task.ID]
		if existed && needsAttention(previousTask) {
			continue
		}
		result = append(result, task)
	}
	return result
}

func needsAttention(task protocol.Task) bool {
	return task.Attention == "immediate" || task.Attention == "attention"
}
