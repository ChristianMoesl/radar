package tui

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"radar/internal/collector"
	"radar/internal/integration"
	"radar/internal/integration/datadog"
	"radar/internal/integration/obsidian"
	"radar/internal/integration/workspace"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/state"
	"radar/internal/taskdb"
	"radar/internal/taskrefs"
)

// Exercise the production collector, Enter creation request, workspace manager,
// canonical-note author and SQLite projection. Only Pup's provider boundary is
// a fixture; preparation never starts tmux, Pi, SBX or a real provider request.
func TestCanonicalOnlySourceWorkspaceKeepsOneTaskAcrossRefreshAndRestart(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	root, vault := filepath.Join(base, "workspaces"), filepath.Join(base, "vault")
	configHome, bin := filepath.Join(base, "config"), filepath.Join(base, "bin")
	for _, dir := range []string{root, filepath.Join(vault, ".obsidian"), filepath.Join(vault, "Tasks"), filepath.Join(configHome, "radar"), bin} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", base)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("RADAR_STATE", filepath.Join(base, "state.db"))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	cfg := fmt.Sprintf("workspace:\n  root_dir: %q\nobsidian:\n  vault_path: %q\nsbx:\n  enabled: false\ndatadog:\n  enabled: true\n  site: datadoghq.eu\n  org: radar\n  monitor_query: tag:team:platform\n", root, vault)
	if err := os.WriteFile(filepath.Join(configHome, "radar", "config.yaml"), []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
	response := filepath.Join(base, "monitors.json")
	writeMonitors := func(monitors string, count int) {
		t.Helper()
		body := fmt.Sprintf(`{"metadata":{"total_count":%d,"page_count":1},"monitors":[%s]}`, count, monitors)
		if err := os.WriteFile(response, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	const monitor = `{"id":42,"name":"API errors","status":"Alert"}`
	writeMonitors(monitor, 1)
	pup := fmt.Sprintf(`#!/bin/sh
case "$*" in
  --version) printf 'pup 2.0.0\n';;
  *'auth status'*) printf '%%s\n' '{"authenticated":true,"has_refresh":true,"token_type":"Bearer","site":"datadoghq.eu","org":"radar","status":"valid","scopes":["monitors_read"]}';;
  *'/api/v1/monitor/search'*) cat %q;;
  *) exit 99;;
esac
`, response)
	if err := os.WriteFile(filepath.Join(bin, "pup"), []byte(pup), 0700); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := state.NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}
	refresh := func() {
		t.Helper()
		author := obsidian.NewSourceAt(vault)
		sources := []integration.Source{author, datadog.NewSource(), workspace.NewSource(author)}
		result := collector.Collect(ctx, store.CollectionTasks(), logger, sources)
		for _, source := range sources {
			if !result.Complete[source.Descriptor().Name] {
				t.Fatalf("incomplete fixture collection: %+v", result.Sources)
			}
		}
		store.SetCollection(result.Tasks, result.SourceNames, result.Sources)
	}
	refresh()
	initial := store.Tasks()
	if len(initial) != 1 || len(initial[0].SourceRefs) != 1 {
		t.Fatalf("expected one unadopted monitor: %+v", initial)
	}
	ref := initial[0].SourceRefs[0]
	if ref.CanonicalKey != "datadog:monitor:42" || len(ref.LinkingKeys) != 0 || ref.Authored {
		t.Fatalf("fixture is not a canonical-only source ref: %+v", ref)
	}
	// The production Enter path must carry the selected canonical identity into
	// the normal manager request rather than creating unrelated work.
	_, cmd := (model{tasks: initial}).activateSelected()
	if cmd == nil {
		t.Fatal("Enter did not inspect the selected task for workspace creation")
	}
	msg := cmd().(tea.BatchMsg)[0]().(workspaceStateMsg)
	if msg.err != nil {
		t.Fatal(msg.err)
	}
	request := msg.editor.createRequest()
	if request.TaskLinkingKey != ref.CanonicalKey {
		t.Fatalf("creation lost selected identity: %+v", request)
	}
	request.PrepareOnly = true
	manager := workspace.NewSource(obsidian.NewSourceAt(vault))
	plan, err := manager.PreviewCreate(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	request.Note, request.ExpectedPlanID = plan.Note, plan.PlanID
	created, err := manager.CreateWorkspace(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	var workspaceTaskID int
	for _, phase := range []string{"created", "refresh", "restart"} {
		if phase == "restart" {
			store, err = state.NewStore(logger)
			if err != nil {
				t.Fatal(err)
			}
		}
		refresh()
		tasks := store.Tasks()
		if len(tasks) != 1 || len(store.Records()) != 1 || len(tasks[0].SourceRefs) != 3 || tasks[0].Attention != "immediate" {
			t.Fatalf("%s split or changed the selected logical task: %+v", phase, tasks)
		}
		if phase == "created" {
			workspaceTaskID = tasks[0].ID
		} else if tasks[0].ID != workspaceTaskID {
			t.Fatalf("%s changed workspace task ID: %d != %d", phase, tasks[0].ID, workspaceTaskID)
		}
		seen := map[string]bool{}
		for _, ref := range tasks[0].SourceRefs {
			seen[ref.Source] = true
			if ref.Source == "workspace" && (!ref.WorkspaceEntry || ref.Path != created.Path) {
				t.Fatalf("workspace association changed: %+v", ref)
			}
		}
		if !seen["datadog"] || !seen["obsidian"] || !seen["workspace"] {
			t.Fatalf("missing a source, canonical note or workspace: %+v", seen)
		}
		updated, open := (model{tasks: tasks}).activateSelected()
		if got := updated.(model); open == nil || got.mode != "" || got.operation.kind != "session" || got.editor.active {
			t.Fatalf("%s Enter offered creation instead of reopening: %+v", phase, got)
		}
		// Existing primary-note explicit-link selection remains intact after
		// adoption; retrying creation through that key reuses the same registry.
		retry := integration.ManagedWorkspaceRequest{Name: tasks[0].Title, TaskLinkingKey: taskrefs.TaskLinkingKey(tasks[0]), PrepareOnly: true}
		reopened, err := manager.CreateWorkspace(ctx, retry)
		if err != nil || reopened.Path != created.Path {
			t.Fatalf("%s retry changed workspace: %+v %v", phase, reopened, err)
		}
	}
	registry, err := workspacegroup.Load(root)
	if err != nil || len(registry.Workspaces) != 1 || registry.Workspaces[0].TaskLinkingKey != ref.CanonicalKey {
		t.Fatalf("source association did not persist: %+v %v", registry, err)
	}
	notes, err := taskdb.List(vault)
	if err != nil || len(notes) != 1 || notes[0].State != "open" {
		t.Fatalf("creation/reopening changed authored lifecycle or duplicated notes: %+v %v", notes, err)
	}
	// A same-title source is independent; the regression must fix association,
	// not collapse or hide duplicate-looking rows.
	writeMonitors(monitor+`,{"id":43,"name":"API errors","status":"Alert"}`, 2)
	refresh()
	if tasks := store.Tasks(); len(tasks) != 2 || len(store.Records()) != 2 {
		t.Fatalf("title matching hid or merged independent source work: %+v", tasks)
	}
}
