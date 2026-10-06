package main

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"radar/internal/protocol"
	"radar/internal/state"
)

func TestMigratedCacheLoadsWithoutDiscardingProductionRecords(t *testing.T) {
	path := filepath.Join(tempDir(t), "tasks.json")
	t.Setenv("RADAR_STATE", path)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := state.NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}
	ref := protocol.SourceRef{
		ID: "obsidian:task:example", Source: "obsidian", Kind: "task", Title: "Example",
		Authored: true, Muted: true, Signal: "attention",
		Role: protocol.SourceRefRoleAuthoritative, Authority: protocol.SourceRefAuthorityPrimary,
		Lifecycle: protocol.SourceRefLifecycleWorkItem,
		Bindings:  []protocol.SourceBinding{{Source: "jira", Kind: "issue", ID: "jira:issue:ABC-123", WorkItem: true}},
	}
	store.SetTasks([]protocol.Task{{Title: "Example", Attention: "attention", Muted: true, SourceRefs: []protocol.SourceRef{ref}}})
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(current, []byte(`"version": 8`)) {
		t.Fatal("migration target must match the production cache version")
	}
	legacy := bytes.Replace(current, []byte(`"version": 8`), []byte(`"version": 7`), 1)
	legacy = bytes.ReplaceAll(legacy, []byte(`"muted":`), []byte(`"ignored":`))
	migrated, version, preferences, err := migrateCache(legacy)
	if err != nil || version != 7 || preferences == 0 || !bytes.Equal(migrated, current) {
		t.Fatalf("production cache round trip: version=%d preferences=%d error=%v", version, preferences, err)
	}
	writeFixture(t, path, migrated, 0o600)
	reloaded, err := state.NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reloaded.Records(), store.Records()) || !reflect.DeepEqual(reloaded.SourceRefs(), store.SourceRefs()) || !reflect.DeepEqual(reloaded.Tasks(), store.Tasks()) {
		t.Fatal("migration lost production records, source facts, bindings, or projected tasks")
	}
	if tasks := reloaded.Tasks(); len(tasks) != 1 || !tasks[0].Muted || tasks[0].DisplayGroup() != "muted" {
		t.Fatalf("migration lost authored preference: %+v", tasks)
	}
}
