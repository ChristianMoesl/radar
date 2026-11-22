package diagnostics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/state"
	"radar/internal/workspacelease"
)

type bundleCheckDiagnosticProvider struct {
	cleanupDiagnosticProvider
	events    []string
	admit     func(context.Context) error
	preflight func(context.Context) error
	admitted  []protocol.CleanupTarget
}

func (p *bundleCheckDiagnosticProvider) AdmitCleanup(ctx context.Context, targets []protocol.CleanupTarget, mode integration.CleanupMode) (integration.CleanupProvider, func(), error) {
	if mode != integration.CleanupConfirmed {
		panic("wrong check mode")
	}
	p.events = append(p.events, "admit")
	p.admitted = append([]protocol.CleanupTarget{}, targets...)
	if p.admit != nil {
		if err := p.admit(ctx); err != nil {
			return nil, nil, err
		}
	}
	return p, func() { p.events = append(p.events, "release") }, nil
}
func (p *bundleCheckDiagnosticProvider) PreflightCleanup(ctx context.Context, _ []protocol.CleanupTarget, mode integration.CleanupMode) error {
	if mode != integration.CleanupConfirmed {
		panic("wrong preflight mode")
	}
	p.events = append(p.events, "preflight")
	if p.preflight != nil {
		return p.preflight(ctx)
	}
	return nil
}
func cleanupCheckFixture(t *testing.T) (*state.Store, *bundleCheckDiagnosticProvider, *cleanupDiagnosticRegistration, protocol.DiagnosticMaintenanceRequest, string) {
	t.Helper()
	store, _, path := fixture(t)
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	store.SetTasks([]protocol.Task{task(path, "test:ABC-123")})
	records, _, _ := store.InspectionSnapshot()
	p := &bundleCheckDiagnosticProvider{cleanupDiagnosticProvider: cleanupDiagnosticProvider{name: "git", targets: []protocol.CleanupTarget{{Source: "git", Kind: "worktree", ResourceID: "private-native-target", Path: path, Title: "private-title", Operation: map[string]string{"private": "private-command"}}}}}
	reg := &cleanupDiagnosticRegistration{cleanupDiagnosticProvider: cleanupDiagnosticProvider{name: "workspace"}, groups: []protocol.DiagnosticWorkspace{{ID: "registered", Path: path}}}
	req := protocol.DiagnosticMaintenanceRequest{Schema: protocol.DiagnosticsVersion, Operation: "cleanup_check", Mode: "check", Scope: "origin", ParentID: records[0].ID, Workspace: path}
	return store, p, reg, req, path
}
func noPending() bool { return false }

func TestCheckCleanupBundleSharesRealGuardsPreservesOrderAndReleasesWithoutCleanup(t *testing.T) {
	for _, scope := range []string{"origin", "feedback", "installation"} {
		for _, numeric := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/numeric=%t", scope, numeric), func(t *testing.T) {
				store, p, reg, req, path := cleanupCheckFixture(t)
				req.Scope = scope
				if scope == "installation" {
					req.Workspace = ""
				}
				records, _, revision := store.InspectionSnapshot()
				if numeric {
					req.ParentID = fmt.Sprint(records[0].NumericID)
				}
				other := p.targets[0]
				other.ResourceID = "private-second"
				p.targets = append(p.targets, other) // Same lease, distinct ordered native targets.
				p.preflight = func(context.Context) error {
					release, err := workspacelease.Acquire(path, false)
					if err == nil {
						release()
						t.Error("actual bundle lease was not held during preflight")
					}
					return nil
				}
				view, err := CheckCleanupBundle(context.Background(), store, req, noPending, integration.NewRegistry(p, reg))
				if err != nil || view.Status != "guards_passed_at_check" || view.TargetCount != 2 || view.CleanupDispatched || view.ExecutionEligibility != "unknown_last_moment_provider_guards_not_run" || view.RecordID != records[0].ID || view.TaskID != records[0].NumericID || view.Scope != scope || view.Revision != revision || store.Revision() != revision {
					t.Fatalf("view=%+v err=%v", view, err)
				}
				if p.calls != 1 || !reflect.DeepEqual(p.events, []string{"admit", "preflight", "release"}) || !reflect.DeepEqual(p.admitted, p.targets) {
					t.Fatalf("native path/order changed: %+v %+v", p.events, p.admitted)
				}
				release, err := workspacelease.Acquire(path, false)
				if err != nil {
					t.Fatal("guard leaked", err)
				}
				release()
				raw, _ := json.Marshal(view)
				if strings.Contains(string(raw), "private-") || strings.Contains(string(raw), path) {
					t.Fatal("private data escaped", string(raw))
				}
			})
		}
	}
}

