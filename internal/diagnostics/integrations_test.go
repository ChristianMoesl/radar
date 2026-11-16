package diagnostics

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"testing"

	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/state"
)

// Every runtime/access method is a trap: cached capability discovery must rely
// on interface assertions only, including for disabled or failed providers.
type diagnosticIntegrationTrap struct{ name string }

func (p diagnosticIntegrationTrap) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: p.name, Label: "Label"}
}
func (diagnosticIntegrationTrap) Collect(context.Context, integration.CollectRequest) integration.CollectResult {
	panic("collection")
}
func (diagnosticIntegrationTrap) Status(context.Context, *slog.Logger) integration.StatusResult {
	panic("status/auth probe")
}
func (diagnosticIntegrationTrap) EnsureAuthentication(context.Context, integration.AuthenticationRequest) (integration.AuthenticationResult, error) {
	panic("authentication")
}
func (diagnosticIntegrationTrap) ResolveBindings(context.Context, integration.BindingRequest) integration.CollectResult {
	panic("bindings")
}
func (diagnosticIntegrationTrap) PreviewCleanup(context.Context, integration.CleanupPreviewRequest) ([]protocol.CleanupTarget, error) {
	panic("preview")
}
func (diagnosticIntegrationTrap) Cleanup(context.Context, integration.CleanupRequest) (protocol.CleanupTarget, error) {
	panic("cleanup")
}
func (diagnosticIntegrationTrap) DiagnosticRuntime(context.Context, string) protocol.DiagnosticRuntimeObservation {
	panic("runtime")
}
func (diagnosticIntegrationTrap) RateLimitSummary(context.Context, *slog.Logger) (string, error) {
	panic("rate limits")
}
func (diagnosticIntegrationTrap) Local() bool { panic("dynamic local check") }

func TestIntegrationInventoryCompletelyPagesCapabilitiesAndCachedStatusesWithoutProbes(t *testing.T) {
	store, _, path := fixture(t)
	registrations := []integration.Integration{}
	statuses := []protocol.SourceStatus{}
	for i := 0; i < 25; i++ {
		name := fmt.Sprintf("provider-%02d", i)
		registrations = append(registrations, diagnosticIntegrationTrap{name})
		statuses = append(statuses, protocol.SourceStatus{Name: name, Status: "error", Detail: "private-token in provider output", SourceRefCount: 99})
	}
	statuses = append(statuses, protocol.SourceStatus{Name: "historical", Status: "partial", Detail: "private-error"})
	store.SetSources(statuses)
	providers := integration.NewRegistry(registrations...)
	req := protocol.DiagnosticsRequest{Section: "integrations", Limit: 7}
	beforeRecords, beforeRefs, beforeRevision := store.InspectionSnapshot()
	beforeSources := store.Sources()
	beforeDisk, err := os.ReadFile(os.Getenv("RADAR_STATE"))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	firstCursor := ""
	for {
		page, err := Read(store, req, true, providers)
		if err != nil || page.Total != 26 || !page.ResourceRefreshPending || len(page.Items) > 7 || page.Schema != protocol.DiagnosticsVersion {
			t.Fatalf("page=%+v %v", page, err)
		}
		if firstCursor == "" {
			firstCursor = page.NextCursor
		}
		raw, _ := json.Marshal(page)
		if len(raw) > maxPageBytes || strings.Contains(string(raw), "private-") {
			t.Fatalf("private/unbounded payload: %s", raw)
		}
		for _, item := range page.Items {
			v := item.Integration
			if v == nil || v.ID != item.ID || seen[item.ID] || v.AccessEvidence != "unknown_not_checked" || v.FreshnessEvidence != "unknown_cache_freshness_not_established" || v.StatusEvidence != "cached_installation_status" {
				t.Fatalf("evidence=%+v", v)
			}
			seen[item.ID] = true
			if v.Name == "historical" {
				if v.RegistrationCount != 0 || v.Capabilities != nil || v.CachedStatus != "partial" {
					t.Fatalf("historical=%+v", v)
				}
			} else {
				if v.RegistrationCount != 1 || v.Capabilities == nil || !v.Capabilities.Collection || !v.Capabilities.StatusCheck || !v.Capabilities.InteractiveAuthentication || !v.Capabilities.BoundResolution || !v.Capabilities.Cleanup || !v.Capabilities.RuntimeProbe || !v.Capabilities.RateLimit || v.InstallationReportedRefCount == nil || *v.InstallationReportedRefCount != 99 {
					t.Fatalf("static capability evidence=%+v", v)
				}
			}
			exact, err := Read(store, protocol.DiagnosticsRequest{Section: "integrations", ID: item.ID}, true, providers)
			if err != nil || exact.Total != 1 || exact.Items[0].ID != item.ID {
				t.Fatal("opaque integration ID not selectable")
			}
		}
		if page.Complete {
			if page.NextCursor != "" {
				t.Fatal("terminal cursor")
			}
			break
		}
		req.Cursor = page.NextCursor
	}
	if len(seen) != 26 {
		t.Fatal("missing registrations beyond summary")
	}
	afterRecords, afterRefs, afterRevision := store.InspectionSnapshot()
	afterDisk, _ := os.ReadFile(os.Getenv("RADAR_STATE"))
	if beforeRevision != afterRevision || !reflect.DeepEqual(beforeRecords, afterRecords) || !reflect.DeepEqual(beforeRefs, afterRefs) || !reflect.DeepEqual(beforeSources, store.Sources()) || string(beforeDisk) != string(afterDisk) {
		t.Fatal("read changed production state")
	}
	// Global provider status must never imply a workspace relationship or health.
	page, err := Read(store, protocol.DiagnosticsRequest{Section: "integrations", Workspace: path}, false, providers)
	if err != nil || page.Total != 0 {
		t.Fatal("unrelated global integrations leaked into workspace scope")
	}
	for _, changed := range []protocol.DiagnosticsRequest{{Section: "integrations", Limit: 7, Cursor: firstCursor, ID: "provider-00"}, {Section: "integrations", Limit: 7, Cursor: firstCursor, Workspace: path}} {
		if _, err := Read(store, changed, true, providers); err == nil {
			t.Fatal("changed selector reused cursor")
		}
	}
	statuses[24].Detail = "changed but still withheld"
	store.SetSources(statuses)
	if _, err := Read(store, protocol.DiagnosticsRequest{Section: "integrations", Limit: 7, Cursor: firstCursor}, true, providers); err == nil {
		t.Fatal("hidden status drift reused cursor")
	}
	if _, err := Read(store, protocol.DiagnosticsRequest{Section: "integrations", Limit: 7, Cursor: firstCursor}, false, providers); err == nil {
		t.Fatal("pending-state drift reused cursor")
	}
	for _, invalid := range []protocol.DiagnosticsRequest{{Section: "integrations", ParentID: "unsupported"}, {Section: "integrations", State: "done"}} {
		if _, err := Read(store, invalid, false, providers); err == nil {
			t.Fatal("irrelevant selector accepted")
		}
	}
}

