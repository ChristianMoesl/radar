package workspacegc_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"radar/internal/cleanup"
	"radar/internal/integration"
	gitprovider "radar/internal/integration/git"
	sbxprovider "radar/internal/integration/sbx"
	tmuxprovider "radar/internal/integration/tmux"
	"radar/internal/integration/workspace"
	"radar/internal/integration/workspace/group"
	"radar/internal/protocol"
	"radar/internal/state"
	"radar/internal/workspacegc"
)

// Real cleanup providers operate only on isolated files and fake runtime CLIs.
// In particular no sandbox, tmux server, remote Git service or user data is used.
func TestStagedRuntimeRetirementPreservesDataAndRetries(t *testing.T) {
	for _, failFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("partial_failure=%v", failFirst), func(t *testing.T) {
			home := t.TempDir()
			root := filepath.Join(home, "workspaces")
			anchor := filepath.Join(root, "completed")
			member := filepath.Join(anchor, "repo--work")
			configHome := filepath.Join(home, "config")
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", configHome)
			t.Setenv("RADAR_STATE", filepath.Join(home, "state.db"))
			write := func(path, body string, mode os.FileMode) {
				t.Helper()
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(body), mode); err != nil {
					t.Fatal(err)
				}
			}
			write(filepath.Join(configHome, "radar", "config.yaml"), fmt.Sprintf("workspace: {root_dir: %q}\n", root), 0600)
			dirty := filepath.Join(member, "dirty.txt")
			unknown := filepath.Join(anchor, "unknown.txt")
			note := filepath.Join(home, "canonical.md")
			for _, path := range []string{dirty, unknown, note} {
				write(path, "valuable local data\n", 0600)
			}
			group := workspacegroup.Workspace{ID: workspacegroup.ID(anchor), Name: "completed", Path: anchor, SessionName: "registered-session", Sandbox: &workspacegroup.Sandbox{Name: "registered-sandbox", Agent: "shell", Mounts: []string{anchor}}, Members: []workspacegroup.Member{{Repository: filepath.Join(home, "primary"), Path: member, Branch: "local-only"}}, NotePath: note, NoteLinkingKey: "test:task", TaskLinkingKey: "test:task"}
			registry := workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}
			if err := workspacegroup.Save(root, registry); err != nil {
				t.Fatal(err)
			}
			calls := filepath.Join(home, "calls")
			stopped := filepath.Join(home, "stopped")
			removed := filepath.Join(home, "session-removed")
			fail := filepath.Join(home, "fail-stop")
			if failFirst {
				write(fail, "fail\n", 0600)
			}
			bin := filepath.Join(home, "bin")
			write(filepath.Join(bin, "sbx"), fmt.Sprintf(`#!/bin/sh
printf 'sbx %%s\n' "$*" >> %q
case "$1" in
ls) status=running; [ ! -f %q ] || status=stopped
printf '[{"id":"container-1","name":"registered-sandbox","status":"%%s","workspaces":[%%s]}]' "$status" '%q';;
stop) [ ! -f %q ] || exit 1
: > %q;;
*) exit 42;;
esac
`, calls, stopped, anchor, fail, stopped), 0755)
			write(filepath.Join(bin, "tmux"), fmt.Sprintf(`#!/bin/sh
printf 'tmux %%s\n' "$*" >> %q
case "$1" in
list-sessions) if [ -f %q ]; then printf 'no server running' >&2; exit 1; fi
printf '101\t1700000001\t$1\tregistered-session\t1\t1\t%%s\n' %q;;
kill-session) : > %q;;
*) exit 42;;
esac
`, calls, removed, anchor, removed), 0755)
			write(filepath.Join(bin, "git"), "#!/bin/sh\nexit 42\n", 0755)
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			store, err := state.NewStore(slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				t.Fatal(err)
			}
			done := time.Date(2026, 1, 2, 10, 20, 30, 0, time.UTC)
			store.SetTasks([]protocol.Task{{Attention: "done", SourceRefs: []protocol.SourceRef{
				{ID: "test:task", EntityID: "test:task", Source: "test", Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkItem, Authority: protocol.SourceRefAuthorityPrimary, Signal: "done", CanonicalKey: "test:task", Metadata: map[string]string{"completed_at": done.Format(time.RFC3339)}},
				{ID: "workspace:" + group.ID, Source: "workspace", Kind: "workspace", Path: anchor, WorkspaceID: group.ID, ProvidesWorkspace: true, WorkspaceEntry: true, CanonicalKey: "workspace:" + anchor},
			}}})
			service := cleanup.New([]integration.CleanupProvider{tmuxprovider.NewSource(), sbxprovider.NewSource(), gitprovider.NewSource(), workspace.NewSource(nil)})
			run := func(age time.Duration) workspacegc.Result {
				t.Helper()
				result, err := workspacegc.Run(context.Background(), store, service, nil, done.Add(age), workspacegc.Options{WorkspaceRoot: root, IgnoreRetention: true})
				if err != nil {
					t.Fatal(err)
				}
				return result
			}
			if result := run(24*time.Hour - time.Nanosecond); len(result.Retired)+len(result.Deleted)+len(result.Skipped) != 0 {
				t.Fatalf("before day one: %+v", result)
			}
			if _, err := os.Stat(calls); !os.IsNotExist(err) {
				t.Fatal("runtime CLI called before day one")
			}
			first := run(24 * time.Hour)
			if failFirst {
				if len(first.Skipped) != 1 || len(first.Retired) != 0 {
					t.Fatalf("partial failure: %+v", first)
				}
				if err := os.Remove(fail); err != nil {
					t.Fatal(err)
				}
			} else if len(first.Retired) != 1 || len(first.Skipped) != 0 {
				t.Fatalf("day one: %+v", first)
			}
			for index, age := range []time.Duration{25 * time.Hour, 7*24*time.Hour - time.Nanosecond} {
				wantRetired := 0
				if failFirst && index == 0 {
					wantRetired = 1
				}
				if result := run(age); len(result.Retired) != wantRetired || len(result.Deleted) != 0 || len(result.Skipped) != 0 {
					t.Fatalf("retry %s: %+v", age, result)
				}
			}
			for _, path := range []string{dirty, unknown, note} {
				if data, err := os.ReadFile(path); err != nil || string(data) != "valuable local data\n" {
					t.Fatalf("data lost at %s: %s %v", path, data, err)
				}
			}
			got, err := workspacegroup.Load(root)
			if err != nil || !reflect.DeepEqual(got, registry) {
				t.Fatalf("registration changed: %+v %v", got, err)
			}
			log, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			wantStops := 1
			if failFirst {
				wantStops = 2
			}
			if strings.Count(string(log), "sbx stop ") != wantStops || strings.Count(string(log), "tmux kill-session ") != 1 || strings.Contains(string(log), "rm --force") {
				t.Fatalf("unsafe/non-idempotent runtime commands:\n%s", log)
			}
		})
	}
}
