package diagnostics

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/integration"
	"radar/internal/integration/workspace"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

func TestExpiredCleanupPreviewKeepsUnknownWarningAndNativeAnchorDataDistinct(t *testing.T) {
	store, root, groups, _ := expiredFixture(t)
	provider, _ := manualPreviewFixture(t, 1)
	provider.mode = integration.CleanupExpired
	provider.targets[0].WorkspaceID = groups[0].ID
	provider.targets[0].Path = filepath.Join(groups[0].Path, "member")
	provider.targets[0].Safety = []protocol.CleanupSafety{{Kind: "private-future-guard", Message: "private-guard-details", BlocksAutomatic: true, Expires: true}}
	if err := os.WriteFile(filepath.Join(groups[0].Path, "private-local-data"), []byte("private-contents"), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(workspacegroup.Path(root))
	if err != nil {
		t.Fatal(err)
	}
	records, _, revision := store.InspectionSnapshot()
	registry := integration.NewRegistry(provider, workspace.Source{})
	req := protocol.DiagnosticsRequest{Section: "cleanup_preview", CleanupMode: "expired", WorkspaceID: groups[0].ID, ParentID: records[0].ID}
	page, err := Probe(context.Background(), store, req, false, registry)
	if err != nil || page.Total != 2 || !page.Complete {
		t.Fatalf("native expired proposal unavailable: %+v %v", page, err)
	}
	for _, row := range page.Items {
		view := row.CleanupPreview
		if len(view.Safety) != 1 || !view.Safety[0].BlocksAutomatic || view.Safety[0].Count != 1 {
			t.Fatalf("safety projection lost: %+v", view)
		}
		switch view.Provider {
		case "git":
			if view.Safety[0].Kind != "other_notice" || view.Safety[0].Expires || view.AutomaticBlockerCount != 1 {
				t.Fatalf("unknown warning waived by provider flag: %+v", view)
			}
		case "workspace":
			if view.Safety[0].Kind != "unknown_entries" || !view.Safety[0].Expires || view.AutomaticBlockerCount != 0 {
				t.Fatalf("native expired_entries lost known data semantics: %+v", view)
			}
		default:
			t.Fatalf("unexpected native provider: %+v", view)
		}
	}
	raw, _ := json.Marshal(page)
	if strings.Contains(string(raw), "private-") || strings.Contains(string(raw), groups[0].Path) {
		t.Fatal("private target or data escaped")
	}
	after, err := os.ReadFile(workspacegroup.Path(root))
	if err != nil || string(before) != string(after) || store.Revision() != revision {
		t.Fatal("proposal changed registration or published state", err)
	}
	if _, err := os.Stat(filepath.Join(groups[0].Path, "private-local-data")); err != nil {
		t.Fatal("proposal executed deletion", err)
	}
	req.Limit = 1
	first, err := Probe(context.Background(), store, req, false, registry)
	if err != nil || first.NextCursor == "" {
		t.Fatal("pagination fixture", err)
	}
	provider.targets[0].Safety[0].Kind = "private-different-future-guard"
	req.Cursor = first.NextCursor
	if _, err := Probe(context.Background(), store, req, false, registry); err == nil {
		t.Fatal("same-public-projection private safety drift reused cursor")
	}
}
