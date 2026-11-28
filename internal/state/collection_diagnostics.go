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
	start, ok := collectionDiagnosticTime(attempt.StartedAt)
	if !ok {
		return false
	}
	finish, ok := collectionDiagnosticTime(attempt.FinishedAt)
	if !ok || finish.Before(start) {
		return false
	}
	if attempt.CollectionStartedAt != "" {
		collection, ok := collectionDiagnosticTime(attempt.CollectionStartedAt)
		if !ok || collection.Before(start) || finish.Before(collection) {
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

// Retain only the bounded native formatter's UTC representation. Go's parser
// also accepts comma decimals and silently truncates excess fractional digits;
// keeping that original text would violate the public diagnostic time boundary.
// Never normalize an invalid observation into new evidence.
func collectionDiagnosticTime(value string) (time.Time, bool) {
	if len(value) < 20 || len(value) > 30 {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return parsed, err == nil && !parsed.IsZero() && parsed.Year() >= 1 && parsed.UTC().Format(time.RFC3339Nano) == value
}
