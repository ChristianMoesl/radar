package workspace_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/integration/sbx"
	"radar/internal/integration/workspace"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
	"radar/internal/workspacelease"
)

func sandboxOverlapFixture(t *testing.T, sandbox bool) (string, workspacegroup.Workspace, workspacegroup.Workspace, protocol.CleanupTarget) {
	t.Helper()
	workspace.UseFixturePlatform(t, "darwin")
	home := t.TempDir()
	root := filepath.Join(home, "workspaces")
	for key, value := range map[string]string{"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, "config"), "XDG_CACHE_HOME": filepath.Join(home, "cache"), "RADAR_SOCKET": filepath.Join(home, "run", "radar.sock")} {
		t.Setenv(key, value)
	}
	config := filepath.Join(home, "config", "radar", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(config), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, fmt.Appendf(nil, "workspace: {root_dir: %q}\n", root), 0600); err != nil {
		t.Fatal(err)
	}
	groups := make([]workspacegroup.Workspace, 0, 2)
	for _, name := range []string{"cleanup", "unrelated"} {
		path := filepath.Join(root, name)
		note := filepath.Join(home, "notes", name+".md")
		for _, dir := range []string{path, filepath.Dir(note)} {
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(note, []byte("canonical "+name+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(note, filepath.Join(path, "notes.md")); err != nil {
			t.Fatal(err)
		}
		group := workspacegroup.Workspace{ID: workspacegroup.ID(path), Name: name, Path: path, SessionName: name + "-session", TaskLinkingKey: "obsidian:task:" + name, NotePath: note, Members: []workspacegroup.Member{}}
		if name == "cleanup" || sandbox {
			group.Sandbox = &workspacegroup.Sandbox{Name: name + "-sandbox", Agent: "shell", Mounts: []string{path}}
		}
		groups = append(groups, group)
	}
	owner, other := groups[0], groups[1] // Save normalizes/sorts its backing slice.
	if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: groups}); err != nil {
		t.Fatal(err)
	}
	target := protocol.CleanupTarget{Source: "sbx", Kind: "sandbox", ResourceID: owner.Sandbox.Name, WorkspaceID: owner.ID, Path: owner.Path, Operation: map[string]string{"session_name": owner.SessionName, "sandbox_name": owner.Sandbox.Name, "runtime_id": "lifetime-1"}}
	return root, owner, other, target
}

// Each recovery has its own native runner and real workspace/note/lease paths.
type recoverySandboxRunner struct {
	sessions map[string]string
	calls    []string
}

func (*recoverySandboxRunner) LookPath(string) error { return nil }
func (r *recoverySandboxRunner) Run(_ context.Context, _ string, name string, args ...string) (string, error) {
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	if name == "sbx" {
		if len(args) > 0 && args[0] == "version" {
			return `{"client":{"version":"v0.50.0"}}`, nil
		}
		if len(args) > 0 && (args[0] == "ls" || args[0] == "ports") {
			return "[]", nil
		}
	}
	if name == "tmux" && len(args) > 0 {
		switch args[0] {
		case "has-session":
			if r.sessions[strings.TrimPrefix(args[len(args)-1], "=")] == "" {
				return "", errors.New("missing")
			}
		case "new-session":
			if r.sessions == nil {
				r.sessions = map[string]string{}
			}
			for i, arg := range args {
				if arg == "-s" && i+1 < len(args) {
					r.sessions[args[i+1]] = "$9"
				}
			}
			return "12:34:$9 @1 %1", nil
		case "display-message":
			if len(args) == 5 {
				selected := strings.TrimSuffix(strings.TrimPrefix(args[3], "="), ":")
				id, sessionName := r.sessions[selected], selected
				if strings.HasPrefix(selected, "$") {
					for candidate, value := range r.sessions {
						if value == selected {
							id, sessionName = value, candidate
						}
					}
				}
				if args[4] == "#{session_name}" {
					return sessionName + "\n", nil
				}
				if args[4] == "#{pid}:#{session_created}:#{session_id}" {
					if id == "" {
						return "12::\n", nil
					}
					return "12:34:" + id + "\n", nil
				}
			}
		}
	}
	if name == "tmux" && len(args) > 0 && (args[0] == "new-window" || args[0] == "split-window") {
		return "@1 %1", nil
	}
	return "", nil
}
func recoverSandboxOverlap(ctx context.Context, runner workspace.Runner, root string, group workspacegroup.Workspace, entry string) error {
	if entry == "open" {
		_, err := workspace.OpenRegisteredWorkspace(ctx, runner, group.Path, true)
		return err
	}
	_, err := workspace.Create(ctx, runner, workspace.CreateOptions{WorkspaceRoot: root, TaskLinkingKey: group.NoteKey(), Switch: true})
	return err
}

func pauseRegisteredSandbox(t *testing.T, owner workspacegroup.Workspace, target protocol.CleanupTarget, service bool, phase string) (func(), <-chan error, string) {
	t.Helper()
	entered, resume, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(resume) }) }
	fixture := installPausedSandbox(t, owner, phase, entered, resume)
	finished := make(chan error, 1)
	mode := integration.CleanupConfirmed
	if phase == "retirement" {
		mode = integration.CleanupRetire
	}
	go func() {
		var err error
		if service {
			_, err = cleanup.New([]integration.CleanupProvider{sbx.Source{}}).Execute(context.Background(), protocol.CleanupPreview{Targets: []protocol.CleanupTarget{target}}, cleanup.ExecuteOptions{Mode: mode})
		} else {
			_, err = (sbx.Source{}).Cleanup(context.Background(), integration.CleanupRequest{Target: target, Mode: mode})
		}
		finished <- err
		close(done)
	}()
	t.Cleanup(func() { unblock(); <-done })
	select {
	case <-entered:
	case err := <-finished:
		t.Fatal("actual provider boundary not reached", phase, err)
	case <-time.After(5 * time.Second):
		t.Fatal("actual provider boundary not reached", phase)
	}
	return unblock, finished, fixture
}

