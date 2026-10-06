package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/integration"
	"radar/internal/integration/obsidian"
	"radar/internal/process"
)

// macOS's default temp root may contain system symlinks (/var -> /private/var).
// Use canonical fixture paths so the same strict live-path checks run on both OSes.
func tempDir(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func noteData(id, title, fields string) []byte {
	return []byte(fmt.Sprintf("---\nradar-id: %s\nradar-title: %q\nradar-state: open\nradar-priority: normal\nradar-created-at: 2026-08-01T10:00:00Z\nradar-completed-at:\n%suser: &user\n  radar-ignored: false\n  radar-muted: not a preference\n  text: |-\n    radar-ignored: true\n    ---\nother: *user\n# trailing comment\n---\nBody: radar-ignored: true\r\nNo final newline", id, title, fields))
}

func cacheData(version int, key string) []byte {
	return []byte(fmt.Sprintf(`{
  "version" : %d,
  "next_task_id": 9007199254740993,
  "ignored": "root metadata",
  "unknown": {"ignored": true, "number": 1.234567890123456789e+40},
  "task_records": [{
    "id": "record-1", "numeric_id": 42, "ignored": "not a preference",
    "state": "open", "ack": {"cursor": "ignored", "ignored": true},
    "first_seen": "2026-08-01T10:00:00Z", "last_seen": "2026-08-02T10:00:00Z",
    "updated_at": "2026-08-03T10:00:00Z", "done_at": "",
    "source_ref_ids": ["obsidian:task:12345678-1234-4234-8234-123456789abc"],
    "snapshot": {
      "id": 42, "%s" : true, "attention": "in_progress",
      "metadata": {"ignored": "true", "muted": "user data"},
      "unknown": {"ignored": false},
      "source_refs": [{"id": "obsidian:task:12345678-1234-4234-8234-123456789abc",
        "%s": false, "status": "done", "metadata": {"ignored": "keep me"},
        "snapshot": {"ignored": "not a typed snapshot here"}}]
    }
  }],
  "source_refs": [{
    "id": "obsidian:task:12345678-1234-4234-8234-123456789abc", "ignored": true,
    "active": false, "task_record_id": "record-1", "observed_at": "2026-08-03T10:00:00Z",
    "snapshot": {"id": "obsidian:task:12345678-1234-4234-8234-123456789abc",
      "%s": true, "status": "open", "metadata": {"ignored": "false"},
      "source_refs": [{"ignored": "not a typed path here"}]}
  }],
  "sources": [{"name": "example", "metadata": {"ignored": true}}]
}
`, version, key, key, key))
}

func writeFixture(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func fixtures(t *testing.T) (vault, state, private, archived string) {
	t.Helper()
	root := tempDir(t)
	vault = filepath.Join(root, "vault")
	if err := os.MkdirAll(filepath.Join(vault, ".obsidian"), 0o700); err != nil {
		t.Fatal(err)
	}
	private = filepath.Join(vault, "Tasks", "Private example--12345678", "Private example.md")
	archived = filepath.Join(vault, "Tasks", "Archived", "Archive example.md")
	writeFixture(t, private, noteData("12345678-1234-4234-8234-123456789abc", "Private example", "radar-ignored: TRUE # keep comment\n"), 0o640)
	archive := noteData("87654321-1234-4234-8234-123456789abc", "Archive example", "radar-ignored: false\n")
	archive = bytes.Replace(archive, []byte("radar-state: open"), []byte("radar-state: done"), 1)
	archive = bytes.Replace(archive, []byte("radar-completed-at:\n"), []byte("radar-completed-at: 2026-08-02T10:00:00Z\n"), 1)
	writeFixture(t, archived, archive, 0o600)
	state = filepath.Join(root, "state", "custom-cache.json")
	writeFixture(t, state, cacheData(7, "ignored"), 0o640)
	return
}

func assertFile(t *testing.T, path string, want []byte, mode os.FileMode) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("file %s differs: %v\ngot:\n%s\nwant:\n%s", path, err, got, want)
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode() != mode {
		t.Fatalf("mode %s: %v, %v; want %v", path, info, err, mode)
	}
}

func noDaemons() ([]int, error) { return nil, nil }

func TestNoteKeyRenamePreservesEverythingElse(t *testing.T) {
	for _, key := range []struct{ before, after string }{
		{"radar-ignored", "radar-muted"},
		{"'radar-ignored'", "'radar-muted'"},
		{`"radar-ignored"`, `"radar-muted"`},
		{`"radar-\u0069gnored"`, `"radar-muted"`},
		{"!!str radar-ignored", "!!str radar-muted"},
		{"&preference radar-ignored", "&preference radar-muted"},
		{"&radar-ignored radar-ignored", "&radar-ignored radar-muted"},
		{"? radar-ignored\n", "? radar-muted\n"},
	} {
		spelling := key.before
		for _, value := range []string{"true", "false", "TRUE", "False", "!!bool true", "&value false"} {
			for _, newline := range []string{"\n", "\r\n"} {
				t.Run(spelling+"/"+value+"/"+fmt.Sprintf("%q", newline), func(t *testing.T) {
					separator := " :  "
					if strings.HasSuffix(spelling, "\n") {
						separator = ":  "
					}
					data := noteData("12345678-1234-4234-8234-123456789abc", "Example", spelling+separator+value+"  # keep comment\n")
					data = bytes.ReplaceAll(data, []byte("\n"), []byte(newline))
					after, err := migrateNote(data)
					want := bytes.Replace(data, bytes.ReplaceAll([]byte(key.before), []byte("\n"), []byte(newline)), bytes.ReplaceAll([]byte(key.after), []byte("\n"), []byte(newline)), 1)
					if err != nil || !bytes.Equal(after, want) {
						t.Fatalf("rename: %v\ngot %s\nwant %s", err, after, want)
					}
					again, err := migrateNote(after)
					if err != nil || !bytes.Equal(again, after) {
						t.Fatalf("not idempotent: %v", err)
					}
				})
			}
		}
	}
}

func TestAbsentAndAlreadyMigratedNotesAreNoops(t *testing.T) {
	for _, fields := range []string{"", "radar-muted: true # existing\n", "radar-muted: false\n"} {
		data := noteData("12345678-1234-4234-8234-123456789abc", "Example", fields)
		after, err := migrateNote(data)
		if err != nil || !bytes.Equal(data, after) {
			t.Fatalf("noop changed: %v", err)
		}
	}
}

func TestNotesFailClosed(t *testing.T) {
	for _, fields := range []string{
		"radar-ignored: true\nradar-muted: true\n",
		"radar-ignored: false\nradar-muted: true\n",
		"radar-ignored: false\nradar-ignored: false\n",
		"radar-muted: true\n'radar-muted': true\n",
		"unknown: {same: 1, same: 2}\n",
		"radar-ignored:\n", "radar-ignored: null\n", "radar-ignored: 'true'\n",
		"radar-ignored: 1\n", "radar-ignored: []\n", "radar-ignored: {}\n",
		"radar-ignored: yes\n", "radar-muted: falsehood\n",
		"unknown-bool: &bool true\nradar-ignored: *bool\n",
		"\"radar-ig\\\nnored\": true\n",
	} {
		t.Run(fields, func(t *testing.T) {
			data := noteData("12345678-1234-4234-8234-123456789abc", "Example", fields)
			if _, err := migrateNote(data); err == nil {
				t.Fatal("invalid note accepted")
			}
		})
	}
	for _, data := range []string{"no frontmatter", "---\nradar-ignored: true\n", "---\n[true]\n---\n", "---\n{radar-ignored: true}\n---\n"} {
		if _, err := migrateNote([]byte(data)); err == nil {
			t.Fatalf("invalid frontmatter accepted: %s", data)
		}
	}
}

func TestCacheOnlyTypedPreferencesAndVersionChange(t *testing.T) {
	data := cacheData(7, "ignored")
	after, version, count, err := migrateCache(data)
	if err != nil || version != 7 || count != 3 || !bytes.Equal(after, cacheData(8, "muted")) {
		t.Fatalf("cache migration: version=%d, count=%d, error=%v\ngot %s", version, count, err, after)
	}
	again, version, count, err := migrateCache(after)
	if err != nil || version != 8 || count != 0 || !bytes.Equal(after, again) {
		t.Fatalf("cache not idempotent: version=%d, count=%d, err=%v", version, count, err)
	}
}

func TestCacheOffsetsAcrossMultipleRecordsAndSourceRefs(t *testing.T) {
	data := []byte(`{
  "version": 7,
  "task_records": [
    {"snapshot": {"ignored": false, "source_refs": [
      {"metadata": {"ignored": "keep"}}, {"ignored": true}, {"ignored": false}
    ]}},
    {"snapshot": {"\u0069gnored": true}}
  ],
  "source_refs": [
    {"snapshot": {"ignored": true}}, {"snapshot": {"ignored": false}}
  ]
}`)
	want := strings.NewReplacer(
		`"version": 7`, `"version": 8`,
		`"ignored": false`, `"muted": false`,
		`"ignored": true`, `"muted": true`,
		`"\u0069gnored": true`, `"muted": true`,
	).Replace(string(data))
	after, version, count, err := migrateCache(data)
	if err != nil || version != 7 || count != 6 || string(after) != want {
		t.Fatalf("offset migration: %d %d %v\ngot %s\nwant %s", version, count, err, after, want)
	}
}

func TestCacheNoPreferencesStillAdvancesVersion(t *testing.T) {
	for _, data := range []string{
		`{"version":7,"task_records":[],"source_refs":[]}`,
		`{"version":7,"task_records":null,"source_refs":null}`,
		`{"version":7,"unknown":{"ignored":true}}`,
		`{"version":7,"task_records":[{"snapshot":{"muted":false,"source_refs":[]}}]}`,
	} {
		after, version, count, err := migrateCache([]byte(data))
		if err != nil || version != 7 || count != 0 || string(after) != strings.Replace(data, `"version":7`, `"version":8`, 1) {
			t.Fatalf("version-only migration: %d %d %v %s", version, count, err, after)
		}
	}
}

func TestCacheFailsClosedAtEveryTypedPath(t *testing.T) {
	paths := []string{
		`{"version":%d,"task_records":[{"snapshot":{%s}}]}`,
		`{"version":%d,"task_records":[{"snapshot":{"source_refs":[{%s}]}}]}`,
		`{"version":%d,"source_refs":[{"snapshot":{%s}}]}`,
	}
	for _, path := range paths {
		for _, version := range []int{7, 8} {
			for _, fields := range []string{
				`"ignored":true,"muted":true`, `"ignored":false,"muted":true`,
				`"ignored":false,"ignored":false`, `"muted":true,"muted":true`,
				`"ignored":"true"`, `"ignored":null`, `"ignored":1`,
				`"muted":{}`, `"muted":[]`,
			} {
				data := fmt.Sprintf(path, version, fields)
				if _, _, _, err := migrateCache([]byte(data)); err == nil {
					t.Fatalf("invalid typed preference accepted: %s", data)
				}
			}
			if version == 8 {
				if _, _, _, err := migrateCache([]byte(fmt.Sprintf(path, version, `"ignored":false`))); err == nil {
					t.Fatal("version 8 with legacy preference accepted")
				}
			}
		}
	}
	for _, data := range []string{
		`{"version":6}`, `{"version":9}`, `{"version":"7"}`, `{}`, `null`,
		`{"version":7,"version":7}`, `{"version":7} {"version":7}`,
		`{"version":7,"task_records":{}}`, `{"version":7,"task_records":[null]}`,
		`{"version":7,"task_records":[{"snapshot":null}]}`,
		`{"version":7,"source_refs":[{"snapshot":[]}]}`,
		`{"version":7,"task_records":[{"snapshot":{"source_refs":{}}}]}`,
	} {
		if _, _, _, err := migrateCache([]byte(data)); err == nil {
			t.Fatalf("invalid cache accepted: %s", data)
		}
	}
}

func TestDryRunStagesPrivateAndArchiveWithoutLiveWrites(t *testing.T) {
	vault, state, private, archived := fixtures(t)
	originalPrivate, _ := os.ReadFile(private)
	originalArchive, _ := os.ReadFile(archived)
	plan, err := preflight(vault, state)
	if err != nil || plan.notes != 2 || plan.noteChanges != 2 || plan.cacheVersion != 7 || plan.preferences != 3 || len(plan.files) != 3 {
		t.Fatalf("preflight: %+v, %v", plan, err)
	}
	backup := filepath.Join(tempDir(t), "must-not-exist")
	var output bytes.Buffer
	if err := run([]string{"--vault", vault, "--state", state, "--backup", backup}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Read-only preflight") || !strings.Contains(output.String(), "Validated 2 managed notes") {
		t.Fatalf("unexpected output: %s", &output)
	}
	if _, err := os.Lstat(backup); !os.IsNotExist(err) {
		t.Fatal("dry-run created backup")
	}
	assertFile(t, private, originalPrivate, 0o640)
	assertFile(t, archived, originalArchive, 0o600)
	assertFile(t, state, cacheData(7, "ignored"), 0o640)
}

func TestMissingTasksRemainsMissing(t *testing.T) {
	vault := tempDir(t)
	if err := os.Mkdir(filepath.Join(vault, ".obsidian"), 0o700); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(tempDir(t), "tasks.json")
	writeFixture(t, state, cacheData(7, "ignored"), 0o600)
	plan, err := preflight(vault, state)
	if err != nil || plan.notes != 0 {
		t.Fatalf("empty vault: %+v, %v", plan, err)
	}
	if _, err := os.Lstat(filepath.Join(vault, "Tasks")); !os.IsNotExist(err) {
		t.Fatal("live Tasks directory created by preflight")
	}
}

func TestApplyBacksUpAllInputsPreservesModesAndIsIdempotent(t *testing.T) {
	vault, state, private, _ := fixtures(t)
	// Also back up a note which already uses the new spelling.
	data, _ := os.ReadFile(private)
	writeFixture(t, private, bytes.Replace(data, []byte("radar-ignored"), []byte("radar-muted"), 1), 0o640)
	plan, err := preflight(vault, state)
	if err != nil || plan.noteChanges != 1 {
		t.Fatalf("preflight: %+v, %v", plan, err)
	}
	backup := filepath.Join(tempDir(t), "backup")
	if err := apply(plan, backup, noDaemons); err != nil {
		t.Fatal(err)
	}
	for _, change := range plan.files {
		assertFile(t, change.path, change.after, change.mode)
		assertFile(t, filepath.Join(backup, change.relative), change.before, change.mode)
	}
	again, err := preflight(vault, state)
	if err != nil || again.noteChanges != 0 || again.cacheVersion != 8 || again.preferences != 0 {
		t.Fatalf("retry: %+v, %v", again, err)
	}
	for _, change := range again.files {
		if !bytes.Equal(change.before, change.after) {
			t.Fatal("retry has changes")
		}
	}
	if err := apply(again, filepath.Join(tempDir(t), "backup"), noDaemons); err != nil {
		t.Fatal(err)
	}
}

func TestProductionParserSeesNoteValuesWithoutOverridingHistoricalCache(t *testing.T) {
	vault, state, private, _ := fixtures(t)
	data, _ := os.ReadFile(private)
	// Current authored preferences are both false. Cached historical true
	// values must still remain true; migration is not a refresh/reconciliation.
	writeFixture(t, private, bytes.Replace(data, []byte("radar-ignored: TRUE"), []byte("radar-ignored: false"), 1), 0o640)
	plan, err := preflight(vault, state)
	if err != nil {
		t.Fatal(err)
	}
	if err := apply(plan, filepath.Join(tempDir(t), "backup"), noDaemons); err != nil {
		t.Fatal(err)
	}
	result := obsidian.NewSourceAt(vault).Collect(context.Background(), integration.CollectRequest{})
	if !result.Complete || len(result.Observations) != 2 {
		t.Fatalf("production reader: %+v", result)
	}
	for _, observation := range result.Observations {
		if observation.Ref.Muted {
			t.Fatal("false note preference became true")
		}
	}
	assertFile(t, state, cacheData(8, "muted"), 0o640)
}

func TestPreflightProductionValidationFailsWithoutWriting(t *testing.T) {
	for _, failure := range []string{"duplicate ID", "duplicate title", "missing lifecycle", "malformed binding", "two private notes", "flat private note", "nested archive", "directory suffix", "cache version", "note collision"} {
		t.Run(failure, func(t *testing.T) {
			vault, state, private, archived := fixtures(t)
			original, _ := os.ReadFile(private)
			archive, _ := os.ReadFile(archived)
			switch failure {
			case "duplicate ID":
				archive = bytes.ReplaceAll(archive, []byte("87654321-1234-4234-8234-123456789abc"), []byte("12345678-1234-4234-8234-123456789abc"))
				writeFixture(t, archived, archive, 0o600)
			case "duplicate title":
				archive = bytes.ReplaceAll(archive, []byte("Archive example"), []byte("Private example"))
				writeFixture(t, archived, archive, 0o600)
			case "missing lifecycle":
				original = bytes.Replace(original, []byte("radar-state: open\n"), nil, 1)
				writeFixture(t, private, original, 0o640)
			case "malformed binding":
				original = bytes.Replace(original, []byte("radar-priority: normal\n"), []byte("radar-priority: normal\nradar-source-refs: [{source: jira, kind: issue}]\n"), 1)
				writeFixture(t, private, original, 0o640)
			case "two private notes":
				writeFixture(t, filepath.Join(filepath.Dir(private), "Another.md"), original, 0o600)
			case "flat private note":
				writeFixture(t, filepath.Join(vault, "Tasks", "Flat.md"), original, 0o600)
			case "nested archive":
				if err := os.Mkdir(filepath.Join(filepath.Dir(archived), "nested"), 0o700); err != nil {
					t.Fatal(err)
				}
			case "directory suffix":
				moved := filepath.Join(vault, "Tasks", "Wrong--87654321")
				if err := os.Rename(filepath.Dir(private), moved); err != nil {
					t.Fatal(err)
				}
				private = filepath.Join(moved, filepath.Base(private))
			case "cache version":
				writeFixture(t, state, cacheData(6, "ignored"), 0o640)
			case "note collision":
				original = bytes.Replace(original, []byte("radar-priority: normal\n"), []byte("radar-priority: normal\nradar-muted: true\n"), 1)
				writeFixture(t, private, original, 0o640)
			}
			if _, err := preflight(vault, state); err == nil {
				t.Fatal("invalid migration accepted")
			}
			assertFile(t, private, original, 0o640)
		})
	}
}

func TestSymlinksFailClosed(t *testing.T) {
	for _, target := range []string{"vault", ".obsidian", "Tasks", "private directory", "private note", "archive directory", "archive note", "extra file", "cache", "cache parent"} {
		t.Run(target, func(t *testing.T) {
			vault, state, private, archived := fixtures(t)
			var path string
			switch target {
			case "vault":
				path = vault
			case ".obsidian", "Tasks":
				path = filepath.Join(vault, target)
			case "private directory":
				path = filepath.Dir(private)
			case "private note":
				path = private
			case "archive directory":
				path = filepath.Dir(archived)
			case "archive note":
				path = archived
			case "cache":
				path = state
			case "cache parent":
				path = filepath.Dir(state)
			case "extra file":
				path = filepath.Join(filepath.Dir(private), "attachment.txt")
				writeFixture(t, path, []byte("not a note"), 0o600)
			}
			moved := filepath.Join(tempDir(t), "original")
			if err := os.Rename(path, moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(moved, path); err != nil {
				t.Fatal(err)
			}
			if _, err := preflight(vault, state); err == nil {
				t.Fatalf("symlink accepted: %s", target)
			}
		})
	}
}

func TestApplyRejectsExistingBackupOrActiveDaemon(t *testing.T) {
	vault, state, _, _ := fixtures(t)
	plan, err := preflight(vault, state)
	if err != nil {
		t.Fatal(err)
	}
	existing := tempDir(t)
	writeFixture(t, filepath.Join(existing, "keep"), []byte("existing backup"), 0o600)
	if err := apply(plan, existing, noDaemons); err == nil {
		t.Fatal("existing backup reused")
	}
	assertFile(t, filepath.Join(existing, "keep"), []byte("existing backup"), 0o600)
	for _, detector := range []func() ([]int, error){
		func() ([]int, error) { return []int{123}, nil },
		func() ([]int, error) { return nil, errors.New("ps unavailable") },
	} {
		backup := filepath.Join(tempDir(t), "backup")
		if err := apply(plan, backup, detector); err == nil {
			t.Fatal("daemon check did not fail closed")
		}
		if _, err := os.Lstat(backup); !os.IsNotExist(err) {
			t.Fatal("backup created before daemon check")
		}
	}
	for _, change := range plan.files {
		assertFile(t, change.path, change.before, change.mode)
	}
}

func TestApplyConcurrentEditsAndModesAreNeverOverwritten(t *testing.T) {
	for _, target := range []string{"note bytes", "note mode", "cache bytes", "cache mode", "symlink"} {
		t.Run(target, func(t *testing.T) {
			vault, state, private, _ := fixtures(t)
			plan, err := preflight(vault, state)
			if err != nil {
				t.Fatal(err)
			}
			path := private
			if strings.HasPrefix(target, "cache") {
				path = state
			}
			data, _ := os.ReadFile(path)
			mode := os.FileMode(0o640)
			switch target {
			case "note bytes", "cache bytes":
				data = append(data, []byte("\nUser concurrent edit")...)
				writeFixture(t, path, data, mode)
			case "note mode", "cache mode":
				mode = 0o600
				if err := os.Chmod(path, mode); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				moved := filepath.Join(tempDir(t), "note.md")
				if err := os.Rename(path, moved); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(moved, path); err != nil {
					t.Fatal(err)
				}
			}
			if err := apply(plan, filepath.Join(tempDir(t), "backup"), noDaemons); err == nil || !strings.Contains(err.Error(), "changed since preflight") {
				t.Fatalf("concurrent edit accepted: %v", err)
			}
			if target != "symlink" {
				assertFile(t, path, data, mode)
			}
		})
	}
}

func TestApplyEditDuringBackupAbortsBeforeLiveWrites(t *testing.T) {
	vault, state, private, _ := fixtures(t)
	plan, err := preflight(vault, state)
	if err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(private)
	modified := append(bytes.Clone(original), []byte("\nConcurrent edit")...)
	calls := 0
	detector := func() ([]int, error) {
		calls++
		if calls == 2 { // After all backups, before any replacements.
			writeFixture(t, private, modified, 0o640)
		}
		return nil, nil
	}
	backup := filepath.Join(tempDir(t), "backup")
	err = apply(plan, backup, detector)
	if err == nil || !strings.Contains(err.Error(), "0/3 files replaced") || !strings.Contains(err.Error(), backup) {
		t.Fatalf("concurrent edit was not reported: %v", err)
	}
	assertFile(t, private, modified, 0o640)
	assertFile(t, state, cacheData(7, "ignored"), 0o640)
	for _, change := range plan.files {
		assertFile(t, filepath.Join(backup, change.relative), change.before, change.mode)
	}
}

func TestPartialApplyRetainsCompleteBackupAndReportsProgress(t *testing.T) {
	vault, state, _, _ := fixtures(t)
	plan, err := preflight(vault, state)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	modified := append(bytes.Clone(plan.files[1].before), []byte("\nConcurrent edit")...)
	detector := func() ([]int, error) {
		calls++
		if calls == 4 { // First replacement succeeded; edit the next input.
			writeFixture(t, plan.files[1].path, modified, plan.files[1].mode)
		}
		return nil, nil
	}
	backup := filepath.Join(tempDir(t), "backup")
	err = apply(plan, backup, detector)
	if err == nil || !strings.Contains(err.Error(), "1/3 files replaced") || !strings.Contains(err.Error(), backup) {
		t.Fatalf("partial failure was not reported: %v", err)
	}
	assertFile(t, plan.files[0].path, plan.files[0].after, plan.files[0].mode)
	assertFile(t, plan.files[1].path, modified, plan.files[1].mode)
	assertFile(t, state, cacheData(7, "ignored"), 0o640)
	for _, change := range plan.files {
		assertFile(t, filepath.Join(backup, change.relative), change.before, change.mode)
	}
}

func TestCLIApplyAndReadOnlyDaemonDetection(t *testing.T) {
	for _, scenario := range []struct {
		name, ps string
		wantErr  bool
	}{
		{"stopped", "#!/bin/sh\nprintf '1 /sbin/init\\n'\n", false},
		{"running", "#!/bin/sh\nprintf '123 radar daemon\\n'\n", true},
		{"discovery failure", "#!/bin/sh\nexit 1\n", true},
		{"empty enumeration", "#!/bin/sh\nexit 0\n", true},
		{"malformed enumeration", "#!/bin/sh\nprintf 'not-a-pid /sbin/init\\n'\n", true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			vault, state, private, archived := fixtures(t)
			privateBefore, _ := os.ReadFile(private)
			archiveBefore, _ := os.ReadFile(archived)
			bin := tempDir(t)
			writeFixture(t, filepath.Join(bin, "ps"), []byte(scenario.ps), 0o700)
			t.Setenv("PATH", bin)
			t.Setenv("RADAR_PID", filepath.Join(tempDir(t), "missing.pid"))
			backup := filepath.Join(tempDir(t), "backup")
			var output bytes.Buffer
			err := run([]string{"--vault", vault, "--state", state, "--apply", "--backup", backup}, &output)
			if scenario.wantErr {
				if err == nil || !strings.Contains(err.Error(), "daemon") {
					t.Fatalf("daemon check accepted: %v", err)
				}
				if _, err := os.Lstat(backup); !os.IsNotExist(err) {
					t.Fatal("daemon refusal created backup")
				}
				assertFile(t, private, privateBefore, 0o640)
				assertFile(t, archived, archiveBefore, 0o600)
				assertFile(t, state, cacheData(7, "ignored"), 0o640)
				return
			}
			if err != nil || !strings.Contains(output.String(), "Applied migration") {
				t.Fatalf("CLI apply: %v, %s", err, &output)
			}
			assertFile(t, filepath.Join(backup, "tasks.json"), cacheData(7, "ignored"), 0o640)
			assertFile(t, state, cacheData(8, "muted"), 0o640)
			result := obsidian.NewSourceAt(vault).Collect(context.Background(), integration.CollectRequest{})
			if !result.Complete || len(result.Observations) != 2 {
				t.Fatalf("production reader: %+v", result)
			}
			for _, observation := range result.Observations {
				want := strings.Contains(observation.Ref.ID, "12345678-1234-")
				if observation.Ref.Muted != want {
					t.Fatalf("production preference: %s got %v want %v", observation.Ref.ID, observation.Ref.Muted, want)
				}
			}
		})
	}
}

func TestStalePIDCannotHideFailedProcessEnumeration(t *testing.T) {
	vault, statePath, private, archived := fixtures(t)
	privateBefore, _ := os.ReadFile(private)
	archiveBefore, _ := os.ReadFile(archived)
	bin := tempDir(t)
	writeFixture(t, filepath.Join(bin, "ps"), []byte("#!/bin/sh\nexit 1\n"), 0o700)
	t.Setenv("PATH", bin)
	pidPath := filepath.Join(tempDir(t), "stale.pid")
	writeFixture(t, pidPath, []byte("2147483647\n"), 0o600)
	t.Setenv("RADAR_PID", pidPath)
	// Demonstrate the unchanged runtime tolerance that the migration must not
	// mistake for a confirmed absence of daemons.
	if pids, err := process.DaemonPIDs(); err != nil || len(pids) != 0 {
		t.Fatalf("runtime stale-PID behavior changed: %v, %v", pids, err)
	}
	backup := filepath.Join(tempDir(t), "backup")
	var output bytes.Buffer
	err := run([]string{"--vault", vault, "--state", statePath, "--apply", "--backup", backup}, &output)
	if err == nil || !strings.Contains(err.Error(), "strict ps enumeration failed") {
		t.Fatalf("uncertain process discovery accepted: %v", err)
	}
	if _, err := os.Lstat(backup); !os.IsNotExist(err) {
		t.Fatal("uncertain discovery created backup")
	}
	assertFile(t, private, privateBefore, 0o640)
	assertFile(t, archived, archiveBefore, 0o600)
	assertFile(t, statePath, cacheData(7, "ignored"), 0o640)
}

func TestPreflightRejectsMalformedProductionCacheTypes(t *testing.T) {
	for _, change := range []struct{ before, after string }{
		{`"active": false`, `"active": "not-a-boolean"`},
		{`"metadata": {"ignored": "true"`, `"metadata": {"ignored": true`},
		{`"metadata": {"ignored": "keep me"}`, `"metadata": {"ignored": true}`},
		{`"numeric_id": 42`, `"numeric_id": "42"`},
		{`"name": "example"`, `"name": true`},
	} {
		for _, version := range []int{7, 8} {
			t.Run(fmt.Sprintf("%d/%s", version, change.after), func(t *testing.T) {
				vault, statePath, private, archived := fixtures(t)
				key := "ignored"
				if version == 8 {
					key = "muted"
				}
				data := bytes.Replace(cacheData(version, key), []byte(change.before), []byte(change.after), 1)
				writeFixture(t, statePath, data, 0o640)
				privateBefore, _ := os.ReadFile(private)
				archiveBefore, _ := os.ReadFile(archived)
				if _, err := preflight(vault, statePath); err == nil || !strings.Contains(err.Error(), "failed production validation") {
					t.Fatalf("malformed production type accepted: %v", err)
				}
				assertFile(t, private, privateBefore, 0o640)
				assertFile(t, archived, archiveBefore, 0o600)
				assertFile(t, statePath, data, 0o640)
			})
		}
	}
}

func TestCacheValidationUsesOnlyStageAndRestoresEnvironment(t *testing.T) {
	for _, present := range []bool{false, true} {
		for _, malformed := range []bool{false, true} {
			t.Run(fmt.Sprintf("present=%v/malformed=%v", present, malformed), func(t *testing.T) {
				vault, statePath, _, _ := fixtures(t)
				configured := filepath.Join(tempDir(t), "must-not-load.json")
				writeFixture(t, configured, []byte("not a valid cache"), 0o600)
				t.Setenv("RADAR_STATE", configured)
				if !present {
					if err := os.Unsetenv("RADAR_STATE"); err != nil {
						t.Fatal(err)
					}
				}
				if malformed {
					data := bytes.Replace(cacheData(7, "ignored"), []byte(`"active": false`), []byte(`"active": "invalid"`), 1)
					writeFixture(t, statePath, data, 0o640)
				}
				_, err := preflight(vault, statePath)
				if (err != nil) != malformed {
					t.Fatalf("preflight: %v", err)
				}
				value, exists := os.LookupEnv("RADAR_STATE")
				if exists != present || (present && value != configured) {
					t.Fatalf("RADAR_STATE not restored: %q, %v", value, exists)
				}
				assertFile(t, configured, []byte("not a valid cache"), 0o600)
			})
		}
	}
}

func TestProductionCacheDiscardCannotPassValidation(t *testing.T) {
	// The production loader silently discards an incompatible cache. Checking
	// raw input counts against loaded counts must detect that even without an
	// unmarshal error. Normal preflight passes the migrated version-8 bytes.
	if err := validateCache(tempDir(t), cacheData(7, "ignored")); err == nil || !strings.Contains(err.Error(), "cache discarded or records lost") {
		t.Fatalf("discarded cache accepted: %v", err)
	}
}

func addedNote(t *testing.T, vault, location string) (string, []byte) {
	t.Helper()
	data := noteData("abcdef01-1234-4234-8234-123456789abc", "Added example", "radar-ignored: true\n")
	path := filepath.Join(vault, "Tasks", "Added example--abcdef01", "Added example.md")
	switch location {
	case "archive":
		path = filepath.Join(vault, "Tasks", "Archived", "Added example.md")
		data = bytes.Replace(data, []byte("radar-state: open"), []byte("radar-state: done"), 1)
		data = bytes.Replace(data, []byte("radar-completed-at:\n"), []byte("radar-completed-at: 2026-08-02T10:00:00Z\n"), 1)
	case "existing private directory":
		path = filepath.Join(vault, "Tasks", "Private example--12345678", "Added example.md")
	}
	writeFixture(t, path, data, 0o640)
	return path, data
}

func TestPreflightRechecksCapturedLayoutAfterStaging(t *testing.T) {
	for _, location := range []string{"private", "archive"} {
		t.Run(location, func(t *testing.T) {
			vault, statePath, private, archived := fixtures(t)
			inventory, err := inventoryTasks(filepath.Join(vault, "Tasks"))
			if err != nil {
				t.Fatal(err)
			}
			privateBefore, _ := os.ReadFile(private)
			archiveBefore, _ := os.ReadFile(archived)
			// An addition after the initial capture but before staging finishes
			// must fail even if the staged production parser accepts all notes.
			path, data := addedNote(t, vault, location)
			if _, err := stagePreflight(vault, statePath, inventory); err == nil || !strings.Contains(err.Error(), "task layout changed since preflight") {
				t.Fatalf("layout addition accepted during preflight: %v", err)
			}
			assertFile(t, path, data, 0o640)
			assertFile(t, private, privateBefore, 0o640)
			assertFile(t, archived, archiveBefore, 0o600)
			assertFile(t, statePath, cacheData(7, "ignored"), 0o640)
		})
	}
}

func TestApplyRejectsAddedNotesBeforeAndDuringBackups(t *testing.T) {
	for _, location := range []string{"private", "archive", "existing private directory"} {
		for _, duringBackups := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/duringBackups=%v", location, duringBackups), func(t *testing.T) {
				vault, statePath, _, _ := fixtures(t)
				plan, err := preflight(vault, statePath)
				if err != nil {
					t.Fatal(err)
				}
				var path string
				var data []byte
				if !duringBackups {
					path, data = addedNote(t, vault, location)
				}
				calls := 0
				detector := func() ([]int, error) {
					calls++
					if duringBackups && calls == 2 { // All backups made, before any live write.
						path, data = addedNote(t, vault, location)
					}
					return nil, nil
				}
				backup := filepath.Join(tempDir(t), "backup")
				err = apply(plan, backup, detector)
				if err == nil || !strings.Contains(err.Error(), "task layout changed since preflight") {
					t.Fatalf("new note accepted: %v", err)
				}
				if duringBackups {
					if !strings.Contains(err.Error(), "0/3 files replaced") || !strings.Contains(err.Error(), backup) {
						t.Fatalf("backup failure progress missing: %v", err)
					}
					for _, change := range plan.files {
						assertFile(t, filepath.Join(backup, change.relative), change.before, change.mode)
					}
				} else if _, err := os.Lstat(backup); !os.IsNotExist(err) {
					t.Fatal("stale inventory created backup")
				}
				for _, change := range plan.files {
					assertFile(t, change.path, change.before, change.mode)
				}
				assertFile(t, path, data, 0o640)
			})
		}
	}
}

