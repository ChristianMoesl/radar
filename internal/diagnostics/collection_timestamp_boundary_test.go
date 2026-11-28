package diagnostics

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"radar/internal/integration"
	"radar/internal/protocol"
)

func TestCollectionTimestampBoundaryKeepsMalformedAttemptUnknown(t *testing.T) {
	for name, value := range map[string]string{
		"excess_precision":   "2026-11-16T17:01:02.1234567891Z",
		"oversized_fraction": "2026-11-16T17:01:02." + strings.Repeat("1", 4096) + "Z",
		"comma_fraction":     "2026-11-16T17:01:02,1Z",
	} {
		t.Run(name, func(t *testing.T) {
			store, _, path := fixture(t)
			store.SetTasks([]protocol.Task{task(path, "test:ABC-123")})
			store.SetSources([]protocol.SourceStatus{{Name: "workspace", Status: "ok", Detail: "private-provider-body"}})
			beforeTasks, beforeSources, revision := store.Snapshot()
			good := protocol.DiagnosticCollectionAttempt{StartedAt: "2026-11-16T17:01:00Z", CollectionStartedAt: "2026-11-16T17:01:01Z", FinishedAt: "2026-11-16T17:01:02Z", Outcome: "complete"}
			store.RecordCollectionAttempts(map[string]protocol.DiagnosticCollectionAttempt{"workspace": good, "other": good})
			bad := good
			bad.FinishedAt = value
			store.RecordCollectionAttempts(map[string]protocol.DiagnosticCollectionAttempt{"workspace": bad})
			page, err := Read(store, protocol.DiagnosticsRequest{Section: "integrations"}, true, integration.NewRegistry(diagnosticIntegrationTrap{"workspace"}, diagnosticIntegrationTrap{"other"}))
			if err != nil || page.Total != 3 || !page.Complete || !page.ResourceRefreshPending {
				t.Fatal("diagnostic inventory unavailable", err)
			}
			for _, row := range page.Items {
				v := row.Integration
				if v.Name == "workspace" {
					if v.CachedStatus != "ok" || v.Collection != nil || v.CollectionEvidence != "unknown_no_current_daemon_attempt" || v.AccessEvidence != "unknown_not_checked" || v.FreshnessEvidence != "unknown_cache_freshness_not_established" {
						t.Fatal("malformed attempt became historical success, normalized evidence or cache freshness")
					}
				} else if v.Name == "other" && (v.Collection == nil || v.Collection.Latest != good || v.Collection.LastCompleteAt != good.FinishedAt) {
					t.Fatal("malformed provider discarded unrelated genuine history")
				}
			}
			raw, _ := json.Marshal(page)
			afterTasks, afterSources, afterRevision := store.Snapshot()
			if strings.Contains(string(raw), value) || strings.Contains(string(raw), "private-provider-body") || len(raw) > 16*1024 || revision != afterRevision || !reflect.DeepEqual(beforeTasks, afterTasks) || !reflect.DeepEqual(beforeSources, afterSources) {
				t.Fatal("invalid timestamp escaped, expanded output or changed task publication")
			}
		})
	}
}
