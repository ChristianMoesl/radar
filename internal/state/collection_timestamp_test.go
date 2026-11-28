package state

import (
	"strings"
	"testing"

	"radar/internal/protocol"
)

func TestCollectionTimestampBoundaryRejectsNonNativeText(t *testing.T) {
	for _, field := range []string{"start", "collection", "finish"} {
		for name, value := range map[string]string{
			"excess_precision":   "2026-11-16T17:01:01.1234567891Z",
			"oversized_fraction": "2026-11-16T17:01:01." + strings.Repeat("1", 4096) + "Z",
			"comma_fraction":     "2026-11-16T17:01:01,1Z",
			"redundant_zero":     "2026-11-16T17:01:01.0Z",
			"trailing_zero":      "2026-11-16T17:01:01.1230Z",
			"UTC_offset_alias":   "2026-11-16T17:01:01+00:00",
			"non_UTC_offset":     "2026-11-16T18:01:01+01:00",
			"private":            "private-provider-body token=private-token",
			"invalid_date":       "2026-02-30T17:01:01Z",
		} {
			t.Run(field+"/"+name, func(t *testing.T) {
				a := protocol.DiagnosticCollectionAttempt{StartedAt: "2026-11-16T17:01:00Z", CollectionStartedAt: "2026-11-16T17:01:01Z", FinishedAt: "2026-11-16T17:01:02Z", Outcome: "complete"}
				// Keep time ordering valid for the syntactically valid aliases;
				// rejection must not depend on a reversed envelope.
				switch field {
				case "start":
					a.StartedAt, a.CollectionStartedAt = value, a.FinishedAt
				case "collection":
					a.CollectionStartedAt = value
				case "finish":
					a.FinishedAt = value
				}
				store := &Store{}
				store.RecordCollectionAttempts(map[string]protocol.DiagnosticCollectionAttempt{"example": a})
				_, _, attempts, revision := store.SourceInspectionSnapshot()
				if len(attempts) != 0 || revision != 0 {
					t.Fatal("non-native timestamp retained as bounded diagnostic evidence", field, name)
				}
			})
		}
	}
}

func TestCollectionTimestampBoundaryPreservesNativePrecisionAndOutcomes(t *testing.T) {
	for _, value := range []string{"2026-11-16T17:01:01Z", "2026-11-16T17:01:01.1Z", "2026-11-16T17:01:01.000000001Z", "2026-11-16T17:01:01.123456789Z"} {
		for _, outcome := range []string{"complete", "incomplete", "skipped_before_collection", "cancelled_or_timed_out"} {
			t.Run(value+"/"+outcome, func(t *testing.T) {
				a := protocol.DiagnosticCollectionAttempt{StartedAt: value, FinishedAt: value, Outcome: outcome}
				if outcome == "complete" || outcome == "incomplete" {
					a.CollectionStartedAt = value
				}
				store := &Store{}
				store.RecordCollectionAttempts(map[string]protocol.DiagnosticCollectionAttempt{"example": a})
				_, _, attempts, revision := store.SourceInspectionSnapshot()
				want := protocol.DiagnosticCollectionEvidence{Latest: a}
				if outcome == "complete" {
					want.LastCompleteAt = value
				}
				if len(attempts) != 1 || attempts["example"] != want || revision != 0 {
					t.Fatal("native timestamp precision, zero-duration or finite outcome changed", outcome)
				}
			})
		}
	}
}
