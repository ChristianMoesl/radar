package statemigration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/integration"
	"radar/internal/integration/obsidian"
	"radar/internal/state"
	"radar/internal/taskdb"
)

func migrationFixture(t *testing.T) Plan {
	t.Helper()
	root := t.TempDir()
	vault := filepath.Join(root, "vault")
	workspace := filepath.Join(root, "workspaces")
	t.Setenv("RADAR_STATE", filepath.Join(root, "state.db"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	if err := os.MkdirAll(filepath.Join(root, "config", "radar"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "radar", "config.yaml"), []byte(fmt.Sprintf("workspace: {root_dir: %q}\n", workspace)), 0o600); err != nil {
		t.Fatal(err)
	}
	id := "12345678-1234-4234-8234-123456789abc"
	path := filepath.Join(vault, "Tasks", "Plan--12345678", "Plan.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\r\nradar-id: " + id + "\r\nradar-title: 'Plan'\r\nradar-state: open\r\nradar-priority: urgent\r\nradar-created-at: 2026-08-01T10:00:00Z\r\nradar-completed-at:\r\nradar-muted: true\r\nradar-agent-review: true\r\nradar-completion-baseline: pending\r\nradar-source-refs:\r\n  - source: jira\r\n    kind: issue\r\n    id: jira:issue:ABC-123\r\n    work_item: true\r\n# user separator\r\ncustom: {owner: Example}\r\n---\r\n\nUser notes.\r\nNo final newline"
	if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(root, "tasks.json")
	cache := `{"version":8,"next_task_id":7,"task_records":[{"id":"obsidian:task:` + id + `","numeric_id":7,"canonical_key":"obsidian:task:` + id + `","state":"active","source_ref_ids":[],"ack":{"cursor":"2026-10-01T10:00:00Z"},"agent_review_pending":true,"snapshot":{}}],"source_refs":[]}`
	if err := os.WriteFile(legacy, []byte(cache), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := Preflight(os.Getenv("RADAR_STATE"), vault, legacy, workspace)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestImportPreservesStateAndContentThroughRestartResetAndBackup(t *testing.T) {
	p := migrationFixture(t)
	if _, err := os.Stat(p.Database); !os.IsNotExist(err) {
		t.Fatal("preflight created database")
	}
	if err := p.Apply(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(p.Notes[0].Task.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, p.Notes[0].After) || strings.Contains(string(after), "radar-") || !strings.HasSuffix(string(after), "User notes.\r\nNo final newline") {
		t.Fatalf("destructive stripping: %q", after)
	}
	if !bytes.Contains(after, []byte("# user separator\r\ncustom: {owner: Example}\r\n")) {
		t.Fatal("user frontmatter or comments changed")
	}
	info, _ := os.Stat(p.Notes[0].Task.Path)
	if info.Mode().Perm() != p.Notes[0].Mode {
		t.Fatal("permissions changed")
	}
	tasks, err := taskdb.List(p.Vault)
	if err != nil || len(tasks) != 1 || !tasks[0].Muted || !tasks[0].AgentReview || tasks[0].CompletionBaseline != "pending" || len(tasks[0].Bindings) != 1 {
		t.Fatalf("state import: %+v %v", tasks, err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := state.NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Reset(); err != nil {
		t.Fatal(err)
	}
	if _, err := state.NewStore(logger); err != nil {
		t.Fatal("restart failed", err)
	}
	projection, err := taskdb.ReadProjection(p.Database)
	if err != nil || !bytes.Contains(projection, []byte("2026-10-01T10:00:00Z")) || !bytes.Contains(projection, []byte(`"agent_review_pending":true`)) {
		t.Fatal("reset lost acknowledgement/pending hold", err)
	}
	result := obsidian.NewSourceAt(p.Vault).Collect(context.Background(), integration.CollectRequest{})
	if !result.Complete || len(result.Observations) != 1 || !result.Observations[0].Ref.Muted {
		t.Fatal("production runtime did not read imported authority", result)
	}
	backup, err := os.ReadFile(filepath.Join(p.BackupPath(), "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved Plan
	if err := json.Unmarshal(backup, &saved); err != nil || !bytes.Equal(saved.Notes[0].Before, p.Notes[0].Before) {
		t.Fatal("backup is incomplete", err)
	}
}

func TestResumeStrippingAfterCommittedImportAndRejectExternalEdits(t *testing.T) {
	p := migrationFixture(t)
	data, _ := json.Marshal(p)
	if err := os.MkdirAll(p.BackupPath(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeNew(filepath.Join(p.BackupPath(), "manifest.json"), data); err != nil {
		t.Fatal(err)
	}
	db, err := taskdb.Open(p.Database)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	task := p.Notes[0].Task
	if err := taskdb.PutTx(tx, &task); err != nil {
		t.Fatal(err)
	}
	if err := taskdb.WriteProjectionTx(tx, p.Cache); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("INSERT INTO documents VALUES('migration_pending',?)", data); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := state.NewStore(slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
		t.Fatal("runtime accepted unfinished cutover")
	}
	if err := os.WriteFile(task.Path, []byte("new user edits"), p.Notes[0].Mode); err != nil {
		t.Fatal(err)
	}
	if err := Resume(p.Database); err == nil {
		t.Fatal("resume overwrote edited note")
	}
	if err := os.WriteFile(task.Path, p.Notes[0].After, p.Notes[0].Mode); err != nil {
		t.Fatal(err)
	}
	if err := Resume(p.Database); err != nil {
		t.Fatal(err)
	}
	items, err := taskdb.List(p.Vault)
	if err != nil || len(items) != 1 {
		t.Fatal("resume duplicated tasks", err)
	}
}

func TestResumeAfterBackupBeforeImportAndMalformedInputsAreReadOnly(t *testing.T) {
	p := migrationFixture(t)
	data, _ := json.Marshal(p)
	if err := os.Mkdir(p.BackupPath(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeNew(filepath.Join(p.BackupPath(), "manifest.json"), data); err != nil {
		t.Fatal(err)
	}
	if err := Resume(p.Database); err != nil {
		t.Fatal("pre-import crash recovery failed", err)
	}
	p = migrationFixture(t)
	if err := os.WriteFile(p.Notes[0].Task.Path, []byte("---\nradar-id: invalid\n---\nPrivate body"), p.Notes[0].Mode); err != nil {
		t.Fatal(err)
	}
	if _, err := Preflight(p.Database, p.Vault, p.LegacyPath, p.WorkspaceRoot); err == nil {
		t.Fatal("malformed input accepted")
	}
	if _, err := os.Stat(p.Database); !os.IsNotExist(err) {
		t.Fatal("failed preflight created database")
	}
}

func TestRollbackRestoresOriginalBytesAndRejectsNewDecisions(t *testing.T) {
	p := migrationFixture(t)
	if err := p.Apply(); err != nil {
		t.Fatal(err)
	}
	if err := Rollback(p.Database); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(p.Notes[0].Task.Path)
	if err != nil || !bytes.Equal(data, p.Notes[0].Before) {
		t.Fatal("rollback did not restore exact bytes", err)
	}
	if _, err := os.Stat(p.Database); !os.IsNotExist(err) {
		t.Fatal("rollback retained incompatible database")
	}
	if err := Resume(p.Database); err == nil {
		t.Fatal("rolled-back import was resumed")
	}
	p = migrationFixture(t)
	if err := p.Apply(); err != nil {
		t.Fatal(err)
	}
	tasks, err := taskdb.List(p.Vault)
	if err != nil {
		t.Fatal(err)
	}
	tasks[0].Muted = false
	if err := taskdb.Put(&tasks[0]); err != nil {
		t.Fatal(err)
	}
	if err := Rollback(p.Database); err == nil {
		t.Fatal("rollback discarded a new decision")
	}
}

func TestAddedNotesAndCrossFieldAliasesFailBeforeWrites(t *testing.T) {
	p := migrationFixture(t)
	if err := os.WriteFile(filepath.Join(filepath.Dir(p.Notes[0].Task.Path), "Added.md"), []byte("new content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := p.Apply(); err == nil {
		t.Fatal("new note was omitted")
	}
	if _, err := os.Stat(p.Database); !os.IsNotExist(err) {
		t.Fatal("inventory failure created database")
	}
	p = migrationFixture(t)
	content := strings.Replace(string(p.Notes[0].Before), "radar-title: 'Plan'", "radar-title: &title 'Plan'", 1)
	content = strings.Replace(content, "custom: {owner: Example}", "custom: *title", 1)
	if err := os.WriteFile(p.Notes[0].Task.Path, []byte(content), p.Notes[0].Mode); err != nil {
		t.Fatal(err)
	}
	if _, err := Preflight(p.Database, p.Vault, p.LegacyPath, p.WorkspaceRoot); err == nil {
		t.Fatal("stripping would leave a dangling user alias")
	}
}

func TestMetadataOnlyNotesMigrateToSingleNewline(t *testing.T) {
	for _, body := range []string{"", "\n", "User content.\n"} {
		t.Run(fmt.Sprintf("body_%q", body), func(t *testing.T) {
			p := migrationFixture(t)
			path := p.Notes[0].Task.Path
			content := []byte("---\nradar-id: " + p.Notes[0].Task.ID + "\nradar-title: Plan\nradar-state: open\nradar-priority: normal\nradar-created-at: 2026-08-01T10:00:00Z\nradar-completed-at:\n---\n" + body)
			if err := os.WriteFile(path, content, p.Notes[0].Mode); err != nil {
				t.Fatal(err)
			}
			p, err := Preflight(p.Database, p.Vault, p.LegacyPath, p.WorkspaceRoot)
			if err != nil {
				t.Fatal(err)
			}
			want := body
			if want == "" {
				want = "\n"
			}
			if string(p.Notes[0].After) != want {
				t.Fatalf("planned content = %q, want %q", p.Notes[0].After, want)
			}
			if err := p.Apply(); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != want {
				t.Fatalf("migrated content = %q, want %q (%v)", data, want, err)
			}
			backup, err := os.ReadFile(filepath.Join(p.BackupPath(), "manifest.json"))
			if err != nil {
				t.Fatal(err)
			}
			var saved Plan
			if err := json.Unmarshal(backup, &saved); err != nil || !bytes.Equal(saved.Notes[0].Before, content) {
				t.Fatal("original empty-body note was not backed up exactly", err)
			}
			if err := Rollback(p.Database); err != nil {
				t.Fatal(err)
			}
			data, err = os.ReadFile(path)
			if err != nil || !bytes.Equal(data, content) {
				t.Fatal("rollback did not restore original bytes", err)
			}
		})
	}
}

func TestNotesOnlyImportIncludesFlatArchive(t *testing.T) {
	p := migrationFixture(t)
	if err := os.Remove(p.LegacyPath); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(p.Vault, "Tasks", "Archived")
	if err := os.Mkdir(archive, 0o755); err != nil {
		t.Fatal(err)
	}
	content := string(p.Notes[0].Before)
	for _, change := range [][2]string{{"12345678-1234-4234-8234-123456789abc", "22345678-1234-4234-8234-123456789abc"}, {"radar-title: 'Plan'", "radar-title: 'Completed'"}, {"radar-state: open", "radar-state: done"}, {"radar-agent-review: true", "radar-agent-review: false"}, {"radar-completed-at:\r\n", "radar-completed-at: 2026-10-01T10:00:00Z\r\n"}, {"ABC-123", "ABC-456"}} {
		content = strings.ReplaceAll(content, change[0], change[1])
	}
	if err := os.WriteFile(filepath.Join(archive, "Completed.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Preflight(p.Database, p.Vault, p.LegacyPath, p.WorkspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Notes) != 2 || len(p.Cache) != 0 {
		t.Fatal("notes-only inventory omitted archive")
	}
	if err := p.Apply(); err != nil {
		t.Fatal(err)
	}
	items, err := taskdb.List(p.Vault)
	if err != nil || len(items) != 2 || items[1].State != "done" {
		t.Fatal("archive lifecycle lost", err)
	}
}
