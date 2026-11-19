package workspace

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/cleanup"
	"radar/internal/integration"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
	"radar/internal/workspacelease"
)

func TestWorkspaceProtectedReferenceUnionRetainsBothBundlesWithoutMutatingSharedReferences(t *testing.T) {
	base := t.TempDir()
	t.Setenv("RADAR_SOCKET", filepath.Join(base, "radar.sock"))
	bundle := func(name string) workspacegroup.Workspace {
		return workspacegroup.Workspace{
			Path: filepath.Join(base, name, "anchor"), NotePath: filepath.Join(base, name, "notes", "note.md"),
			Members: []workspacegroup.Member{{Path: filepath.Join(base, name, "anchor", "member"), Repository: filepath.Join(base, name, "repository")}},
			Sandbox: &workspacegroup.Sandbox{Name: name, SharedDirectory: filepath.Join(base, name, "shared"),
				AdditionalMounts: []workspacegroup.SandboxMount{{Path: filepath.Join(base, name, "additional"), ReadOnly: true}},
				Mounts:           []string{filepath.Join(base, name, "effective") + ":ro"}},
		}
	}
	current, desired := bundle("current"), bundle("desired")
	release, err := acquireWorkspaceLeases(context.Background(), current, desired)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for _, group := range []workspacegroup.Workspace{current, desired} {
		for _, reference := range []string{group.NotePath, group.Members[0].Repository, group.Sandbox.SharedDirectory, group.Sandbox.AdditionalMounts[0].Path, strings.TrimSuffix(group.Sandbox.Mounts[0], ":ro")} {
			// References need shared deletion admission, not exclusive mutation
			// leases: different workspaces can use the same repository or mount.
			other := workspacegroup.Workspace{Path: filepath.Join(base, "other"), NotePath: reference}
			shared, err := acquireWorkspaceLeases(context.Background(), other)
			if err != nil {
				t.Fatal("shared reference globally serialized writers", err)
			}
			shared()
			agent, err := workspacelease.Acquire(reference, false)
			if err != nil {
				t.Fatal("reference admission blocked an agent", err)
			}
			agent()
			for _, target := range []string{reference, filepath.Dir(reference)} {
				started, resume := make(chan struct{}), make(chan struct{})
				close(resume)
				service := cleanup.New([]integration.CleanupProvider{activationCleanup{started: started, resume: resume}})
				_, err := service.Execute(context.Background(), protocol.CleanupPreview{Targets: []protocol.CleanupTarget{{Source: "fixture", Kind: "workspace", Path: target}}}, cleanup.ExecuteOptions{Mode: integration.CleanupConfirmed})
				if err == nil || !strings.Contains(err.Error(), "in use") {
					t.Fatal("actual cleanup entered a current/desired reference", target, err)
				}
				select {
				case <-started:
					t.Fatal("conflicting cleanup dispatched its provider")
				default:
				}
			}
		}
	}
}

func TestWorkspaceProtectedReferenceConflictReleasesEarlierBundleAndAncestors(t *testing.T) {
	base := t.TempDir()
	t.Setenv("RADAR_SOCKET", filepath.Join(base, "radar.sock"))
	group := workspacegroup.Workspace{Path: filepath.Join(base, "aa", "anchor"), NotePath: filepath.Join(base, "zz", "note.md")}
	blocked, err := workspacelease.Acquire(group.NotePath, true)
	if err != nil {
		t.Fatal(err)
	}
	if release, err := acquireWorkspaceLeases(context.Background(), group); err == nil {
		release()
		t.Fatal("reference conflict was ignored")
	}
	blocked()
	for _, path := range []string{base, group.Path, filepath.Dir(group.Path), group.NotePath, filepath.Dir(group.NotePath)} {
		release, err := workspacelease.Acquire(path, true)
		if err != nil {
			t.Fatal("reference conflict leaked partial admission", err)
		}
		release()
	}
}