func assertSandboxAdmissionReleased(t *testing.T, owner workspacegroup.Workspace) {
	t.Helper()
	for _, admit := range []func() (func(), error){func() (func(), error) { return workspacelease.Acquire(owner.Path, true) }, func() (func(), error) { return workspacelease.AcquireSandbox(owner.Sandbox.Name, true) }} {
		release, err := admit()
		if err != nil {
			t.Fatal("leaked admission", err)
		}
		release()
	}
}

func TestRegisteredSandboxIOAllowsUnrelatedRecovery(t *testing.T) {
	for _, phase := range []string{"lookup", "removal", "absence", "retirement"} {
		for _, service := range []bool{false, true} {
			for _, sandbox := range []bool{false, true} {
				for _, entry := range []string{"open", "create-existing"} {
					t.Run(fmt.Sprintf("%s/service=%t/%s/sandbox=%t", phase, service, entry, sandbox), func(t *testing.T) {
						root, owner, other, target := sandboxOverlapFixture(t, sandbox)
						before, err := workspacegroup.Load(root)
						if err != nil {
							t.Fatal(err)
						}
						unblock, finished, fixture := pauseRegisteredSandbox(t, owner, target, service, phase)
						recovery := &recoverySandboxRunner{}
						recovered, done := make(chan error, 1), make(chan struct{})
						go func() {
							recovered <- recoverSandboxOverlap(context.Background(), recovery, root, other, entry)
							close(done)
						}()
						t.Cleanup(func() { unblock(); <-done })
						select {
						case err := <-recovered:
							if err != nil {
								t.Fatal("recovery fixture failed", err)
							}
						case <-time.After(5 * time.Second):
							unblock()
							<-done
							if err := <-recovered; err != nil {
								t.Fatal("recovery fixture failed after release", err)
							}
							t.Fatal("unrelated recovery stalled inside actual registered SBX I/O")
						}
						log := strings.Join(recovery.calls, "\n")
						if !strings.Contains(log, "tmux new-session") || !strings.Contains(log, "tmux switch-client") || sandbox && !strings.Contains(log, "sbx create") {
							t.Fatal("actual recovery effects missing", log)
						}
						after, err := workspacegroup.Load(root)
						if err != nil || !reflect.DeepEqual(before, after) {
							t.Fatal("registrations changed", err)
						}
						for _, g := range []workspacegroup.Workspace{owner, other} {
							data, err := os.ReadFile(g.NotePath)
							if err != nil || string(data) != "canonical "+g.Name+"\n" {
								t.Fatal("canonical note changed", err)
							}
						}
						unblock()
						if err := <-finished; err != nil {
							t.Fatal(err)
						}
						calls, err := os.ReadFile(filepath.Join(fixture, "calls"))
						if err != nil {
							t.Fatal(err)
						}
						want := "rm --force lifetime-1"
						if phase == "retirement" {
							want = "stop " + owner.Sandbox.Name
							if strings.Contains(string(calls), "rm ") {
								t.Fatal("retirement removed runtime")
							}
						}
						if !strings.Contains(string(calls), want) {
							t.Fatal("expected owned runtime command missing", string(calls))
						}
						assertSandboxAdmissionReleased(t, owner)
					})
				}
			}
		}
	}
}