func TestCheckCleanupBundleFailuresNeverDispatchAndReleaseAdmission(t *testing.T) {
	for _, kind := range []string{"preview_error", "empty", "oversized", "unsupported_provider", "duplicate_provider", "unsupported_effect", "native_identity_missing", "admission_error", "preflight_error", "path_contention", "sandbox_name_contention", "stopped_owner", "cancel_before", "cancel_preview", "cancel_admission", "cancel_final_preflight", "revision_preview", "revision_preflight", "registration_preview", "registration_preflight", "pending_before", "pending_during", "outside_scope", "unregistered_scope", "duplicate_scope", "duplicate_registration_id", "invalid_registration"} {
		t.Run(kind, func(t *testing.T) {
			store, p, reg, req, path := cleanupCheckFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			pending := false
			var release func()
			registry := integration.NewRegistry(p, reg)
			switch kind {
			case "preview_error":
				p.err = errors.New("private-provider-error")
			case "empty":
				p.targets = nil
			case "oversized":
				p.targets = make([]protocol.CleanupTarget, 513)
			case "unsupported_provider":
				p.name = "private-provider"
			case "duplicate_provider":
				registry = integration.NewRegistry(p, p, reg)
			case "unsupported_effect":
				p.targets[0].Kind = "private-kind"
			case "native_identity_missing":
				p.targets[0].ResourceID = ""
			case "admission_error":
				p.admit = func(context.Context) error { return errors.New("private-admission-error") }
			case "preflight_error":
				p.preflight = func(context.Context) error { return errors.New("private-preflight-error") }
			case "path_contention":
				var err error
				release, err = workspacelease.Acquire(path, false)
				if err != nil {
					t.Fatal(err)
				}
			case "sandbox_name_contention":
				p.name = "sbx"
				p.targets[0].Source = "sbx"
				p.targets[0].Kind = "sandbox"
				var err error
				release, err = workspacelease.AcquireSandbox(p.targets[0].ResourceID, false)
				if err != nil {
					t.Fatal(err)
				}
			case "stopped_owner":
				retirementOwner(t, path)
			case "cancel_before":
				cancel()
			case "cancel_preview":
				p.run = func(context.Context) { cancel() }
			case "cancel_admission":
				p.admit = func(context.Context) error { cancel(); return nil }
			case "cancel_final_preflight":
				p.preflight = func(context.Context) error { cancel(); return nil }
			case "revision_preview":
				p.run = func(context.Context) { store.SetTasks([]protocol.Task{task(path, "test:ABC-124")}) }
			case "revision_preflight":
				p.preflight = func(context.Context) error { store.SetTasks([]protocol.Task{task(path, "test:ABC-124")}); return nil }
			case "registration_preview":
				p.run = func(context.Context) { reg.groups[0].ID = "changed" }
			case "registration_preflight":
				p.preflight = func(context.Context) error { reg.groups[0].ID = "changed"; return nil }
			case "pending_before":
				pending = true
			case "pending_during":
				p.preflight = func(context.Context) error { pending = true; return nil }
			case "outside_scope":
				req.Workspace = filepath.Join(path, "other")
				reg.groups[0].Path = req.Workspace
			case "unregistered_scope":
				req.Workspace = filepath.Join(path, "other")
			case "duplicate_scope":
				reg.groups = append(reg.groups, reg.groups[0])
			case "duplicate_registration_id":
				other := reg.groups[0]
				other.Path = filepath.Join(path, "other")
				reg.groups = append(reg.groups, other)
			case "invalid_registration":
				reg.groups[0].ID = ""
			}
			defer func() {
				if release != nil {
					release()
				}
			}()
			view, err := CheckCleanupBundle(ctx, store, req, func() bool { return pending }, registry)
			if err == nil && view.Status != "not_passed_or_unavailable" {
				t.Fatalf("false pass: %+v", view)
			}
			if err != nil && strings.Contains(err.Error(), "private-") {
				t.Fatal("raw error escaped", err)
			}
			if view.CleanupDispatched {
				t.Fatal("dispatch attested")
			}
			if len(p.events) > 0 && p.events[0] == "admit" && kind != "admission_error" && p.events[len(p.events)-1] != "release" {
				t.Fatalf("admission leaked: %v", p.events)
			}
			if release != nil {
				release()
				release = nil
			}
			owned, err := workspacelease.Acquire(path, false)
			if err != nil {
				t.Fatal("path lease leaked", err)
			}
			owned()
		})
	}
}

func TestCheckCleanupBundleRejectsInvalidRequestsBeforeAnyNativeProviderWork(t *testing.T) {
	cases := map[string]func(*protocol.DiagnosticMaintenanceRequest){"old_schema": func(r *protocol.DiagnosticMaintenanceRequest) { r.Schema-- }, "operation": func(r *protocol.DiagnosticMaintenanceRequest) { r.Operation = "cleanup" }, "apply": func(r *protocol.DiagnosticMaintenanceRequest) { r.Mode = "apply" }, "expired": func(r *protocol.DiagnosticMaintenanceRequest) { r.Mode = "expired" }, "plan": func(r *protocol.DiagnosticMaintenanceRequest) { r.PlanID = strings.Repeat("a", 64) }, "parent": func(r *protocol.DiagnosticMaintenanceRequest) { r.ParentID = "" }, "long_parent": func(r *protocol.DiagnosticMaintenanceRequest) { r.ParentID = strings.Repeat("x", 513) }, "scope": func(r *protocol.DiagnosticMaintenanceRequest) { r.Scope = "all" }, "relative": func(r *protocol.DiagnosticMaintenanceRequest) { r.Workspace = "relative" }, "missing_scope": func(r *protocol.DiagnosticMaintenanceRequest) { r.Workspace = "" }, "installation_path": func(r *protocol.DiagnosticMaintenanceRequest) { r.Scope = "installation" }, "long_path": func(r *protocol.DiagnosticMaintenanceRequest) { r.Workspace = "/" + strings.Repeat("x", 4096) }}
	for kind, mutate := range cases {
		t.Run(kind, func(t *testing.T) {
			store, p, reg, req, _ := cleanupCheckFixture(t)
			reads := 0
			reg.observe = func() { reads++ }
			mutate(&req)
			_, err := CheckCleanupBundle(context.Background(), store, req, noPending, integration.NewRegistry(p, reg))
			if err == nil || p.calls != 0 || reads != 0 || len(p.events) != 0 {
				t.Fatal("invalid request reached native work", err, reads, p.events)
			}
		})
	}
}
