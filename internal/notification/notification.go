package notification

import (
	"context"
	"fmt"
	"log/slog"

	"radar/internal/protocol"
)

// Sender delivers one host operating system notification.
type Sender interface {
	Send(context.Context, string, string) error
}

// Service finds tasks that newly need attention and notifies the user.
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
		if err := s.sender.Send(ctx, "Radar: "+title, body); err != nil {
			s.logger.Warn("could not send notification", "task_id", task.ID, "error", err)
		}
	}
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