func TestIntegrationProjectionSeparatesScopedRefsFromInstallationStatusAndUnknownAccess(t *testing.T) {
	path := "/registered/workspace"
	refs := []state.SourceRefRecord{
		{Source: "provider", Active: true, ObservedAt: "2026-01-02T11:00:00+01:00", Snapshot: protocol.SourceRef{Path: path}},
		{Source: "provider", Active: false, ObservedAt: "2026-01-03T10:00:00Z", Snapshot: protocol.SourceRef{Metadata: map[string]string{"workspace_path": path + "/member", "token": "private-token"}}},
		{Source: "provider", Active: true, ObservedAt: "private-not-a-timestamp", Snapshot: protocol.SourceRef{Path: path}},
		{Source: "provider", Active: true, ObservedAt: "2026-01-04T10:00:00Z", Snapshot: protocol.SourceRef{Path: path + "-foreign"}},
		{Source: "cached-only", Active: false, Snapshot: protocol.SourceRef{Path: path}},
	}
	statuses := []protocol.SourceStatus{{Name: "provider", Status: "disabled", Detail: "private-provider-error", SourceRefCount: 80}, {Name: "unrelated", Status: "ok"}}
	providers := integration.NewRegistry(diagnosticIntegrationTrap{"provider"})
	items, err := integrationItems(protocol.DiagnosticsRequest{Workspace: path}, refs, statuses, nil, providers)
	if err != nil || len(items) != 2 {
		t.Fatalf("items=%+v %v", items, err)
	}
	providerID := ""
	for _, item := range items {
		v := item.Integration
		if v.InstallationReportedRefCount != nil {
			t.Fatal("workspace scope exposed installation-wide observation count")
		}
		if v.Name == "provider" {
			providerID = v.ID
			if v.ActiveRefs != 2 || v.InactiveRefs != 1 || v.UnknownRefObservedAt != 1 || v.LatestRefObservedAt != "2026-01-03T10:00:00Z" || v.CachedStatus != "disabled" || !v.DetailsAvailable || v.StatusEvidence != "cached_installation_status" {
				t.Fatalf("scope/freshness=%+v", v)
			}
		} else if v.CachedStatus != "unknown" || v.StatusEvidence != "unavailable" || v.RegistrationCount != 0 || v.Capabilities != nil || v.UnknownRefObservedAt != 1 {
			t.Fatalf("missing evidence reported healthy/empty: %+v", v)
		}
	}
	raw, _ := json.Marshal(items)
	if strings.Contains(string(raw), "private-") {
		t.Fatalf("raw private timestamp/error leaked: %s", raw)
	}
	for _, stateValue := range []string{"ok", "partial", "error", "disabled", "private-state"} {
		statuses[0].Status = stateValue
		items, err = integrationItems(protocol.DiagnosticsRequest{ID: providerID}, refs, statuses, nil, providers)
		if err != nil || len(items) != 1 {
			t.Fatal(err)
		}
		want := stateValue
		if strings.HasPrefix(want, "private") {
			want = "unknown"
		}
		if items[0].Integration.CachedStatus != want {
			t.Fatal("cached status not allowlisted")
		}
	}
}

