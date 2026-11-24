package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"radar/internal/protocol"
)

func TestCleanupPlanPublicationReaderCannotIssueStaleRevision(t *testing.T) {
	testCleanupPlanPublicationRevision(t, true)
}

func TestCleanupPlanPublicationReaderStableRevisionStillIssues(t *testing.T) {
	testCleanupPlanPublicationRevision(t, false)
}

func testCleanupPlanPublicationRevision(t *testing.T, drift bool) {
	t.Helper()
	for _, adapter := range []string{"native", "diagnostic"} {
		for _, prior := range []bool{false, true} {
			name := adapter + "/empty"
			if prior {
				name = adapter + "/prior_plan"
			}
			t.Run(name, func(t *testing.T) {
				var s *Server
				var issue func() (string, error)
				var assertProviders func(int32)
				var sentinel string
				publicationReads := 2
				if adapter == "native" {
					var provider *cleanupPlanProvider
					s, provider, sentinel = cleanupPlanFixture(t)
					issue = func() (string, error) {
						view, err := s.cleanupPreview(context.Background(), 1)
						return view.PlanID, err
					}
					assertProviders = func(previews int32) {
						if provider.previews.Load() != previews || provider.admissions.Load() != 0 || provider.checks.Load() != 0 || provider.calls.Load() != 0 {
							t.Fatal("issuance changed planning count or crossed native execution", provider.previews.Load(), provider.admissions.Load(), provider.checks.Load(), provider.calls.Load())
						}
					}
				} else {
					var provider *manualAdapterSource
					var req protocol.DiagnosticMaintenanceRequest
					s, provider, req = manualAdapterFixture(t)
					sentinel = filepath.Join(req.Workspace, "retained-sentinel")
					if err := os.WriteFile(sentinel, []byte("intact"), 0600); err != nil {
						t.Fatal(err)
					}
					publicationReads = 4
					issue = func() (string, error) {
						view, err := s.diagnosticCleanup(context.Background(), req)
						return view.PlanID, err
					}
					assertProviders = func(previews int32) {
						if provider.previews.Load() != previews || provider.admissions.Load() != 0 || provider.preflights.Load() != 0 || provider.calls.Load() != 0 {
							t.Fatal("issuance changed planning count or crossed typed execution", provider.previews.Load(), provider.admissions.Load(), provider.preflights.Load(), provider.calls.Load())
						}
					}
				}
				oldID := ""
				if prior {
					var err error
					oldID, err = issue()
					if err != nil || !hexPlanID(oldID) {
						t.Fatal("prior plan fixture failed", oldID, err)
					}
				}
				revision := s.store.Revision()
				reads := 0
				s.SetResourcePublication(nil, func() protocol.Response {
					reads++
					// Both public preview paths end in the same locked plan issuer. Earlier
					// publication reads stay stable; only its final reader advances revision.
					if reads == publicationReads && drift {
						s.store.SetTasks(s.store.Tasks())
					}
					return protocol.Response{} // Non-pending publication does not undo drift.
				})
				id, err := issue()
				if reads != publicationReads {
					t.Fatalf("fixture missed final issuance reader: got %d want %d", reads, publicationReads)
				}
				if drift {
					if s.store.Revision() == revision {
						t.Fatal("fixture failed to advance revision")
					}
					if err == nil || id != "" {
						t.Errorf("publication reader revision drift issued stale preview: id=%q err=%v", id, err)
					}
					wantPlans := 0
					if prior {
						wantPlans = 1
					}
					if len(s.cleanupPlans) != wantPlans || (prior && s.cleanupPlans[1].id != oldID) {
						t.Error("rejected stale issuance retained a new identity or superseded its prior plan")
					}
				} else {
					if err != nil || !hexPlanID(id) || id == oldID || s.store.Revision() != revision || len(s.cleanupPlans) != 1 || s.cleanupPlans[1].id != id {
						t.Fatal("stable publication disabled live exact-plan issuance", id, err)
					}
				}
				previews := int32(1)
				if prior {
					previews++
				}
				assertProviders(previews)
				_, _, operations, _ := s.store.OperationInspectionSnapshot()
				if len(operations) != 0 {
					t.Fatal("preview started a managed operation")
				}
				if data, err := os.ReadFile(sentinel); err != nil || string(data) != "intact" {
					t.Fatal("preview changed the retained resource", err)
				}
				// Keeping an earlier identity after refusing issuance is not reviving it:
				// existing apply-time revision checks remain authoritative and unchanged.
			})
		}
	}
}
