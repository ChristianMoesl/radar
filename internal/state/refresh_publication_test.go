package state

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"radar/internal/protocol"
	"radar/internal/taskdb"
)

func TestLinkGroupsKeepTransitiveBindingsAndIgnoreInactiveOrInformationalRefs(t *testing.T) {
	ref := func(id string) SourceRefRecord {
		return SourceRefRecord{ID: id, TaskRecordID: id, Active: true, Snapshot: protocol.SourceRef{ID: id, Source: "local", Kind: "item", BindingKey: id, Role: protocol.SourceRefRoleAuthoritative}}
	}
	a, b, c, d, inactive, info := ref("a"), ref("b"), ref("c"), ref("d"), ref("inactive"), ref("info")
	b.Snapshot.LinkingKeys = []string{a.Snapshot.Binding().LinkingKey(), " shared "}
	c.Snapshot.LinkingKeys = []string{"shared", "shared", ""}
	inactive.Active = false
	inactive.Snapshot.LinkingKeys = []string{"shared"}
	info.Snapshot.Role = protocol.SourceRefRoleInformational
	info.Snapshot.LinkingKeys = []string{d.Snapshot.Binding().LinkingKey()}
	refs := []SourceRefRecord{a, b, c, d, inactive, info}
	groups := func() [][]string {
		var ids [][]string
		for _, group := range sourceRefLinkGroups(refs) {
			var row []string
			for _, r := range group {
				row = append(row, r.ID)
			}
			ids = append(ids, row)
		}
		return ids
	}
	if got := groups(); !reflect.DeepEqual(got, [][]string{{"a", "b", "c"}, {"d"}}) {
		t.Fatalf("groups=%v", got)
	}
	refs[0].Snapshot.BindingKey = "new-lifetime"
	if got := groups(); !reflect.DeepEqual(got, [][]string{{"a"}, {"b", "c"}, {"d"}}) {
		t.Fatalf("replacement inherited old binding: %v", got)
	}
}

func TestCollectionPublishesTaskAndStatusInOneRevisionAndMetadataLeavesRowsUntouched(t *testing.T) {
	t.Setenv("RADAR_STATE", filepath.Join(t.TempDir(), "state.db"))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}
	task := makeTask("in_progress", "work", testJiraIssueRef("jira:issue:ABC-1", "Example"))
	before := store.Revision()
	store.SetCollection([]protocol.Task{task}, nil, []protocol.SourceStatus{{Name: "jira", Status: "ok", SourceRefCount: 1}})
	tasks, statuses, revision := store.Snapshot()
	if revision != before+1 || len(tasks) != 1 || len(statuses) != 1 || statuses[0].SourceRefCount != 1 {
		t.Fatalf("incoherent publication: tasks=%d statuses=%v revision=%d", len(tasks), statuses, revision)
	}
	db, err := taskdb.Open(store.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE row_writes(n INTEGER); INSERT INTO row_writes VALUES(0);
CREATE TRIGGER task_insert AFTER INSERT ON task_records BEGIN UPDATE row_writes SET n=n+1; END;
CREATE TRIGGER task_delete AFTER DELETE ON task_records BEGIN UPDATE row_writes SET n=n+1; END;
CREATE TRIGGER ref_insert AFTER INSERT ON observed_refs BEGIN UPDATE row_writes SET n=n+1; END;
CREATE TRIGGER ref_delete AFTER DELETE ON observed_refs BEGIN UPDATE row_writes SET n=n+1; END;`); err != nil {
		t.Fatal(err)
	}
	statuses[0].Status = "partial"
	store.SetSources(statuses)
	var writes int
	if err := db.QueryRow("SELECT n FROM row_writes").Scan(&writes); err != nil {
		t.Fatal(err)
	}
	if writes != 0 {
		t.Fatalf("status update rewrote %d task/reference rows", writes)
	}
	revision = store.Revision()
	store.SetSources(statuses)
	if store.Revision() != revision {
		t.Fatal("unchanged status generated another publication")
	}
	reloaded, err := NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Tasks()) != 1 || reloaded.Tasks()[0].ID != tasks[0].ID || !reflect.DeepEqual(reloaded.Sources(), statuses) {
		t.Fatal("metadata update failed restart round trip")
	}
}

func TestStatusRetryRecoversFailedTaskProjection(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	t.Setenv("RADAR_STATE", filepath.Join(dir, "state.db"))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}
	statuses := []protocol.SourceStatus{{Name: "jira", Status: "ok"}}
	first := makeTask("in_progress", "work", testJiraIssueRef("jira:issue:ABC-1", "First"))
	second := makeTask("in_progress", "work", testJiraIssueRef("jira:issue:ABC-2", "Second"))
	store.SetCollection([]protocol.Task{first}, nil, statuses)
	if err := os.Rename(dir, dir+"-saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	store.SetTasks([]protocol.Task{first, second})
	if !store.saveFailed {
		t.Fatal("fixture did not fail persistence")
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(dir+"-saved", dir); err != nil {
		t.Fatal(err)
	}
	store.SetSources(statuses)
	reloaded, err := NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Tasks()) != 2 {
		t.Fatal("status retry saved metadata without recovering the failed task write")
	}
}
