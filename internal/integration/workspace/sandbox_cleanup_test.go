package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"radar/internal/integration"
	workspacegroup "radar/internal/integration/workspace/group"
)

func TestAnchorRemovalRequiresVerifiedSandboxAbsence(t *testing.T) {
	for _, mode := range []integration.CleanupMode{integration.CleanupSafe, integration.CleanupConfirmed, integration.CleanupExpired} {
		for _, listing := range []string{`[]`, `[{"id":"container-1","name":"fixture"}]`, `[{"id":"container-1"}]`, `[{"name":"foreign"}]`, `invalid`} {
			t.Run(fmt.Sprintf("%v/%s", mode, listing), func(t *testing.T) {
				root, group, task := disposableAnchorFixture(t)
				configureDisposableEntries(t, ".pnpm-store")
				group.Sandbox = &workspacegroup.Sandbox{Name: "fixture", Agent: "shell", Mounts: []string{group.Path}}
				saveAnchorFixture(t, root, group)
				target := previewDisposableAnchor(t, task)
				_, err := removeWorkspaceAnchor(context.Background(), &fakeRunner{sbxListOutput: listing}, root, target, mode)
				absent := listing == "[]"
				if (err == nil) != absent {
					t.Fatalf("err=%v absent=%v", err, absent)
				}
				registry, err := workspacegroup.Load(root)
				if err != nil {
					t.Fatal(err)
				}
				if absent {
					if len(registry.Workspaces) != 0 {
						t.Fatal("registration remains")
					}
				} else {
					if len(registry.Workspaces) != 1 {
						t.Fatal("registration lost")
					}
					assertAnchorFileKept(t, filepath.Join(group.Path, ".pnpm-store", "v10", "content"))
					if _, err := os.Stat(group.Path); err != nil {
						t.Fatal(err)
					}
				}
			})
		}
	}
}
