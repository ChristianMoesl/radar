package state

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"radar.nvim/internal/protocol"
)

type Store struct {
	mu      sync.RWMutex
	items   []protocol.Task
	sources []protocol.SourceStatus
	path    string
	logger  *slog.Logger
}

func Path() (string, error) {
	if explicit := os.Getenv("RADAR_STATE"); explicit != "" {
		return explicit, nil
	}

	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}

	return filepath.Join(base, "radar", "tasks.json"), nil
}

func NewStore(logger *slog.Logger) (*Store, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}

	store := &Store{
		items:  []protocol.Task{},
		path:   path,
		logger: logger,
	}
	if err := store.Load(); err != nil {
		logger.Warn("could not load state", "path", path, "error", err)
	}
	return store, nil
}

func (s *Store) Load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.logger.Info("state file does not exist yet", "path", s.path)
			return nil
		}
		return err
	}

	var items []protocol.Task
	if err := json.Unmarshal(data, &items); err != nil {
		return err
	}

	s.mu.Lock()
	s.items = items
	s.mu.Unlock()

	s.logger.Info("state loaded", "path", s.path, "tasks", len(items))
	return nil
}

func (s *Store) SetTasks(items []protocol.Task) {
	s.mu.Lock()
	s.items = assignTaskIDs(s.items, items)
	s.mu.Unlock()

	if err := s.Save(); err != nil {
		s.logger.Warn("could not save state", "path", s.path, "error", err)
	}
}

func (s *Store) Save() error {
	s.mu.RLock()
	items := make([]protocol.Task, len(s.items))
	copy(items, s.items)
	s.mu.RUnlock()

	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}

	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return err
	}

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}

	s.logger.Debug("state saved", "path", s.path, "tasks", len(items))
	return nil
}

func (s *Store) Acknowledge(itemID string) bool {
	s.mu.Lock()
	changed := false
	ackAt := time.Now().UTC().Format(time.RFC3339)
	items := s.items[:0]
	for i := range s.items {
		item := s.items[i]
		if fmt.Sprint(item.ID) != itemID {
			items = append(items, item)
			continue
		}
		for j := range item.SourceRefs {
			if item.SourceRefs[j].Metadata == nil {
				item.SourceRefs[j].Metadata = map[string]string{}
			}
			if latest := item.SourceRefs[j].Metadata["latest_general_comment_at"]; latest != "" {
				ackAt = latest
			}
			item.SourceRefs[j].Metadata["general_comments_ack_at"] = ackAt
			delete(item.SourceRefs[j].Metadata, "new_general_comments")
			changed = true
		}
		if item.Kind == "github_pr_activity" && !hasUnresolvedReviewThreads(item) {
			changed = true
			continue
		}
		if item.Kind == "github_own_pr" && !hasUnresolvedReviewThreads(item) {
			item.Attention = "in_progress"
			item.Reason = baseReason(item)
			for j := range item.SourceRefs {
				item.SourceRefs[j].Status = item.Reason
			}
		}
		items = append(items, item)
	}
	s.items = items
	s.mu.Unlock()

	if changed {
		if err := s.Save(); err != nil {
			s.logger.Warn("could not save acknowledged state", "path", s.path, "error", err)
		}
	}
	return changed
}

var ticketPattern = regexp.MustCompile(`(?i)[A-Z][A-Z0-9]+-[0-9]+`)

func assignTaskIDs(previous []protocol.Task, next []protocol.Task) []protocol.Task {
	bySourceRef := map[string]int{}
	byKey := map[string]int{}
	maxID := 0
	for _, task := range previous {
		if task.ID > maxID {
			maxID = task.ID
		}
		for _, sourceRef := range task.SourceRefs {
			bySourceRef[sourceRef.ID] = task.ID
		}
		for _, key := range taskKeys(task) {
			byKey[key] = task.ID
		}
	}

	assigned := make([]protocol.Task, len(next))
	used := map[int]bool{}
	for i, task := range next {
		id := task.ID
		if id == 0 {
			id = matchingTaskID(task, bySourceRef, byKey, used)
		}
		if id == 0 {
			maxID++
			id = maxID
		}
		task.ID = id
		used[id] = true
		assigned[i] = task
	}
	return assigned
}

func matchingTaskID(task protocol.Task, bySourceRef map[string]int, byKey map[string]int, used map[int]bool) int {
	for _, sourceRef := range task.SourceRefs {
		if id := bySourceRef[sourceRef.ID]; id != 0 && !used[id] {
			return id
		}
	}
	for _, key := range taskKeys(task) {
		if id := byKey[key]; id != 0 && !used[id] {
			return id
		}
	}
	return 0
}

func taskKeys(task protocol.Task) []string {
	values := []string{task.Title, task.Repo, task.URL}
	for _, sourceRef := range task.SourceRefs {
		values = append(values, sourceRef.ID, sourceRef.Title, sourceRef.Branch, sourceRef.Path, sourceRef.Repo, sourceRef.URL)
		for _, value := range sourceRef.Metadata {
			values = append(values, value)
		}
	}
	keys := make([]string, 0)
	seen := map[string]bool{}
	for _, value := range values {
		for _, match := range ticketPattern.FindAllString(value, -1) {
			key := match
			if !seen[key] {
				seen[key] = true
				keys = append(keys, key)
			}
		}
	}
	return keys
}

func hasUnresolvedReviewThreads(item protocol.Task) bool {
	for _, sourceRef := range item.SourceRefs {
		if sourceRef.Metadata != nil && sourceRef.Metadata["unresolved_review_threads"] != "" {
			return true
		}
	}
	return false
}

func baseReason(item protocol.Task) string {
	for _, sourceRef := range item.SourceRefs {
		if sourceRef.Metadata != nil && sourceRef.Metadata["base_reason"] != "" {
			return sourceRef.Metadata["base_reason"]
		}
	}
	return "open PR"
}

func (s *Store) Tasks() []protocol.Task {
	s.mu.RLock()
	defer s.mu.RUnlock()

	items := make([]protocol.Task, len(s.items))
	copy(items, s.items)
	return items
}

func (s *Store) SetSources(sources []protocol.SourceStatus) {
	s.mu.Lock()
	s.sources = make([]protocol.SourceStatus, len(sources))
	copy(s.sources, sources)
	s.mu.Unlock()
}

func (s *Store) Sources() []protocol.SourceStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sources := make([]protocol.SourceStatus, len(s.sources))
	copy(sources, s.sources)
	return sources
}

func (s *Store) Summary() protocol.Summary {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var summary protocol.Summary
	for _, item := range s.items {
		switch item.Attention {
		case "immediate":
			summary.Immediate++
		case "attention":
			summary.Attention++
		case "in_progress":
			summary.InProgress++
		case "done":
			summary.Done++
		case "low_priority":
			summary.LowPriority++
		}
	}
	return summary
}
