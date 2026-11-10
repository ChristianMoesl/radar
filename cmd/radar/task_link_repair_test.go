package main

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

func taskLinkRepairFixture(t *testing.T) (string, state.LinkRepairPlan, []byte) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "state.db")
	t.Setenv("RADAR_STATE", path)
	t.Setenv("RADAR_PID", filepath.Join(root, "absent.pid"))
	refs := []protocol.SourceRef{
		{ID: "obsidian:task:one", Source: "obsidian", Kind: "task", Authored: true, Role: protocol.SourceRefRoleAuthoritative},
		{ID: "obsidian:task:two", Source: "obsidian", Kind: "task", Authored: true, Role: protocol.SourceRefRoleAuthoritative},
		{ID: "jira:issue:ABC-7", Source: "jira", Kind: "issue", Role: protocol.SourceRefRoleAuthoritative},
	}
	record := state.TaskRecord{ID: "task:7", NumericID: 7, Ack: state.TaskAckState{Cursor: "saved"}, AgentReviewPending: true, Snapshot: protocol.Task{SourceRefs: refs}}
	var stored []state.SourceRefRecord
	for _, ref := range refs {
		record.SourceRefIDs = append(record.SourceRefIDs, ref.ID)
		stored = append(stored, state.SourceRefRecord{ID: ref.ID, Source: ref.Source, Kind: ref.Kind, TaskRecordID: record.ID, Active: true, Snapshot: ref})
	}
	data, _ := json.Marshal(map[string]any{"version": 8, "next_task_id": 7, "task_records": []state.TaskRecord{record}, "source_refs": stored})
	if err := taskdb.WriteProjection(path, data); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"one", "two"} {
		task := taskdb.Task{ID: id, Title: id, Path: filepath.Join(root, id+".md"), Vault: root, State: "open", AgentReview: true}
		if id == "one" {
			task.Bindings = []protocol.SourceBinding{refs[2].Binding()}
		}
		if err := taskdb.Put(&task); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(task.Path, []byte("Retained note content\n"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := previewTaskLinkRepair(path)
	if err != nil || len(plan.Conflicts) != 1 {
		t.Fatalf("repair preview: %+v (%v)", plan, err)
	}
	plan.Resolutions[0].KeepAuthoredID = refs[0].ID
	plan.Resolutions[0].AcknowledgedIDs = []string{refs[0].ID}
	plan.Resolutions[0].PendingReviewIDs = []string{refs[0].ID, refs[1].ID}
	plan.Resolutions[0].Assignments[refs[2].ID] = refs[0].ID
	data, _ = taskdb.ReadProjection(path)
	return path, plan, data
}

func TestTaskLinkRepairPreviewAndOfflineApplyPreserveAuthorityAndBackup(t *testing.T) {
	path, plan, before := taskLinkRepairFixture(t)
	one, _ := taskdb.Find("one")
	two, _ := taskdb.Find("two")
	info, _ := os.Stat(path)
	previewTaskLinkRepair(path)
	unchanged, _ := taskdb.ReadProjection(path)
	afterInfo, _ := os.Stat(path)
	if !bytes.Equal(unchanged, before) || info.Mode() != afterInfo.Mode() {
		t.Fatal("preview changed state")
	}
	backup := filepath.Join(t.TempDir(), "backup.db")
	if err := applyTaskLinkRepair(path, backup, plan); err != nil {
		t.Fatal(err)
	}
	backedUp, err := taskdb.ReadProjection(backup)
	if err != nil || !bytes.Equal(backedUp, before) {
		t.Fatal("original invalid projection missing from backup", err)
	}
	store, err := state.NewStore(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil || len(store.Records()) != 2 {
		t.Fatal("repaired store cannot load", err)
	}
	for _, want := range []taskdb.Task{one, two} {
		got, err := taskdb.Find(want.ID)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatal("repair changed authored identity, review or binding intent", err)
		}
		data, _ := os.ReadFile(want.Path)
		info, _ := os.Stat(want.Path)
		if string(data) != "Retained note content\n" || info.Mode().Perm() != 0o640 {
			t.Fatal("repair changed a note")
		}
	}
	db, _ := taskdb.Current()
	defer db.Close()
	var journal []byte
	if err := db.QueryRow("SELECT data FROM documents WHERE name=?", "task_link_repair:"+plan.ProjectionSHA256).Scan(&journal); err != nil || !bytes.Contains(journal, []byte(`"before"`)) {
		t.Fatal("repair did not journal original history", err)
	}
}

func TestTaskLinkRepairRefusesActiveLockStalenessAndBindingTransfer(t *testing.T) {
	for _, scenario := range []string{"locked", "stale", "binding", "existing backup", "incomplete"} {
		t.Run(scenario, func(t *testing.T) {
			path, plan, before := taskLinkRepairFixture(t)
			backup := filepath.Join(t.TempDir(), "backup.db")
			switch scenario {
			case "locked":
				lock, err := taskdb.Lock(path, true)
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Close()
			case "stale":
				plan.ProjectionSHA256 = "stale"
			case "binding":
				plan.Resolutions[0].Assignments["jira:issue:ABC-7"] = "obsidian:task:two"
			case "existing backup":
				if err := os.WriteFile(backup, []byte("Do not replace"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "incomplete":
				plan.Resolutions[0].KeepAuthoredID = ""
			}
			if err := applyTaskLinkRepair(path, backup, plan); err == nil {
				t.Fatal("unsafe repair succeeded")
			}
			after, _ := taskdb.ReadProjection(path)
			if !bytes.Equal(before, after) {
				t.Fatal("failed repair changed projection")
			}
			if scenario == "existing backup" {
				data, _ := os.ReadFile(backup)
				if string(data) != "Do not replace" {
					t.Fatal("backup overwritten")
				}
			}
		})
	}
}

func TestTaskLinkRepairMissingPreviewDoesNotCreateStateAndStrictPlan(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "state.db")
	if _, err := previewTaskLinkRepair(path); err == nil {
		t.Fatal("missing state accepted")
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatal("preview created state directory")
	}
	planPath := filepath.Join(t.TempDir(), "plan.json")
	for _, content := range []string{`{"unknown":true}`, `{}` + ` {}`} {
		_ = os.WriteFile(planPath, []byte(content), 0o600)
		if _, err := readLinkRepairPlan(planPath); err == nil {
			t.Fatal("invalid plan accepted: " + strings.TrimSpace(content))
		}
	}
}
