package state

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"radar/internal/protocol"
)

func newManualTestStore(t *testing.T) *Store {
	t.Helper()
	t.Setenv("RADAR_STATE", filepath.Join(t.TempDir(), "tasks.json"))
	store, err := NewStore(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestManualTaskLifecyclePersistsWithoutSources(t *testing.T) {
	store := newManualTestStore(t)
	created, err := store.CreateManualTask("Write the release process in Notion")
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == 0 || created.Attention != "low_priority" || created.Title != "Write the release process in Notion" || len(created.SourceRefs) != 0 {
		t.Fatalf("created task = %+v", created)
	}
	createdID := created.ID
	revision := store.Revision()

	store.SetTasks(nil)
	if got := store.Tasks(); len(got) != 1 || got[0].ID != createdID {
		t.Fatalf("tasks after refresh = %+v", got)
	}

	done, err := store.CompleteManualTask(createdID)
	if err != nil {
		t.Fatal(err)
	}
	if done.Attention != "done" || done.Metadata["manual_complete"] != "true" || store.Revision() <= revision {
		t.Fatalf("done task = %+v, revision %d", done, store.Revision())
	}
	reopened, err := store.ReopenManualTask(createdID)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Attention != "low_priority" || reopened.ID != createdID {
		t.Fatalf("reopened task = %+v", reopened)
	}

	reloaded, err := NewStore(store.logger)
	if err != nil {
		t.Fatal(err)
	}
	got := reloaded.Tasks()
	if len(got) != 1 || got[0].ID != createdID || got[0].Attention != "low_priority" {
		t.Fatalf("reloaded tasks = %+v", got)
	}
}

func TestAttachAssociationMergesIntoManualTaskAndPreservesID(t *testing.T) {
	store := newManualTestStore(t)
	jira := protocol.Task{
		Kind: "jira_issue", Title: "DPSCAP-123 Implement release process", Attention: "in_progress", Reason: "In Progress",
		SourceRefs: []protocol.SourceRef{{
			ID: "jira:issue:DPSCAP-123", EntityID: "jira:issue:DPSCAP-123", Source: "jira", Kind: "issue", Role: protocol.SourceRefRoleAuthoritative,
			Lifecycle: protocol.SourceRefLifecycleWorkItem, Presentation: protocol.SourceRefPresentation{PreferTitle: true}, Title: "DPSCAP-123 Implement release process",
			CanonicalKey: "jira:issue:DPSCAP-123", LinkingKeys: []string{"mark:DPSCAP-123"}, Signal: "in_progress", Status: "In Progress",
		}},
	}
	store.SetTasks([]protocol.Task{jira})
	manual, err := store.CreateManualTask("Refine the authentication epic in Jira")
	if err != nil {
		t.Fatal(err)
	}

	attached, err := store.AttachAssociation(manual.ID, testAssociation("DPSCAP-123"))
	if err != nil {
		t.Fatal(err)
	}
	if attached.ID != manual.ID || attached.Title != jira.Title || attached.Metadata["manual_title"] != manual.Title {
		t.Fatalf("attached task = %+v", attached)
	}
	if len(store.Tasks()) != 1 || len(store.Records()) != 1 || len(attached.SourceRefs) != 1 {
		t.Fatalf("merged state: tasks=%+v records=%+v refs=%+v", store.Tasks(), store.Records(), store.SourceRefs())
	}
	if _, err := store.CompleteManualTask(manual.ID); err == nil {
		t.Fatal("CompleteManualTask() error = nil after Jira attachment")
	}

	store.SetTasks([]protocol.Task{jira})
	got := store.Tasks()
	if len(got) != 1 || got[0].ID != manual.ID || got[0].Title != jira.Title {
		t.Fatalf("tasks after Jira refresh = %+v", got)
	}
}

func TestAttachAssociationLinksLaterMarkedReferences(t *testing.T) {
	store := newManualTestStore(t)
	manual, err := store.CreateManualTask("Plan DPSCAP-88")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AttachAssociation(manual.ID, testAssociation("DPSCAP-88")); err != nil {
		t.Fatal(err)
	}
	store.SetTasks([]protocol.Task{{
		Title: "DPSCAP-88 Planned work", Attention: "low_priority", Reason: "Selected for Development",
		SourceRefs: []protocol.SourceRef{{ID: "jira:issue:DPSCAP-88", EntityID: "jira:issue:DPSCAP-88", Source: "jira", Kind: "issue", Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkItem, Presentation: protocol.SourceRefPresentation{PreferTitle: true}, Title: "DPSCAP-88 Planned work", LinkingKeys: []string{"mark:DPSCAP-88"}, Signal: "low_priority"}},
	}})
	got := store.Tasks()
	if len(got) != 1 || got[0].ID != manual.ID || len(got[0].Associations) != 1 || got[0].Associations[0].CanonicalKey != "mark:DPSCAP-88" {
		t.Fatalf("linked tasks = %+v", got)
	}
}

func testAssociation(key string) protocol.TaskAssociation {
	return protocol.TaskAssociation{
		Source: "jira", ExternalID: key, CanonicalKey: "mark:" + key,
		LinkingKeys: []string{"mark:" + key}, Lifecycle: protocol.SourceRefLifecycleWorkItem,
	}
}