func TestIntegrationIdentityClippingDuplicateAndInvalidEvidence(t *testing.T) {
	name := strings.Repeat("same-prefix", 60)
	providers := integration.NewRegistry(diagnosticIntegrationTrap{name + "first"}, diagnosticIntegrationTrap{name + "second"})
	items, err := integrationItems(protocol.DiagnosticsRequest{}, nil, nil, nil, providers)
	if err != nil || len(items) != 2 || items[0].ID == items[1].ID || items[0].Integration.Name != items[1].Integration.Name {
		t.Fatalf("full identity clipped: %+v %v", items, err)
	}
	duplicate := integration.NewRegistry(diagnosticIntegrationTrap{"duplicate"}, diagnosticIntegrationTrap{"duplicate"})
	items, err = integrationItems(protocol.DiagnosticsRequest{}, nil, []protocol.SourceStatus{{Name: "duplicate", Status: "ok"}, {Name: "duplicate", Status: "error", Detail: "private-error"}}, nil, duplicate)
	v := items[0].Integration
	if err != nil || v.RegistrationCount != 2 || v.Capabilities != nil || v.CapabilityEvidence != "ambiguous_registration" || v.CachedStatusCount != 2 || v.CachedStatus != "unknown" || v.StatusEvidence != "ambiguous_cached_status" || v.InstallationReportedRefCount != nil {
		t.Fatalf("ambiguous=%+v %v", v, err)
	}
	for _, tc := range []struct {
		providers integration.Registry
		refs      []state.SourceRefRecord
		statuses  []protocol.SourceStatus
	}{
		{providers: integration.NewRegistry(diagnosticIntegrationTrap{""})},
		{statuses: []protocol.SourceStatus{{Name: "", Status: "ok"}}},
		{refs: []state.SourceRefRecord{{Source: ""}}},
	} {
		if _, err := integrationItems(protocol.DiagnosticsRequest{}, tc.refs, tc.statuses, nil, tc.providers); err == nil {
			t.Fatal("unidentifiable evidence became an apparently complete inventory")
		}
	}
}

func TestIntegrationReadIncludesHiddenWorkspaceRelationshipsAndOmitsUnknownNativeCounts(t *testing.T) {
	store, _, path := fixture(t)
	store.SetCollection([]protocol.Task{{Attention: "done", Title: "private-title", SourceRefs: []protocol.SourceRef{{ID: "provider:ABC-123", EntityID: "provider:ABC-123", Source: "provider", Path: path, Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkItem, Authority: protocol.SourceRefAuthorityPrimary, Signal: "done", Metadata: map[string]string{"completed_at": "2026-01-02T10:00:00Z"}}}}}, nil, []protocol.SourceStatus{{Name: "provider", Status: "error", Detail: "private-auth-error", SourceRefCount: 80}, {Name: "unrelated", Status: "ok"}})
	if len(store.Tasks()) != 0 {
		t.Fatal("fixture must be dashboard-hidden")
	}
	providers := integration.NewRegistry(diagnosticIntegrationTrap{"provider"}, diagnosticIntegrationTrap{"unrelated"})
	page, err := Read(store, protocol.DiagnosticsRequest{Section: "integrations", Workspace: path}, false, providers)
	if err != nil || page.Total != 1 {
		t.Fatalf("hidden scoped relation=%+v %v", page, err)
	}
	v := page.Items[0].Integration
	if v.Name != "provider" || v.ActiveRefs != 1 || v.CachedStatus != "error" || v.InstallationReportedRefCount != nil {
		t.Fatalf("scope/global distinction lost: %+v", v)
	}
	page, err = Read(store, protocol.DiagnosticsRequest{Section: "integrations", Workspace: path + "-foreign"}, false, providers)
	if err != nil || page.Total != 0 {
		t.Fatal("lexical sibling leaked into scoped integrations")
	}
	for _, count := range []int{-1, 0} {
		items, err := integrationItems(protocol.DiagnosticsRequest{}, nil, []protocol.SourceStatus{{Name: "provider", Status: "ok", SourceRefCount: count}}, nil, providers)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range items {
			if item.Integration.InstallationReportedRefCount != nil {
				t.Fatal("unknown/unrecorded native count reported none")
			}
		}
	}
}
