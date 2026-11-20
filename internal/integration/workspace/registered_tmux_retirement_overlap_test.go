package workspace_test

import (
	"context"
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
	"radar/internal/integration/tmux"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
	"radar/internal/workspacelease"
)

func pauseNativeRetirement(t *testing.T, owner workspacegroup.Workspace, service bool, phase string) (func(), <-chan error, string) {
	t.Helper()
	entered, resume, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(resume) }) }
	dir := installPausedNativeRetirement(t, owner, phase, entered, resume)
	target := protocol.CleanupTarget{Source: "tmux", Kind: "session", ResourceID: owner.SessionName, Path: owner.Path, WorkspaceID: owner.ID, Operation: map[string]string{"session_name": owner.SessionName, "sandbox_name": owner.Sandbox.Name, "runtime_id": "tmux:session:12:34:$7"}}
	finished := make(chan error, 1)
	go func() {
		var err error
		if service {
			_, err = cleanup.New([]integration.CleanupProvider{tmux.Source{}}).Execute(context.Background(), protocol.CleanupPreview{Targets: []protocol.CleanupTarget{target}}, cleanup.ExecuteOptions{Mode: integration.CleanupRetire})
		} else {
			_, err = (tmux.Source{}).Cleanup(context.Background(), integration.CleanupRequest{Target: target, Mode: integration.CleanupRetire})
		}
		finished <- err
		close(done)
	}()
	t.Cleanup(func() { unblock(); <-done })
	select {
	case <-entered:
	case err := <-finished:
		t.Fatal("actual native retirement boundary not reached", phase, err)
	case <-time.After(5 * time.Second):
		t.Fatal("actual native retirement boundary not reached", phase)
	}
	return unblock, finished, dir
}

func TestNativeRetirementIOAllowsUnrelatedRecovery(t *testing.T) {
	for _, phase := range []string{"verification", "listing", "kill"} {
		for _, service := range []bool{false, true} {
			for _, sandbox := range []bool{false, true} {
				for _, entry := range []string{"open", "create-existing"} {
					t.Run(fmt.Sprintf("%s/service=%t/%s/sandbox=%t", phase, service, entry, sandbox), func(t *testing.T) {
						root, owner, other, _ := sandboxOverlapFixture(t, sandbox)
						before, err := workspacegroup.Load(root)
						if err != nil {
							t.Fatal(err)
						}
						unblock, finished, dir := pauseNativeRetirement(t, owner, service, phase)
						runner := &recoverySandboxRunner{}
						recovered, done := make(chan error, 1), make(chan struct{})
						go func() {
							recovered <- recoverSandboxOverlap(context.Background(), runner, root, other, entry)
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
							t.Fatal("unrelated recovery stalled inside actual native retirement I/O")
						}
						log := strings.Join(runner.calls, "\n")
						if !strings.Contains(log, "tmux new-session") || !strings.Contains(log, "tmux switch-client") || sandbox && !strings.Contains(log, "sbx create") {
							t.Fatal("actual recovery effects missing", log)
						}
						after, err := workspacegroup.Load(root)
						if err != nil || !reflect.DeepEqual(before, after) {
							t.Fatal("retirement changed registration", err)
						}
						for _, group := range []workspacegroup.Workspace{owner, other} {
							data, err := os.ReadFile(group.NotePath)
							if err != nil || string(data) != "canonical "+group.Name+"\n" {
								t.Fatal("canonical note changed", err)
							}
						}
						unblock()
						if err := <-finished; err != nil {
							t.Fatal(err)
						}
						calls, err := os.ReadFile(filepath.Join(dir, "calls"))
						if err != nil || !strings.Contains(string(calls), "kill-session -t $7") || strings.Contains(string(calls), "kill-session -t "+owner.SessionName) {
							t.Fatal("retirement did not address only the admitted ID", err, string(calls))
						}
						assertNativeRetirementAdmissionReleased(t, owner)
					})
				}
			}
		}
	}
}

func installPausedNativeRetirement(t *testing.T, owner workspacegroup.Workspace, phase string, entered chan<- struct{}, resume <-chan struct{}) string {
	t.Helper()
	dir := t.TempDir()
	script := fmt.Sprintf(`#!/bin/sh
set -eu
fixture=%q
phase=%q
name=%q
path=%q
printf '%%s\n' "$*" >> "$fixture/calls"
pause=false
case "$1" in
display-message)
 count=0
 [ ! -e "$fixture/count" ] || count=$(cat "$fixture/count")
 count=$((count + 1)); printf '%%s' "$count" > "$fixture/count"
 if [ "$phase" = verification ] && [ "$count" = 5 ]; then pause=true; fi ;;
list-sessions) [ "$phase" != listing ] || pause=true ;;
kill-session) [ "$phase" != kill ] || pause=true ;;
esac
if $pause && [ ! -e "$fixture/entered" ]; then
 touch "$fixture/entered"
 while [ ! -e "$fixture/resume" ]; do sleep 0.02; done
fi
case "$1" in
display-message)
 if [ -e "$fixture/killed" ]; then printf '12::\n'
 elif [ "$5" = '#{session_name}' ]; then printf '%%s\n' "$name"
 else printf '12:34:$7\n'; fi ;;
list-sessions) printf '12\t34\t$7\t%%s\t0\t1\t%%s\n' "$name" "$path" ;;
kill-session) touch "$fixture/killed" ;;
esac
`, dir, phase, owner.SessionName, owner.Path)
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(script), 0755); err != nil {
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
	t.Cleanup(func() { close(cancel); <-bridgeDone })
	return dir
}

func assertNativeRetirementAdmissionReleased(t *testing.T, owner workspacegroup.Workspace) {
	t.Helper()
	release, err := workspacelease.Acquire(owner.Path, true)
	if err != nil {
		t.Fatal("owner path admission leaked", err)
	}
	release()
	for _, key := range []string{"name:" + owner.SessionName, "runtime:tmux:session:12:34:$7"} {
		release, err := workspacelease.AcquireTmux(key, true)
		if err != nil {
			t.Fatal("native admission leaked", err)
		}
		release()
	}
}

func TestNativeRetirementIOProtectsConflicts(t *testing.T) {
	for _, conflict := range []string{"anchor", "alias", "shared-name"} {
		for _, service := range []bool{false, true} {
			for _, entry := range []string{"open", "create-existing"} {
				t.Run(fmt.Sprintf("%s/service=%t/%s", conflict, service, entry), func(t *testing.T) {
					root, owner, other, _ := sandboxOverlapFixture(t, true)
					unblock, finished, _ := pauseNativeRetirement(t, owner, service, "listing")
					selected := owner
					if conflict == "alias" {
						alias := filepath.Join(t.TempDir(), "alias")
						if err := os.Symlink(owner.Path, alias); err != nil {
							t.Fatal(err)
						}
						release, err := workspacelease.Acquire(alias, false)
						if err == nil {
							release()
							t.Fatal("physical alias escaped retirement admission")
						}
					}
					if conflict == "shared-name" {
						other.SessionName = owner.SessionName
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
						if err == nil || !strings.Contains(err.Error(), "in use") {
							t.Fatal("matching recovery escaped retirement admission", err)
						}
					case <-time.After(5 * time.Second):
						unblock()
						<-done
						t.Fatal("matching recovery waited instead of reporting actual conflict")
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
						t.Fatal("unexpected retirement outcome", err)
					}
					assertNativeRetirementAdmissionReleased(t, owner)
				})
			}
		}
	}
}