func TestApplyRejectsAdditionBetweenReplacementsWithBackupIntact(t *testing.T) {
	vault, statePath, _, _ := fixtures(t)
	plan, err := preflight(vault, statePath)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	var path string
	var data []byte
	detector := func() ([]int, error) {
		calls++
		if calls == 4 { // First replacement finished; next one has not started.
			path, data = addedNote(t, vault, "archive")
		}
		return nil, nil
	}
	backup := filepath.Join(tempDir(t), "backup")
	err = apply(plan, backup, detector)
	if err == nil || !strings.Contains(err.Error(), "task layout changed since preflight") || !strings.Contains(err.Error(), "1/3 files replaced") {
		t.Fatalf("addition during apply accepted: %v", err)
	}
	assertFile(t, plan.files[0].path, plan.files[0].after, plan.files[0].mode)
	for _, change := range plan.files[1:] {
		assertFile(t, change.path, change.before, change.mode)
	}
	assertFile(t, path, data, 0o640)
	assertFile(t, statePath, cacheData(7, "ignored"), 0o640)
	for _, change := range plan.files {
		assertFile(t, filepath.Join(backup, change.relative), change.before, change.mode)
	}
}

func TestMissingTasksInventoryRejectsLaterCreation(t *testing.T) {
	vault := tempDir(t)
	if err := os.Mkdir(filepath.Join(vault, ".obsidian"), 0o700); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(tempDir(t), "tasks.json")
	writeFixture(t, statePath, cacheData(7, "ignored"), 0o600)
	plan, err := preflight(vault, statePath)
	if err != nil {
		t.Fatal(err)
	}
	path, data := addedNote(t, vault, "private")
	if err := apply(plan, filepath.Join(tempDir(t), "backup"), noDaemons); err == nil || !strings.Contains(err.Error(), "task layout changed since preflight") {
		t.Fatalf("new Tasks root accepted: %v", err)
	}
	assertFile(t, path, data, 0o640)
	assertFile(t, statePath, cacheData(7, "ignored"), 0o600)
}