func TestRegisteredSandboxIOProtectsConflicts(t *testing.T) {
	for _, conflict := range []string{"anchor", "alias", "shared-name"} {
		for _, service := range []bool{false, true} {
			for _, entry := range []string{"open", "create-existing"} {
				t.Run(fmt.Sprintf("%s/service=%t/%s", conflict, service, entry), func(t *testing.T) {
					root, owner, other, target := sandboxOverlapFixture(t, true)
					unblock, finished, _ := pauseRegisteredSandbox(t, owner, target, service, "lookup")
					selected := owner
					if conflict == "alias" {
						alias := filepath.Join(t.TempDir(), "alias")
						if err := os.Symlink(owner.Path, alias); err != nil {
							t.Fatal(err)
						}
						release, err := workspacelease.Acquire(alias, false)
						if err == nil {
							release()
							t.Fatal("physical alias escaped deletion admission")
						}
					}
					if conflict == "shared-name" {
						other.Sandbox.Name = owner.Sandbox.Name
						selected = other
						if err := workspacegroup.Update(root, func(registry *workspacegroup.Registry) error {
							for i := range registry.Workspaces {
								if registry.Workspaces[i].ID == other.ID {
									registry.Workspaces[i] = other
								}
							}
							return nil
						}); err != nil {
							t.Fatal(err)
						}
					}
					runner := &recoverySandboxRunner{}
					recovered, done := make(chan error, 1), make(chan struct{})
					go func() {
						recovered <- recoverSandboxOverlap(context.Background(), runner, root, selected, entry)
						close(done)
					}()
					t.Cleanup(func() { unblock(); <-done })
					select {
					case err := <-recovered:
						if err == nil || !strings.Contains(err.Error(), "in use by another operation") {
							t.Fatal("actual matching recovery escaped admission", err)
						}
					case <-time.After(5 * time.Second):
						unblock()
						<-done
						t.Fatal("conflicting recovery waited instead of reporting its conflict")
					}
					log := strings.Join(runner.calls, "\n")
					for _, effect := range []string{"tmux new-session", "tmux switch-client", "sbx create"} {
						if strings.Contains(log, effect) {
							t.Fatal("conflicting recovery had effects", log)
						}
					}
					unblock()
					err := <-finished
					if (err != nil) != (conflict == "shared-name") {
						t.Fatal("unexpected cleanup outcome", err)
					}
					assertSandboxAdmissionReleased(t, owner)
				})
			}
		}
	}
}

func TestRegisteredSandboxIOBlocksNameOnlyBypass(t *testing.T) {
	for _, service := range []bool{false, true} {
		t.Run(fmt.Sprint("service=", service), func(t *testing.T) {
			_, owner, _, target := sandboxOverlapFixture(t, false)
			unblock, finished, _ := pauseRegisteredSandbox(t, owner, target, service, "removal")
			result, done := make(chan error, 1), make(chan struct{})
			go func() {
				_, err := (sbx.Source{}).Cleanup(context.Background(), integration.CleanupRequest{Target: protocol.CleanupTarget{Source: "sbx", Kind: "sandbox", ResourceID: owner.Sandbox.Name}, Mode: integration.CleanupConfirmed})
				result <- err
				close(done)
			}()
			t.Cleanup(func() { unblock(); <-done })
			select {
			case err := <-result:
				if err == nil || !strings.Contains(err.Error(), "in use by another operation") {
					t.Fatal("name-only cleanup escaped runtime admission", err)
				}
			case <-time.After(5 * time.Second):
				unblock()
				<-done
				t.Fatal("name-only cleanup waited for the global callback instead of rejecting the actual conflict")
			}
			unblock()
			if err := <-finished; err != nil {
				t.Fatal(err)
			}
			assertSandboxAdmissionReleased(t, owner)
		})
	}
}

func installPausedSandbox(t *testing.T, owner workspacegroup.Workspace, phase string, entered chan<- struct{}, resume <-chan struct{}) string {
	t.Helper()
	dir := t.TempDir()
	listing := fmt.Sprintf(`[{"id":"lifetime-1","name":%q,"status":"running","workspaces":[%q]}]`, owner.Sandbox.Name, owner.Path)
	if err := os.WriteFile(filepath.Join(dir, "listing"), []byte(listing), 0600); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf(`#!/bin/sh
set -eu
fixture=%q
phase=%q
printf '%%s\n' "$*" >> "$fixture/calls"
pause=false
case "$phase:$1" in
lookup:ls) [ -e "$fixture/removed" ] || pause=true ;;
removal:rm|retirement:stop) pause=true ;;
absence:ls) [ ! -e "$fixture/removed" ] || pause=true ;;
esac
if $pause && [ ! -e "$fixture/entered" ]; then
 touch "$fixture/entered"
 while [ ! -e "$fixture/resume" ]; do sleep 0.02; done
fi
case "$1" in
ls) if [ -e "$fixture/removed" ]; then printf '[]'; else cat "$fixture/listing"; fi ;;
rm) touch "$fixture/removed" ;;
stop) : ;;
esac
`, dir, phase)
	if err := os.WriteFile(filepath.Join(dir, "sbx"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	bridgeDone, cancel := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(bridgeDone)
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-cancel:
				return
			case <-ticker.C:
				if _, err := os.Stat(filepath.Join(dir, "entered")); err == nil {
					close(entered)
					select {
					case <-resume:
					case <-cancel:
					}
					_ = os.WriteFile(filepath.Join(dir, "resume"), nil, 0600)
					return
				}
			}
		}
	}()
	t.Cleanup(func() { close(cancel); _ = os.WriteFile(filepath.Join(dir, "resume"), nil, 0600); <-bridgeDone })
	return dir
}
