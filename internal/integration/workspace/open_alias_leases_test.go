package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/workspacelease"
)

func TestOpenWorkspaceRejectsCleanupThroughPhysicalPathAliases(t *testing.T) {
	for _, live := range []bool{false, true} {
		for _, target := range []string{"anchor", "member", "missing-member"} {
			t.Run(target+"/"+map[bool]string{false: "recovery", true: "live"}[live], func(t *testing.T) {
				_, group := activationFixture(t)
				alias := filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(group.Path, alias); err != nil {
					t.Fatal(err)
				}
				if target != "anchor" {
					alias = filepath.Join(alias, "member")
				}
				if target == "missing-member" {
					if err := os.Remove(group.Members[0].Path); err != nil {
						t.Fatal(err)
					}
				}
				release, err := workspacelease.Acquire(alias, true)
				if err != nil {
					t.Fatal(err)
				}
				defer release()
				runner := &fakeRunner{hasSession: live}
				_, err = OpenRegisteredWorkspace(context.Background(), runner, group.Path, true)
				if err == nil || !strings.Contains(err.Error(), "in use by another operation") || len(runner.calls) != 0 {
					t.Fatalf("activation bypassed an alias cleanup lease: err=%v calls=%+v", err, runner.calls)
				}
				release()
				assertActivationLeasesReleased(t, group)
			})
		}
	}
}

func TestOpenWorkspaceLeasesRejectAliasCleanupUntilRuntimeReturn(t *testing.T) {
	for _, live := range []bool{false, true} {
		t.Run(map[bool]string{false: "recovery", true: "live"}[live], func(t *testing.T) {
			_, group := activationFixture(t)
			alias := filepath.Join(t.TempDir(), "alias")
			if err := os.Symlink(group.Path, alias); err != nil {
				t.Fatal(err)
			}
			base := &fakeRunner{hasSession: live}
			checked := false
			runner := activationRunner{Runner: base, run: func(ctx context.Context, cwd, name string, args ...string) (string, error) {
				if name == "tmux" && args[0] == "switch-client" {
					checked = true
					started, resume := make(chan struct{}), make(chan struct{})
					close(resume)
					service := cleanup.New([]integration.CleanupProvider{activationCleanup{started: started, resume: resume}})
					preview := protocol.CleanupPreview{Targets: []protocol.CleanupTarget{{Source: "fixture", Kind: "workspace", Path: alias}}}
					_, err := service.Execute(ctx, preview, cleanup.ExecuteOptions{Mode: integration.CleanupConfirmed})
					if err == nil || !strings.Contains(err.Error(), "in use by another operation") {
						t.Fatalf("alias cleanup admitted during runtime call: %v", err)
					}
					select {
					case <-started:
						t.Fatal("conflicting alias cleanup reached its provider")
					default:
					}
				}
				return base.Run(ctx, cwd, name, args...)
			}}
			if _, err := OpenRegisteredWorkspace(context.Background(), runner, group.Path, true); err != nil || !checked {
				t.Fatalf("activation=%v runtime boundary checked=%t", err, checked)
			}
			assertActivationLeasesReleased(t, group)
		})
	}
}
