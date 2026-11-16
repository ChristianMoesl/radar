package diagnostics

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"radar/internal/integration"
	"radar/internal/protocol"
)

func TestIntegrationCollectionEvidenceIsScopedAtomicPrivateAndCursorBound(t *testing.T) {
	store, _, path := fixture(t)
	store.SetTasks([]protocol.Task{task(path, "test:ABC-123")})
	providers := integration.NewRegistry(diagnosticIntegrationTrap{"workspace"}, diagnosticIntegrationTrap{"unrelated"})
	attempt := protocol.DiagnosticCollectionAttempt{StartedAt: "2026-11-16T17:01:00Z", CollectionStartedAt: "2026-11-16T17:01:01Z", FinishedAt: "2026-11-16T17:01:02Z", Outcome: "complete"}
	store.RecordCollectionAttempts(map[string]protocol.DiagnosticCollectionAttempt{"workspace": attempt, "unrelated": attempt})
	beforeDisk, err := os.ReadFile(os.Getenv("RADAR_STATE"))
	if err != nil {
		t.Fatal(err)
	}
	beforeTasks, beforeSources, revision := store.Snapshot()
	scoped, err := Read(store, protocol.DiagnosticsRequest{Section: "integrations", Workspace: path}, true, providers)
	if err != nil || scoped.Total != 1 || !scoped.Complete {
		t.Fatalf("scope=%+v %v", scoped, err)
	}
	for _, item := range scoped.Items {
		v := item.Integration
		if v.Collection == nil || v.CollectionEvidence != "current_daemon_completed_attempt" || v.Collection.Latest != attempt || v.Collection.LastCompleteAt != attempt.FinishedAt || v.AccessEvidence != "unknown_not_checked" || v.FreshnessEvidence != "unknown_cache_freshness_not_established" {
			t.Fatalf("evidence=%+v", v)
		}
	}
	first, err := Read(store, protocol.DiagnosticsRequest{Section: "integrations", Limit: 1}, false, providers)
	if err != nil || first.NextCursor == "" {
		t.Fatalf("first=%+v %v", first, err)
	}
	attempt.Outcome = "incomplete"
	attempt.FinishedAt = "2026-11-16T17:01:03Z"
	store.RecordCollectionAttempts(map[string]protocol.DiagnosticCollectionAttempt{"workspace": attempt})
	if _, err := Read(store, protocol.DiagnosticsRequest{Section: "integrations", Limit: 1, Cursor: first.NextCursor}, false, providers); err == nil {
		t.Fatal("attempt drift reused snapshot cursor")
	}
	after, err := Read(store, protocol.DiagnosticsRequest{Section: "integrations", Workspace: path}, true, providers)
	if err != nil {
		t.Fatal(err)
	}
	v := after.Items[0].Integration
	if v.Collection.Latest.Outcome != "incomplete" || v.Collection.LastCompleteAt != "2026-11-16T17:01:02Z" || !after.ResourceRefreshPending || after.Revision != revision {
		t.Fatalf("failure changed completion/freshness evidence: %+v", after)
	}
	raw, _ := json.Marshal(after)
	for _, private := range []string{"private-title", "private-token", "private-provider-body", "private-env"} {
		if strings.Contains(string(raw), private) {
			t.Fatalf("private evidence leaked: %s", raw)
		}
	}
	afterDisk, _ := os.ReadFile(os.Getenv("RADAR_STATE"))
	tasks, sources, afterRevision := store.Snapshot()
	if !reflect.DeepEqual(beforeDisk, afterDisk) || !reflect.DeepEqual(beforeTasks, tasks) || !reflect.DeepEqual(beforeSources, sources) || afterRevision != revision {
		t.Fatal("diagnostic attempt/read mutated cache publication or durable state")
	}
}
func TestIntegrationCollectionAmbiguityAndMissingHistoryStayUnknown(t *testing.T) {
	store, _, _ := fixture(t)
	providers := integration.NewRegistry(diagnosticIntegrationTrap{"duplicate"}, diagnosticIntegrationTrap{"duplicate"}, diagnosticIntegrationTrap{"unobserved"})
	attempt := protocol.DiagnosticCollectionAttempt{StartedAt: "2026-11-16T17:01:00Z", FinishedAt: "2026-11-16T17:01:02Z", Outcome: "skipped_before_collection"}
	store.RecordCollectionAttempts(map[string]protocol.DiagnosticCollectionAttempt{"duplicate": attempt, "historical": attempt})
	page, err := Read(store, protocol.DiagnosticsRequest{Section: "integrations"}, false, providers)
	if err != nil || page.Total != 3 {
		t.Fatalf("page=%+v %v", page, err)
	}
	for _, item := range page.Items {
		v := item.Integration
		switch v.Name {
		case "duplicate":
			if v.CollectionEvidence != "ambiguous_registration" || v.Collection != nil {
				t.Fatalf("ambiguous identity leaked attempt=%+v", v)
			}
		case "unobserved":
			if v.CollectionEvidence != "unknown_no_current_daemon_attempt" || v.Collection != nil {
				t.Fatal("missing attempt became success")
			}
		case "historical":
			if v.RegistrationCount != 0 || v.Collection == nil || v.Collection.Latest != attempt || v.Collection.LastCompleteAt != "" {
				t.Fatalf("historical/skip=%+v", v)
			}
		}
	}
}