func TestInventoryDoesNotScanUnmanagedVaultOrNestedAttachments(t *testing.T) {
	vault, _, private, _ := fixtures(t)
	attachment := filepath.Join(filepath.Dir(private), "attachments", "Existing.txt")
	writeFixture(t, attachment, []byte("existing attachment"), 0o600)
	inventory, err := inventoryTasks(filepath.Join(vault, "Tasks"))
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(vault, "Personal", "Unmanaged.md"), []byte("not a managed task"), 0o600)
	writeFixture(t, filepath.Join(filepath.Dir(attachment), "Nested.md"), []byte("not a managed note"), 0o600)
	if err := inventory.unchanged(); err != nil {
		t.Fatalf("inventory scanned outside its scope: %v", err)
	}
}

func TestCLIRequiresExplicitPathsAndBackupOutsideVault(t *testing.T) {
	vault, state, _, _ := fixtures(t)
	for _, args := range [][]string{
		nil, {"--vault", vault}, {"--state", state},
		{"--vault", "relative", "--state", state},
		{"--vault", vault, "--state", "relative"},
		{"--vault", vault, "--state", state, "--apply"},
		{"--vault", vault, "--state", state, "--apply", "--backup", "relative"},
		{"--vault", vault, "--state", state, "--apply", "--backup", filepath.Join(vault, "backup")},
		{"--vault", vault, "--state", state, "unexpected"},
	} {
		var output bytes.Buffer
		if err := run(args, &output); err == nil {
			t.Fatalf("invalid CLI accepted: %v", args)
		}
	}
}
