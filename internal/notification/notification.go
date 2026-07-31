package notification

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

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
	refs := make([]protocol.SourceRef, 0)
	for _, ref := range task.SourceRefs {
		if ref.Source == "github" && ref.Kind == "pull_request" {
			refs = append(refs, ref)
		}
	}
	for _, ref := range refs {
		if ref.Signal == task.Attention && ref.Status == task.Reason {
			if url := pullRequestURL(ref); url != "" {
				return url
			}
		}
	}
	for _, ref := range refs {
		if ref.Signal == task.Attention {
			if url := pullRequestURL(ref); url != "" {
				return url
			}
		}
	}
	for _, ref := range refs {
		if url := pullRequestURL(ref); url != "" {
			return url
		}
	}
	return task.URL
}

func pullRequestURL(ref protocol.SourceRef) string {
	if strings.HasPrefix(ref.URL, "https://github.com/") || strings.HasPrefix(ref.URL, "http://github.com/") {
		return ref.URL
	}

	const prefix = "github:pr:"
	if !strings.HasPrefix(ref.ID, prefix) {
		return ""
	}
	id := strings.TrimPrefix(ref.ID, prefix)
	separator := strings.LastIndexByte(id, ':')
	if separator <= 0 || separator == len(id)-1 {
		return ""
	}
	repo := strings.TrimSpace(ref.Repo)
	if repo == "" {
		repo = id[:separator]
	}
	number, err := strconv.Atoi(id[separator+1:])
	if err != nil || number <= 0 || strings.Count(repo, "/") != 1 {
		return ""
	}
	return fmt.Sprintf("https://github.com/%s/pull/%d", repo, number)
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
