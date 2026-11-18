package diagnostics

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/workspacelease"
)

func TestCleanupPreviewLeaseEvidenceIsPerTargetNotExecutionPermission(t *testing.T) {
	for _, kind := range []string{"missing", "free", "shared", "exclusive", "relative", "pathless", "registered-and-leased"} {
		t.Run(kind, func(t *testing.T) {
			store, _, path := fixture(t)
			store.SetTasks([]protocol.Task{task(path, "test:ABC-123")})
			records, _, revision := store.InspectionSnapshot()
			provider, registration := manualPreviewFixture(t, 2)
			for i := range provider.targets {
				provider.targets[i].Path = path
			}
			expected, eligibility := "unknown_no_lock_file", "unknown_fresh_execution_guards_not_run"
			switch kind {
			case "free", "shared", "exclusive", "registered-and-leased":
				release, err := workspacelease.Acquire(path, kind == "exclusive")
				if err != nil {
					t.Fatal(err)
				}
				if kind == "free" {
					release()
					expected = "exclusive_available_at_probe"
				} else {
					defer release()
					expected, eligibility = "contended_at_probe", "blocked_lease_contention"
				}
				if kind == "registered-and-leased" {
					retirementOwner(t, path)
					eligibility = "blocked_registered_agent"
				}
			case "relative":
				for i := range provider.targets {
					provider.targets[i].Path = "private-relative"
				}
				expected = "unavailable"
			case "pathless":
				for i := range provider.targets {
					provider.targets[i].Path = ""
				}
				expected = "not_checked_no_target_path"
			}
			page, err := Probe(context.Background(), store, protocol.DiagnosticsRequest{Section: "cleanup_preview", ParentID: records[0].ID, Workspace: path}, false, integration.NewRegistry(provider, registration))
			if err != nil || len(page.Items) != 2 {
				t.Fatalf("page=%+v %v", page, err)
			}
			for _, row := range page.Items {
				if row.CleanupPreview.LeaseEvidence != expected || row.CleanupPreview.ExecutionEligibility != eligibility {
					t.Fatalf("%+v", row.CleanupPreview)
				}
			}
			raw, _ := json.Marshal(page)
			if strings.Contains(string(raw), path) || strings.Contains(string(raw), "private-") || store.Revision() != revision {
				t.Fatal("lease probe leaked private data or mutated cache")
			}
			if kind == "missing" {
				if _, err := os.Lstat(filepath.Join(filepath.Dir(os.Getenv("RADAR_SOCKET")), "leases")); !os.IsNotExist(err) {
					t.Fatal("preview created lease state", err)
				}
			}
		})
	}
}

func TestCleanupPreviewObservesCanonicalAliasLeaseWithoutExposingPaths(t *testing.T) {
	store, _, path := fixture(t)
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "private-alias")
	if err := os.Symlink(path, alias); err != nil {
		t.Fatal(err)
	}
	store.SetTasks([]protocol.Task{task(path, "test:ABC-123")})
	records, _, revision := store.InspectionSnapshot()
	provider, registration := manualPreviewFixture(t, 2)
	for i := range provider.targets {
		provider.targets[i].Path = alias
	}
	release, err := workspacelease.Acquire(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	page, err := Probe(context.Background(), store, protocol.DiagnosticsRequest{Section: "cleanup_preview", ParentID: records[0].ID, Workspace: path}, false, integration.NewRegistry(provider, registration))
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("alias preview=%+v %v", page, err)
	}
	for _, row := range page.Items {
		if row.CleanupPreview.LeaseEvidence != "contended_at_probe" || row.CleanupPreview.ExecutionEligibility != "blocked_lease_contention" {
			t.Fatal("alias preview missed the physical resource holder")
		}
	}
	raw, err := json.Marshal(page)
	if err != nil || strings.Contains(string(raw), path) || strings.Contains(string(raw), alias) || store.Revision() != revision {
		t.Fatal("alias preview leaked a path or mutated cached state", err)
	}
}

func TestCleanupPreviewLeaseDriftRejectsPageAndCursor(t *testing.T) {
	for _, kind := range []string{"created", "acquired", "released", "replaced", "removed"} {
		for _, during := range []bool{false, true} {
			t.Run(kind+map[bool]string{false: " between pages", true: " during inspection"}[during], func(t *testing.T) {
				store, _, path := fixture(t)
				store.SetTasks([]protocol.Task{task(path, "test:ABC-123")})
				records, _, _ := store.InspectionSnapshot()
				provider, registration := manualPreviewFixture(t, 2)
				for i := range provider.targets {
					provider.targets[i].Path = path
				}
				release := func() {}
				if kind != "created" {
					var err error
					release, err = workspacelease.Acquire(path, false)
					if err != nil {
						t.Fatal(err)
					}
					if kind != "released" {
						release()
						release = func() {}
					}
				}
				defer func() { release() }()
				mutate := func() {
					if kind == "created" || kind == "acquired" {
						var err error
						release, err = workspacelease.Acquire(path, false)
						if err != nil {
							t.Fatal(err)
						}
					} else if kind == "released" {
						release()
						release = func() {}
					} else {
						dir := filepath.Join(filepath.Dir(os.Getenv("RADAR_SOCKET")), "leases")
						entries, err := os.ReadDir(dir)
						if err != nil || len(entries) != 1 {
							t.Fatal("missing native lease", err)
						}
						file := filepath.Join(dir, entries[0].Name())
						if err := os.Rename(file, file+".old"); err != nil {
							t.Fatal(err)
						}
						if kind == "replaced" {
							if err := os.WriteFile(file, nil, 0600); err != nil {
								t.Fatal(err)
							}
						}
					}
				}
				req := protocol.DiagnosticsRequest{Section: "cleanup_preview", ParentID: records[0].ID, Limit: 1}
				registry := integration.NewRegistry(provider, registration)
				if during {
					n := 0
					registration.observe = func() {
						n++
						if n == 2 {
							mutate()
						}
					}
				} else {
					page, err := Probe(context.Background(), store, req, false, registry)
					if err != nil || page.NextCursor == "" {
						t.Fatal("missing first page", err)
					}
					req.Cursor = page.NextCursor
					mutate()
				}
				page, err := Probe(context.Background(), store, req, false, registry)
				if err == nil || len(page.Items) != 0 || strings.Contains(err.Error(), path) {
					t.Fatalf("changed lease gave usable/private evidence: %+v %v", page, err)
				}
			})
		}
	}
}
