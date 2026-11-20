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

func pauseManagedSessionCleanup(t *testing.T, owner workspacegroup.Workspace, service bool, phase string) (func(), <-chan error, string) {
	t.Helper()
	entered, resume, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(resume) }) }
	dir := installPausedNativeRetirement(t, owner, phase, entered, resume)
	// Name-only fallback has no workspace/anchor metadata to borrow from.
	target := protocol.CleanupTarget{Source: "tmux", Kind: "session", ResourceID: "$7", Title: owner.SessionName, SourceRefID: "tmux:session:12:34:$7"}
	finished := make(chan error, 1)
	go func() {
		var err error
		if service {
			_, err = cleanup.New([]integration.CleanupProvider{tmux.Source{}}).Execute(context.Background(), protocol.CleanupPreview{Targets: []protocol.CleanupTarget{target}}, cleanup.ExecuteOptions{})
		} else {
			_, err = (tmux.Source{}).Cleanup(context.Background(), integration.CleanupRequest{Target: target})
		}
		finished <- err
		close(done)
	}()
	t.Cleanup(func() { unblock(); <-done })
	select {
	case <-entered:
	case err := <-finished:
		t.Fatal("actual managed tmux boundary not reached", phase, err)
	case <-time.After(5 * time.Second):
		t.Fatal("actual managed tmux boundary not reached", phase)
	}
	return unblock, finished, dir
}

func TestManagedSessionIOAllowsUnrelatedRecovery(t *testing.T) {
	for _, phase := range []string{"verification", "kill"} {
		for _, service := range []bool{false, true} {
			for _, sandbox := range []bool{false, true} {
				for _, entry := range []string{"open", "create-existing"} {
					t.Run(fmt.Sprintf("%s/service=%t/%s/sandbox=%t", phase, service, entry, sandbox), func(t *testing.T) {
						root, owner, other, _ := sandboxOverlapFixture(t, sandbox)
						before, err := workspacegroup.Load(root)
						if err != nil {
							t.Fatal(err)
						}
						unblock, finished, dir := pauseManagedSessionCleanup(t, owner, service, phase)
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
							t.Fatal("unrelated recovery stalled inside actual ordinary managed tmux I/O")
						}
						log := strings.Join(runner.calls, "\n")
						if !strings.Contains(log, "tmux new-session") || !strings.Contains(log, "tmux switch-client") || sandbox && !strings.Contains(log, "sbx create") {
							t.Fatal("actual recovery effects missing", log)
						}
						after, err := workspacegroup.Load(root)
						if err != nil || !reflect.DeepEqual(before, after) {
							t.Fatal("managed session cleanup changed registration", err)
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
						if err != nil || !strings.Contains(string(calls), "kill-session -t $7") || strings.Contains(string(calls), "kill-session -t =") {
							t.Fatal("cleanup did not address admitted numeric ID", string(calls), err)
						}
						assertNativeRetirementAdmissionReleased(t, owner)
					})
				}
			}
		}
	}
}

func TestManagedSessionIOProtectsConflicts(t *testing.T) {
	for _, conflict := range []string{"anchor", "alias", "shared-name"} {
		for _, service := range []bool{false, true} {
			for _, entry := range []string{"open", "create-existing"} {
				t.Run(fmt.Sprintf("%s/service=%t/%s", conflict, service, entry), func(t *testing.T) {
					root, owner, other, _ := sandboxOverlapFixture(t, true)
					unblock, finished, _ := pauseManagedSessionCleanup(t, owner, service, "kill")
					selected := owner
					if conflict == "alias" {
						alias := filepath.Join(t.TempDir(), "alias")
						if err := os.Symlink(owner.Path, alias); err != nil {
							t.Fatal(err)
						}
						if release, err := workspacelease.Acquire(alias, false); err == nil {
							release()
							t.Fatal("physical alias escaped managed session admission")
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
							t.Fatal("matching recovery escaped managed cleanup admission", err)
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
						t.Fatal("unexpected managed cleanup outcome", err)
					}
					assertNativeRetirementAdmissionReleased(t, owner)
				})
			}
		}
	}
}
