package diagnostics

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"radar/internal/integration"
	"radar/internal/protocol"
)

type recreationDiagnosticProvider struct {
	calls int
	run   func(context.Context, string, string) (protocol.DiagnosticRecreationPreview, error)
}

func (*recreationDiagnosticProvider) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: "workspace"}
}
func (*recreationDiagnosticProvider) DiagnosticWorkspaceRegistrations() ([]protocol.DiagnosticWorkspace, error) {
	panic("no fallback inventory")
}
func (p *recreationDiagnosticProvider) DiagnosticRecreationPreview(ctx context.Context, id, scope string) (protocol.DiagnosticRecreationPreview, error) {
	p.calls++
	return p.run(ctx, id, scope)
}
func validRecreationView(id string) protocol.DiagnosticRecreationPreview {
	return protocol.DiagnosticRecreationPreview{ID: id, PreviewID: "recreation-preview:" + strings.Repeat("a", 64), Status: "recreate", PreviousState: "running", RuntimeIdentityObserved: true, ExecutionEligibility: "unknown_fresh_execution_guards_not_run", OwnershipEvidence: "unavailable", LeaseEvidence: "unavailable"}
}

func TestRecreationPreviewUsesOnlyExactNativeCapability(t *testing.T) {
	store, _, path := fixture(t)
	req := protocol.DiagnosticsRequest{Section: "recreation_preview", ParentID: "registered", Workspace: path, Limit: 20}
	p := &recreationDiagnosticProvider{run: func(ctx context.Context, id, scope string) (protocol.DiagnosticRecreationPreview, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > CleanupPreviewTimeout || id != req.ParentID || scope != path {
			t.Fatal("unbounded or wrong native selection")
		}
		return validRecreationView(id), nil
	}}
	registry := integration.NewRegistry(p)
	page, err := Probe(context.Background(), store, req, false, registry)
	if err != nil || p.calls != 1 || page.Schema != protocol.DiagnosticsVersion || page.Total != 1 || len(page.Items) != 1 || !page.Complete || page.NextCursor != "" || page.Evidence != recreationPreviewEvidence || page.ResourceRefreshPending || page.Items[0].RecreationPreview == nil {
		t.Fatalf("missing bounded native proposal: %+v %v", page, err)
	}
	if _, err := Read(store, req, false, registry); err == nil || p.calls != 1 {
		t.Fatal("ordinary inspection invoked native preview")
	}
}

func TestRecreationPreviewRejectsSelectorsBeforeProvider(t *testing.T) {
	store, _, path := fixture(t)
	base := protocol.DiagnosticsRequest{Section: "recreation_preview", ParentID: "registered", Workspace: path}
	for name, change := range map[string]func(*protocol.DiagnosticsRequest){
		"missing": func(q *protocol.DiagnosticsRequest) { q.ParentID = "" }, "oversize": func(q *protocol.DiagnosticsRequest) { q.ParentID = strings.Repeat("x", 513) }, "section": func(q *protocol.DiagnosticsRequest) { q.Section = "runtime_probe" }, "relative": func(q *protocol.DiagnosticsRequest) { q.Workspace = "relative" }, "row": func(q *protocol.DiagnosticsRequest) { q.ID = "row" }, "state": func(q *protocol.DiagnosticsRequest) { q.State = "done" }, "cursor": func(q *protocol.DiagnosticsRequest) { q.Cursor = "opaque" }, "mode": func(q *protocol.DiagnosticsRequest) { q.CleanupMode = "manual" }, "workspace_id": func(q *protocol.DiagnosticsRequest) { q.WorkspaceID = "registered" }, "limit": func(q *protocol.DiagnosticsRequest) { q.Limit = 51 },
	} {
		t.Run(name, func(t *testing.T) {
			p := &recreationDiagnosticProvider{run: func(context.Context, string, string) (protocol.DiagnosticRecreationPreview, error) {
				panic("bad selector reached native planner")
			}}
			q := base
			change(&q)
			if _, err := PreviewRecreation(context.Background(), store, q, func() bool { return false }, integration.NewRegistry(p)); err == nil || p.calls != 0 {
				t.Fatal("selector ignored")
			}
		})
	}
}

func TestRecreationPreviewRejectsStaleUnavailableAndInvalidEvidence(t *testing.T) {
	for _, scenario := range []string{"pending_before", "pending_after", "cancel_before", "cancel_after", "revision_drift", "native_error", "wrong_id", "bad_preview_id", "bad_status", "bad_state", "missing_runtime", "skipped_with_runtime", "eligibility_claim", "missing_provider", "duplicate_provider"} {
		t.Run(scenario, func(t *testing.T) {
			store, _, _ := fixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			pending := scenario == "pending_before"
			if scenario == "cancel_before" {
				cancel()
			}
			p := &recreationDiagnosticProvider{run: func(context.Context, string, string) (protocol.DiagnosticRecreationPreview, error) {
				v := validRecreationView("registered")
				switch scenario {
				case "pending_after":
					pending = true
				case "cancel_after":
					cancel()
				case "revision_drift":
					store.SetTasks([]protocol.Task{{Title: "private"}})
				case "native_error":
					return v, errors.New("PRIVATE-native-error")
				case "wrong_id":
					v.ID = "other"
				case "bad_preview_id":
					v.PreviewID = "PRIVATE-plan"
				case "bad_status":
					v.Status = "recreated"
				case "bad_state":
					v.PreviousState = "healthy"
				case "missing_runtime":
					v.RuntimeIdentityObserved = false
				case "skipped_with_runtime":
					v.Status = "skipped"
				case "eligibility_claim":
					v.ExecutionEligibility = "eligible"
				}
				return v, nil
			}}
			registry := integration.NewRegistry(p)
			if scenario == "missing_provider" {
				registry = integration.NewRegistry()
			}
			if scenario == "duplicate_provider" {
				registry = integration.NewRegistry(p, p)
			}
			page, err := PreviewRecreation(ctx, store, protocol.DiagnosticsRequest{Section: "recreation_preview", ParentID: "registered"}, func() bool { return pending }, registry)
			if err == nil || len(page.Items) != 0 || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatalf("unsafe evidence: %+v %v", page, err)
			}
			if (scenario == "pending_before" || scenario == "cancel_before" || scenario == "duplicate_provider") && p.calls != 0 {
				t.Fatal("inadmissible request reached planner")
			}
		})
	}
}
