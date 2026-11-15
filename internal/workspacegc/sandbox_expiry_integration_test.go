package workspacegc_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"radar/internal/agentservice"
	"radar/internal/cleanup"
	"radar/internal/integration"
	sbxprovider "radar/internal/integration/sbx"
	"radar/internal/integration/workspace"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
	"radar/internal/state"
	"radar/internal/workspacegc"
	"radar/internal/workspacelease"
)

// The real providers run against a fake SBX executable, never a live sandbox.
func TestExpiredSandboxCleanupUsesRegistrationAndRetainsOwnershipOnFailure(t *testing.T) {
	for _, scenario := range []string{"missing ref", "collected ref", "already absent", "removal failure", "removal no-op", "inspection failure", "foreign mount", "replaced identity", "missing provider", "runtime reappears", "identity changes after preview", "runtime appears after preview", "registration changes", "held lease", "active task", "pending review", "durable agent", "post removal inspection failure", "runtime reappears at anchor"} {
		t.Run(scenario, func(t *testing.T) {
			home := t.TempDir()
			root, configHome := filepath.Join(home, "workspaces"), filepath.Join(home, "config")
			anchor := filepath.Join(root, "completed")
			write := func(path, text string, mode os.FileMode) {
				t.Helper()
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(text), mode); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", configHome)
			t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
			t.Setenv("RADAR_STATE", filepath.Join(home, "state.db"))
			write(filepath.Join(configHome, "radar", "config.yaml"), fmt.Sprintf("workspace: {root_dir: %q}\n", root), 0600)
			write(filepath.Join(anchor, "local.txt"), "local work\n", 0600)
			group := workspacegroup.Workspace{ID: workspacegroup.ID(anchor), Name: "completed", Path: anchor, Sandbox: &workspacegroup.Sandbox{Name: "registered-sandbox", Agent: "shell", Mounts: []string{anchor}}}
			if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
				t.Fatal(err)
			}
			absent, calls := filepath.Join(home, "absent"), filepath.Join(home, "calls")
			mount := anchor
			if scenario == "foreign mount" {
				mount = filepath.Join(home, "foreign")
			}
			if scenario == "already absent" || scenario == "runtime appears after preview" {
				write(absent, "", 0600)
			}
			verified := filepath.Join(home, "verified")
			failure := filepath.Join(home, "fail")
			if scenario == "removal failure" {
				write(failure, "", 0600)
			}
			write(filepath.Join(home, "bin", "sbx"), fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %q
case "$1" in
ls)
 [ %q != 'inspection failure' ] || exit 1
 if [ -f %q ]; then
  [ %q != 'post removal inspection failure' ] || exit 1
  if [ %q = 'runtime reappears at anchor' ]; then
   if [ -f %q ]; then printf '[{"id":"replacement","name":"registered-sandbox","status":"stopped","workspaces":[%%s]}]' '%q'; exit; fi
   : > %q
  fi
  [ %q != 'runtime reappears' ] || { printf '[{"id":"replacement","name":"registered-sandbox","status":"stopped","workspaces":[%%s]}]' '%q'; exit; }
  printf '[]'; exit
 fi
 printf '[{"id":"container-1","name":"registered-sandbox","status":"stopped","workspaces":[%%s]}]' '%q';;
rm)
 [ ! -f %q ] || exit 1
 [ %q = 'removal no-op' ] || : > %q;;
*) exit 42;;
esac
`, calls, scenario, absent, scenario, scenario, verified, anchor, verified, scenario, anchor, mount, failure, scenario, absent), 0755)
			t.Setenv("PATH", filepath.Join(home, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
			store, err := state.NewStore(slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				t.Fatal(err)
			}
			done := time.Date(2026, 1, 2, 10, 20, 30, 0, time.UTC)
			refs := []protocol.SourceRef{
				{ID: "test:ABC-123", EntityID: "test:ABC-123", Source: "test", Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkItem, Authority: protocol.SourceRefAuthorityPrimary, Signal: "done", CanonicalKey: "test:ABC-123", Metadata: map[string]string{"completed_at": done.Format(time.RFC3339)}},
				{ID: "workspace:" + group.ID, Source: "workspace", Kind: "workspace", Path: anchor, WorkspaceID: group.ID, ProvidesWorkspace: true, WorkspaceEntry: true, CanonicalKey: "workspace:" + anchor},
			}
			if scenario == "collected ref" || scenario == "replaced identity" {
				id := "container-1"
				if scenario == "replaced identity" {
					id = "old-container"
				}
				refs = append(refs, protocol.SourceRef{ID: "sbx:sandbox:registered-sandbox", Source: "sbx", Kind: "sandbox", Path: anchor, Metadata: map[string]string{"name": "registered-sandbox", "id": id}})
			}
			if scenario == "active task" {
				refs[1].Role = protocol.SourceRefRoleAuthoritative
				refs[1].Activity = protocol.ActivityBusy
			}
			if scenario == "pending review" {
				refs[1].AgentReview = true
			}
			if scenario == "durable agent" {
				agentRoot := filepath.Join(home, "agents")
				t.Setenv("RADAR_AGENT_ROOT", agentRoot)
				registration := agentservice.Registration{Version: 1, ID: "fixture", Workspace: protocol.AgentWorkspaceBinding{ID: group.ID, Path: anchor, Mode: "local"}}
				data, err := json.Marshal(registration)
				if err != nil {
					t.Fatal(err)
				}
				write(filepath.Join(agentRoot, "fixture", "registration.json"), string(data), 0600)
			}
			store.SetTasks([]protocol.Task{{Attention: "done", SourceRefs: refs}})
			if scenario == "held lease" {
				release, err := workspacelease.Acquire(anchor, false)
				if err != nil {
					t.Fatal(err)
				}
				defer release()
			}
			providers := []integration.CleanupProvider{workspace.NewSource(nil)}
			if scenario != "missing provider" {
				providers = append([]integration.CleanupProvider{sbxprovider.NewSource()}, providers...)
			}
			options := workspacegc.Options{WorkspaceRoot: root, GuardExecution: func(_ context.Context, _ protocol.Task, execute func() error) error {
				switch scenario {
				case "identity changes after preview":
					path := filepath.Join(home, "bin", "sbx")
					body, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					write(path, strings.ReplaceAll(string(body), "container-1", "container-2"), 0755)
				case "runtime appears after preview":
					if err := os.Remove(absent); err != nil {
						t.Fatal(err)
					}
				case "registration changes":
					group.Sandbox.Name = "replacement"
					if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
						t.Fatal(err)
					}
				}
				return execute()
			}}
			result, err := workspacegc.Run(context.Background(), store, cleanup.New(providers), nil, done.Add(8*24*time.Hour), options)
			if err != nil {
				t.Fatal(err)
			}
			success := scenario == "missing ref" || scenario == "collected ref" || scenario == "already absent"
			registry, err := workspacegroup.Load(root)
			if err != nil {
				t.Fatal(err)
			}
			log, _ := os.ReadFile(calls)
			if success {
				if len(result.Deleted) != 1 || len(result.Skipped) != 0 || len(registry.Workspaces) != 0 {
					t.Fatalf("result=%+v registry=%+v calls=%s", result, registry, log)
				}
				if _, err := os.Lstat(anchor); !os.IsNotExist(err) {
					t.Fatalf("anchor remains: %v", err)
				}
				want := 1
				if scenario == "already absent" {
					want = 0
				}
				if want == 1 && !strings.Contains(string(log), "rm --force container-1") {
					t.Fatalf("removal did not use inspected runtime ID: %s", log)
				}
				if strings.Count(string(log), "rm --force ") != want {
					t.Fatalf("removal count: %s", log)
				}
				if _, err := os.Stat(absent); err != nil {
					t.Fatalf("sandbox survives: %v calls=%s", err, log)
				}
			} else {
				wantSkipped := 1
				if scenario == "active task" {
					wantSkipped = 0
				} // Ineligible at planning time.
				if len(result.Deleted) != 0 || len(result.Skipped) != wantSkipped || len(registry.Workspaces) != 1 {
					t.Fatalf("lost ownership: result=%+v registry=%+v calls=%s", result, registry, log)
				}
				wantReason := map[string]string{
					"durable agent":                  "associated with agent fixture",
					"foreign mount":                  "no longer mounts",
					"replaced identity":              "identity changed",
					"identity changes after preview": "identity changed",
					"runtime appears after preview":  "appeared after preview",
					"runtime reappears at anchor":    "registered sandbox still exists",
				}[scenario]
				if wantReason != "" && !strings.Contains(result.Skipped[0].Reason, wantReason) {
					t.Fatalf("wrong safeguard: %s", result.Skipped[0].Reason)
				}
				if data, err := os.ReadFile(filepath.Join(anchor, "local.txt")); err != nil || string(data) != "local work\n" {
					t.Fatalf("local data lost: %q %v", data, err)
				}
				if scenario != "removal failure" && scenario != "removal no-op" && scenario != "runtime reappears" && scenario != "post removal inspection failure" && scenario != "runtime reappears at anchor" && strings.Contains(string(log), "rm --force") {
					t.Fatalf("unsafe runtime removal: %s", log)
				}
				if scenario == "removal failure" {
					if err := os.Remove(failure); err != nil {
						t.Fatal(err)
					}
					retry, err := workspacegc.Run(context.Background(), store, cleanup.New(providers), nil, done.Add(8*24*time.Hour), workspacegc.Options{WorkspaceRoot: root})
					if err != nil || len(retry.Deleted) != 1 || len(retry.Skipped) != 0 {
						t.Fatalf("retry=%+v err=%v", retry, err)
					}
					registry, err := workspacegroup.Load(root)
					if err != nil || len(registry.Workspaces) != 0 {
						t.Fatalf("retry registry=%+v err=%v", registry, err)
					}
				}
			}
		})
	}
}
