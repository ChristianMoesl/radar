package taskdb

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"radar/internal/protocol"
)

func fixture(t *testing.T) (string, Task) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "state.db")
	t.Setenv("RADAR_STATE", path)
	return path, Task{ID: "one", Title: "Plan", Path: filepath.Join(root, "Plan.md"), Vault: root, State: "open", Priority: "normal", CreatedAt: "2026-10-01T10:00:00Z"}
}

func TestDurablePreferencesBindingsRevisionsAndRollback(t *testing.T) {
	path, one := fixture(t)
	one.Muted = true
	one.AgentReview = true
	one.Bindings = []protocol.SourceBinding{{Source: "jira", Kind: "issue", ID: "jira:issue:ABC-123", WorkItem: true}}
	if err := Put(&one); err != nil {
		t.Fatal(err)
	}
	stale := one
	one.Priority = "urgent"
	if err := Put(&one); err != nil {
		t.Fatal(err)
	}
	stale.Muted = false
	if err := Put(&stale); err == nil {
		t.Fatal("stale writer committed")
	}
	two := one
	two.ID = "two"
	two.Title = "Other"
	two.Path = filepath.Join(one.Vault, "Other.md")
	two.Revision = 0
	if err := Put(&two); err == nil {
		t.Fatal("duplicate binding owner committed")
	}
	if two.Revision != 0 {
		t.Fatal("failed transaction advanced caller revision")
	}
	items, err := List(one.Vault)
	if err != nil || len(items) != 1 || !items[0].Muted || !items[0].AgentReview || items[0].Priority != "urgent" {
		t.Fatalf("durable state lost: %+v %v", items, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("unsafe database permissions", err)
	}
	backup := filepath.Join(t.TempDir(), "backup.db")
	if err := Backup(path, backup); err != nil {
		t.Fatal(err)
	}
	if err := Integrity(backup); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RADAR_STATE", backup)
	items, err = List(one.Vault)
	if err != nil || len(items) != 1 || items[0].Revision != one.Revision {
		t.Fatal("backup lost task revision", err)
	}
}

func TestUnknownSchemaAndCorruptionArePreserved(t *testing.T) {
	path, _ := fixture(t)
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version=999"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	before, _ := os.ReadFile(path)
	if _, err := Open(path); err == nil {
		t.Fatal("unknown schema accepted")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("unknown schema was modified")
	}
}

func TestMoveRecoveryAfterRenameAndBeforePublication(t *testing.T) {
	_, task := fixture(t)
	if err := Put(&task); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(task.Path, []byte("notes"), 0o640); err != nil {
		t.Fatal(err)
	}
	old := task.Path
	task.Path = filepath.Join(task.Vault, "Renamed.md")
	data, _ := json.Marshal(task)
	db, err := Current()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO file_moves VALUES(?,?,?,?)", task.ID, old, task.Path, data); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err := os.Rename(old, task.Path); err != nil {
		t.Fatal(err)
	}
	if err := RecoverMoves(os.Rename); err != nil {
		t.Fatal(err)
	}
	items, err := List(task.Vault)
	if err != nil || len(items) != 1 || items[0].Path != task.Path {
		t.Fatal("move association not recovered", err)
	}
	if err := RecoverMoves(os.Rename); err != nil {
		t.Fatal("repeat recovery failed", err)
	}
}

func TestTrashedTasksReleaseActiveTitleAndBindingOwnership(t *testing.T) {
	_, old := fixture(t)
	old.Bindings = []protocol.SourceBinding{{Source: "jira", Kind: "issue", ID: "jira:issue:ABC-123"}}
	if err := Put(&old); err != nil {
		t.Fatal(err)
	}
	old.Trashed = true
	if err := Put(&old); err != nil {
		t.Fatal(err)
	}
	replacement := old
	replacement.ID = "new"
	replacement.Revision = 0
	replacement.Trashed = false
	if err := Put(&replacement); err != nil {
		t.Fatal("trashed record prevented new adoption", err)
	}
	old.Trashed = false
	if err := Put(&old); err == nil {
		t.Fatal("restore overwrote replacement ownership")
	}
}
