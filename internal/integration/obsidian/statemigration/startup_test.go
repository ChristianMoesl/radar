package statemigration

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"radar/internal/protocol"
	"radar/internal/state"
	"radar/internal/taskdb"
)

func startupLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestStartupMigratesGroupedNotesWithoutGuessingBindingOwnership(t *testing.T) {
	p := migrationFixture(t)
	const second = "22345678-1234-4234-8234-123456789abc"
	archive := filepath.Join(p.Vault, "Tasks", "Archived")
	if err := os.Mkdir(archive, 0o755); err != nil {
		t.Fatal(err)
	}
	secondPath := filepath.Join(archive, "Completed.md")
	content := strings.NewReplacer(
		p.Notes[0].Task.ID, second,
		"radar-title: 'Plan'", "radar-title: 'Completed'",
		"radar-state: open", "radar-state: done",
		"radar-muted: true", "radar-muted: false",
		"radar-agent-review: true", "radar-agent-review: false",
		"radar-completed-at:\r\n", "radar-completed-at: 2026-10-01T10:00:00Z\r\n",
		"ABC-123", "ABC-456",
	).Replace(string(p.Notes[0].Before))
	refs := []protocol.SourceRef{
		{ID: "obsidian:task:" + p.Notes[0].Task.ID, Source: "obsidian", Kind: "task", Authored: true, Role: protocol.SourceRefRoleAuthoritative},
		{ID: "obsidian:task:" + second, Source: "obsidian", Kind: "task", Authored: true, Role: protocol.SourceRefRoleAuthoritative},
		{ID: "jira:issue:ABC-123", Source: "jira", Kind: "issue", Role: protocol.SourceRefRoleAuthoritative},
		{ID: "jira:issue:ABC-456", Source: "jira", Kind: "issue", Role: protocol.SourceRefRoleAuthoritative},
		{ID: "github:pr:acme/app:7", Source: "github", Kind: "pr", Role: protocol.SourceRefRoleAuthoritative},
	}
	legacy := struct {
		Version    int                     `json:"version"`
		NextTaskID int                     `json:"next_task_id"`
		Records    []state.TaskRecord      `json:"task_records"`
		SourceRefs []state.SourceRefRecord `json:"source_refs"`
	}{Version: 8, NextTaskID: 7, Records: []state.TaskRecord{{
		ID: "task:7", NumericID: 7, CanonicalKey: "mark:ABC-123", Kind: "linking_mark", State: "active",
		Ack: state.TaskAckState{Cursor: "2026-10-01T10:00:00Z"}, AgentReviewPending: true,
		Snapshot: protocol.Task{SourceRefs: refs[:2]},
	}}}
	for _, ref := range refs {
		legacy.Records[0].SourceRefIDs = append(legacy.Records[0].SourceRefIDs, ref.ID)
		legacy.SourceRefs = append(legacy.SourceRefs, state.SourceRefRecord{ID: ref.ID, Source: ref.Source, Kind: ref.Kind, TaskRecordID: "task:7", Active: true, Snapshot: ref})
	}
	cache, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.LegacyPath, cache, 0o600); err != nil {
		t.Fatal(err)
	}
	// Grouping must not hide a cached authored identity whose note is missing.
	if _, err := Preflight(p.Database, p.Vault, p.LegacyPath, p.WorkspaceRoot); err == nil || !strings.Contains(err.Error(), "has no note") {
		t.Fatalf("missing grouped note was not rejected: %v", err)
	}
	if err := os.WriteFile(secondPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err = Preflight(p.Database, p.Vault, p.LegacyPath, p.WorkspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Notes) != 2 || len(p.Warnings) != 1 {
		t.Fatalf("grouped import plan: %d notes, warnings %v", len(p.Notes), p.Warnings)
	}
	if _, err := os.Stat(p.Database); !os.IsNotExist(err) {
		t.Fatal("preflight created a database")
	}
	for _, note := range p.Notes {
		data, err := os.ReadFile(note.Task.Path)
		if err != nil || !bytes.Equal(data, note.Before) {
			t.Fatal("preflight changed a note", err)
		}
		if len(note.Task.Bindings) != 1 {
			t.Fatalf("group associations were promoted to a note: %+v", note.Task.Bindings)
		}
	}
	for range 2 { // Initial cutover, then an ordinary restart.
		if err := Ensure(p.Database, p.Vault, p.WorkspaceRoot, startupLogger()); err != nil {
			t.Fatal(err)
		}
	}
	for _, note := range p.Notes {
		task, err := taskdb.Find(note.Task.ID)
		want := note.Task
		want.Revision = 1
		if err != nil || !reflect.DeepEqual(task, want) {
			t.Fatalf("note authority changed: %+v, want %+v (%v)", task, want, err)
		}
		data, err := os.ReadFile(note.Task.Path)
		if err != nil || !bytes.Equal(data, note.After) {
			t.Fatal("migration changed note content", err)
		}
	}
	projection, err := taskdb.ReadProjection(p.Database)
	if err != nil {
		t.Fatal(err)
	}
	var before, after any
	if err := json.Unmarshal(cache, &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(projection, &after); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("migration changed grouped identity, observations or acknowledgement state")
	}
	data, err := os.ReadFile(p.LegacyPath)
	if err != nil || !bytes.Equal(data, cache) {
		t.Fatal("migration changed the original cache", err)
	}
	backup, err := os.ReadFile(filepath.Join(p.BackupPath(), "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved Plan
	if err := json.Unmarshal(backup, &saved); err != nil || !reflect.DeepEqual(saved.Notes, p.Notes) {
		t.Fatal("grouped notes were not backed up exactly", err)
	}
	if _, err := state.NewStore(startupLogger()); err != nil {
		t.Fatal("runtime rejected migrated grouped state", err)
	}
}

func TestStartupMigratesOnceAndKeepsSubsequentDecisions(t *testing.T) {
	p := migrationFixture(t)
	if err := Ensure(p.Database, p.Vault, p.WorkspaceRoot, startupLogger()); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(p.Notes[0].Task.Path)
	if err != nil || !bytes.Equal(after, p.Notes[0].After) {
		t.Fatalf("startup did not preserve content: %q %v", after, err)
	}
	backup, err := os.ReadFile(filepath.Join(p.BackupPath(), "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	items, err := taskdb.List(p.Vault)
	if err != nil || len(items) != 1 || !items[0].Muted || !items[0].AgentReview {
		t.Fatalf("lost imported decisions: %+v %v", items, err)
	}
	items[0].Title = "Changed after upgrade"
	if err := taskdb.Put(&items[0]); err != nil {
		t.Fatal(err)
	}
	// Completed upgrades ignore the retained legacy file, even if it is edited.
	if err := os.WriteFile(p.LegacyPath, []byte("obsolete cache"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(p.Database, p.Vault, p.WorkspaceRoot, startupLogger()); err != nil {
		t.Fatal(err)
	}
	items, err = taskdb.List(p.Vault)
	if err != nil || len(items) != 1 || items[0].Title != "Changed after upgrade" || items[0].Revision != 2 {
		t.Fatalf("startup reimported old authority: %+v %v", items, err)
	}
	retained, err := os.ReadFile(filepath.Join(p.BackupPath(), "manifest.json"))
	if err != nil || !bytes.Equal(backup, retained) {
		t.Fatal("startup replaced the original backup", err)
	}
}

func TestStartupResumesBeforeAndAfterImportCommit(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "before commit", true: "after commit"}[committed], func(t *testing.T) {
			p := migrationFixture(t)
			data, _ := json.Marshal(p)
			if err := os.Mkdir(p.BackupPath(), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := writeNew(filepath.Join(p.BackupPath(), "manifest.json"), data); err != nil {
				t.Fatal(err)
			}
			if committed {
				if err := p.importRecords(data); err != nil {
					t.Fatal(err)
				}
				// Model a committed import interrupted during note stripping.
				if err := os.WriteFile(p.Notes[0].Task.Path, p.Notes[0].Before, p.Notes[0].Mode); err != nil {
					t.Fatal(err)
				}
				db, err := taskdb.Open(p.Database)
				if err != nil {
					t.Fatal(err)
				}
				_, err = db.Exec("DELETE FROM documents WHERE name='migration_complete'")
				if err == nil {
					_, err = db.Exec("INSERT INTO documents(name,data) VALUES('migration_pending',?)", data)
				}
				db.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := Ensure(p.Database, p.Vault, p.WorkspaceRoot, startupLogger()); err != nil {
				t.Fatal(err)
			}
			items, err := taskdb.List(p.Vault)
			if err != nil || len(items) != 1 || items[0].Revision != 1 {
				t.Fatalf("recovery duplicated or lost tasks: %+v %v", items, err)
			}
			after, err := os.ReadFile(p.Notes[0].Task.Path)
			if err != nil || !bytes.Equal(after, p.Notes[0].After) {
				t.Fatal("recovery did not finish note stripping", err)
			}
		})
	}
}

func TestStartupRejectsConflictsBeforeWrites(t *testing.T) {
	for _, input := range []string{"malformed note", "missing title", "unsupported cache", "existing database", "symlinked notes only"} {
		t.Run(input, func(t *testing.T) {
			p := migrationFixture(t)
			switch input {
			case "malformed note":
				if err := os.WriteFile(p.Notes[0].Task.Path, []byte("---\nradar-id: invalid\n---\nKeep me"), p.Notes[0].Mode); err != nil {
					t.Fatal(err)
				}
			case "missing title":
				before := bytes.Replace(p.Notes[0].Before, []byte("radar-title: 'Plan'\r\n"), nil, 1)
				if err := os.WriteFile(p.Notes[0].Task.Path, before, p.Notes[0].Mode); err != nil {
					t.Fatal(err)
				}
			case "unsupported cache":
				if err := os.WriteFile(p.LegacyPath, []byte(`{"version":7}`), 0o600); err != nil {
					t.Fatal(err)
				}
			case "existing database":
				db, err := taskdb.Open(p.Database)
				if err != nil {
					t.Fatal(err)
				}
				db.Close()
			case "symlinked notes only":
				if err := os.Remove(p.LegacyPath); err != nil {
					t.Fatal(err)
				}
				original := p.Notes[0].Task.Path + ".original"
				if err := os.Rename(p.Notes[0].Task.Path, original); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(original, p.Notes[0].Task.Path); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(p.Notes[0].Task.Path)
			if err := Ensure(p.Database, p.Vault, p.WorkspaceRoot, startupLogger()); err == nil {
				t.Fatal("unsafe migration accepted")
			}
			after, _ := os.ReadFile(p.Notes[0].Task.Path)
			if !bytes.Equal(before, after) {
				t.Fatal("failed startup changed note bytes")
			}
			if _, err := os.Stat(p.BackupPath()); !os.IsNotExist(err) {
				t.Fatal("failed preflight created backup")
			}
			if input != "existing database" {
				if _, err := os.Stat(p.Database); !os.IsNotExist(err) {
					t.Fatal("failed preflight created database")
				}
			}
		})
	}
}

func TestStartupRecoveryRefusesExternalEditsAndChangedConfiguration(t *testing.T) {
	p := migrationFixture(t)
	data, _ := json.Marshal(p)
	if err := os.Mkdir(p.BackupPath(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeNew(filepath.Join(p.BackupPath(), "manifest.json"), data); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(p.Database, p.Vault, p.WorkspaceRoot+"-changed", startupLogger()); err == nil || !strings.Contains(err.Error(), "configuration changed") {
		t.Fatal("startup used obsolete configuration", err)
	}
	edits := []byte("New user content")
	if err := os.WriteFile(p.Notes[0].Task.Path, edits, p.Notes[0].Mode); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(p.Database, p.Vault, p.WorkspaceRoot, startupLogger()); err == nil {
		t.Fatal("recovery accepted externally edited notes")
	}
	after, _ := os.ReadFile(p.Notes[0].Task.Path)
	if !bytes.Equal(after, edits) {
		t.Fatal("recovery overwrote external edits")
	}
}

func TestStartupDoesNotUndoOrRepeatRollback(t *testing.T) {
	p := migrationFixture(t)
	if err := p.Apply(); err != nil {
		t.Fatal(err)
	}
	if err := taskdb.WriteDocument(p.Database, "rollback_pending", []byte("true")); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(p.Database, p.Vault, p.WorkspaceRoot, startupLogger()); err == nil || !strings.Contains(err.Error(), "rollback") {
		t.Fatal("startup undid an in-progress rollback", err)
	}
	// Model a crash after the rollback marker was synced but before the
	// database journal was removed; explicit rollback must still finish.
	if err := writeNew(filepath.Join(p.BackupPath(), "rolled-back"), []byte("true\n")); err != nil {
		t.Fatal(err)
	}
	if err := Rollback(p.Database); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(p.Database, p.Vault, p.WorkspaceRoot, startupLogger()); err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatal("startup repeated an intentionally rolled-back migration", err)
	}
}

func TestFreshStartupSkipsMigrationAndCacheOnlyUpgradeAllowsAbsentTasks(t *testing.T) {
	root := t.TempDir()
	database, vault := filepath.Join(root, "state.db"), filepath.Join(root, "vault")
	if err := os.Mkdir(vault, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(database, vault, root, startupLogger()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(database); !os.IsNotExist(err) {
		t.Fatal("fresh startup created a migration database")
	}
	cache := []byte(`{"version":8,"next_task_id":0,"task_records":[],"source_refs":[]}`)
	if err := os.WriteFile(filepath.Join(root, "tasks.json"), cache, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(database, vault, root, startupLogger()); err != nil {
		t.Fatal("cache-only installation failed", err)
	}
	data, err := taskdb.ReadProjection(database)
	if err != nil || !bytes.Contains(data, []byte(`"version":8`)) {
		t.Fatal("legacy projection was not imported", err)
	}
}
