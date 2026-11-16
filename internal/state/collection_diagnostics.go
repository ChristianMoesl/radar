package state

import (
	"time"

	"radar/internal/protocol"
)

// RecordCollectionAttempts retains one returned native envelope per source in
// this Store lifetime. It never writes projection data, advances a task revision,
// notifies task watchers, or certifies reconciliation/publication/cache freshness.
func (s *Store) RecordCollectionAttempts(attempts map[string]protocol.DiagnosticCollectionAttempt) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.collectionAttempts == nil {
		s.collectionAttempts = map[string]protocol.DiagnosticCollectionEvidence{}
	}
	for name, attempt := range attempts {
		if name == "" {
			continue
		}
		if !validCollectionAttempt(attempt) {
			delete(s.collectionAttempts, name)
			continue
		}
		current := s.collectionAttempts[name]
		finished, _ := time.Parse(time.RFC3339Nano, attempt.FinishedAt)
		oldFinished, _ := time.Parse(time.RFC3339Nano, current.Latest.FinishedAt)
		lastComplete, _ := time.Parse(time.RFC3339Nano, current.LastCompleteAt)
		if attempt.Outcome == "complete" && finished.After(lastComplete) {
			current.LastCompleteAt = attempt.FinishedAt
		}
		if !finished.Before(oldFinished) {
			current.Latest = attempt
		}
		s.collectionAttempts[name] = current
	}
}

func validCollectionAttempt(attempt protocol.DiagnosticCollectionAttempt) bool {
	start, err := time.Parse(time.RFC3339Nano, attempt.StartedAt)
	if err != nil || start.IsZero() || start.Location() != time.UTC {
		return false
	}
	finish, err := time.Parse(time.RFC3339Nano, attempt.FinishedAt)
	if err != nil || finish.IsZero() || finish.Before(start) || finish.Location() != time.UTC {
		return false
	}
	if attempt.CollectionStartedAt != "" {
		collection, err := time.Parse(time.RFC3339Nano, attempt.CollectionStartedAt)
		if err != nil || collection.IsZero() || collection.Before(start) || finish.Before(collection) || collection.Location() != time.UTC {
			return false
		}
	}
	switch attempt.Outcome {
	case "complete", "incomplete":
		return attempt.CollectionStartedAt != ""
	case "skipped_before_collection":
		return attempt.CollectionStartedAt == ""
	case "cancelled_or_timed_out":
		return true
	default:
		return false
	}
}
