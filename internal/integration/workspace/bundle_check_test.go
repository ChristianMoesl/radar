package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/cleanup"
	"radar/internal/integration"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
	"radar/internal/workspacelease"
)

func TestBundleCheckUsesNativeWorkspaceAdmissionAndProtectedScan(t *testing.T) {
	for _, mode := range []integration.CleanupMode{integration.CleanupSafe, integration.CleanupConfirmed, integration.CleanupExpired} {
		for _, outcome := range []string{"success", "protected-anchor", "registered-shared", "disposable-shared", "busy-shared", "cancel-scan"} {
			t.Run(fmt.Sprintf("mode=%d/%s", mode, outcome), func(t *testing.T) {
				root, owner, other, target := sharedFilesystemFixture(t, true)
				assertNotes := sharedFilesystemNotesUnchanged(t, owner.NotePath, other.NotePath)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				var unblock func()
				var protected string
				switch outcome {
				case "protected-anchor", "registered-shared", "disposable-shared":
					path := owner.Path
					if outcome != "protected-anchor" {
						path = owner.Sandbox.SharedDirectory
					}
					protected = filepath.Join(path, "nested", ".git")
					if err := os.MkdirAll(protected, 0700); err != nil {
						t.Fatal(err)
					}
					if outcome == "registered-shared" {
						other.Sandbox.Mounts = append(other.Sandbox.Mounts, protected)
						updateSharedCleanupGroup(t, root, other)
					}
				case "busy-shared":
					var err error
					unblock, err = workspacelease.Acquire(owner.Sandbox.SharedDirectory, false)
					if err != nil {
						t.Fatal(err)
					}
					defer unblock()
				}
				scans := 0
				base := &fakeRunner{}
				source := scanCleanupSource{runnerCleanupSource: runnerCleanupSource{runner: base}, scan: func(a *cleanupAnchor, registry workspacegroup.Registry, group workspacegroup.Workspace, disposable []string, gotMode integration.CleanupMode) (anchorEntries, error) {
					scans++
					if gotMode != mode || group.ID != owner.ID {
						t.Fatal("native scan changed selected owner or mode", group, gotMode)
					}
					for _, path := range []string{owner.Path, owner.Sandbox.SharedDirectory} {
						if release, err := workspacelease.Acquire(path, false); err == nil {
							release()
							t.Fatal("native preflight scan lost bundle admission", path)
						}
					}
					entries, err := a.entries(registry, group, disposable, gotMode)
					if outcome == "cancel-scan" {
						cancel()
					}
					return entries, err
				}}
				err := cleanup.New([]integration.CleanupProvider{source}).CheckBundle(ctx, protocol.CleanupPreview{Targets: []protocol.CleanupTarget{target}}, cleanup.ExecuteOptions{Mode: mode})
				if (err == nil) != (outcome == "success" || outcome == "disposable-shared") || len(base.calls) != 0 {
					t.Fatal("native check bypassed protection or entered runtime cleanup", err, base.calls)
				}
				wantProblem := ""
				if outcome == "protected-anchor" {
					wantProblem = "unidentified Git checkout"
					if mode == integration.CleanupSafe {
						wantProblem = "workspace anchor contains unknown files"
					}
				}
				if outcome == "registered-shared" {
					wantProblem = "an external sandbox mount"
				}
				if wantProblem != "" && (err == nil || !strings.Contains(err.Error(), wantProblem)) {
					t.Fatal("check did not execute native protected-content policy", err, wantProblem)
				}
				// Shared temp contents are disposable by native policy, unlike an
				// anchor or registered protected reference. Checking still must
				// preserve them; do not invent a second classification policy.
				if outcome == "cancel-scan" && !errors.Is(err, context.Canceled) {
					t.Fatal("native check lost cancellation", err)
				}
				wantScans := 1
				if outcome == "registered-shared" || outcome == "busy-shared" {
					wantScans = 0 // The real shared-root admission rejects first.
				}
				if scans != wantScans {
					t.Fatal("check did not follow native admission/preflight ordering", scans, wantScans)
				}
				assertAnchorRegistered(t, root, owner.ID)
				assertAnchorRegistered(t, root, other.ID)
				assertAnchorFileKept(t, filepath.Join(owner.Path, ".pnpm-store", "content"))
				assertAnchorFileKept(t, filepath.Join(owner.Sandbox.SharedDirectory, "a-content"))
				if protected != "" {
					if _, err := os.Stat(protected); err != nil {
						t.Fatal("check changed protected content", err)
					}
				}
				assertNotes()
				assertDirectAnchorAdmissionReleased(t, owner.Path)
				if unblock != nil {
					unblock()
				}
				assertDirectAnchorAdmissionReleased(t, owner.Sandbox.SharedDirectory)
			})
		}
	}
}
