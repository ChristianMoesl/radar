package diagnostics

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"radar/internal/integration"
	"radar/internal/integration/workspace"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

type runtimeProbeFixture struct {
	integration.Integration
	source string
	calls  *[]string
	view   protocol.DiagnosticRuntimeObservation
	run    func(context.Context)
}

func (p runtimeProbeFixture) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: p.source}
}
func (p runtimeProbeFixture) DiagnosticRuntime(ctx context.Context, name string) protocol.DiagnosticRuntimeObservation {
	*p.calls = append(*p.calls, p.source+":"+name)
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > RuntimeProbeTimeout {
		panic("probe has no bounded deadline")
	}
	if p.run != nil {
		p.run(ctx)
	}
	return p.view
}

type probeRegistrationFixture struct {
	workspace.Source
	registrations []protocol.DiagnosticWorkspace
	err           error
}

func (p *probeRegistrationFixture) DiagnosticWorkspaceRegistrations() ([]protocol.DiagnosticWorkspace, error) {
	return p.registrations, p.err
}

func TestRuntimeProbeExactScopeIdentityPrivacyPaginationAndNoMutation(t *testing.T) {
	store, root, path := fixture(t)
	name := strings.Repeat("n", 600) + "selected"
	group := workspacegroup.Workspace{ID: workspacegroup.ID(path), Name: "registered", Path: path, SessionName: "registered-session", Sandbox: &workspacegroup.Sandbox{Name: name, Agent: "shell"}}
	if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
		t.Fatal(err)
	}
	input := task(path, "test:ABC-123")
	input.SourceRefs = append(input.SourceRefs, protocol.SourceRef{ID: "sbx:sandbox:cached", Source: "sbx", Path: path, Metadata: map[string]string{"name": name, "id": "cached-lifetime", "token": "private-token"}}, protocol.SourceRef{ID: "tmux:session:1:2:$3", Source: "tmux", Path: path, Title: group.SessionName})
	store.SetTasks([]protocol.Task{input})
	beforeRecords, beforeRefs, beforeRevision := store.InspectionSnapshot()
	beforeRegistry, _ := os.ReadFile(workspacegroup.Path(root))
	calls := []string{}
	sbx := runtimeProbeFixture{source: "sbx", calls: &calls, view: protocol.DiagnosticRuntimeObservation{Presence: "present", State: "private-state", ID: "different-lifetime"}}
	tmux := runtimeProbeFixture{source: "tmux", calls: &calls, view: protocol.DiagnosticRuntimeObservation{Presence: "present", State: "running", ID: "tmux:session:1:2:$3"}}
	providers := integration.NewRegistry(workspace.Source{}, sbx, tmux)
	req := protocol.DiagnosticsRequest{Section: "runtime_probe", Workspace: path, ParentID: group.ID, Limit: 1}
	page, err := Probe(context.Background(), store, req, true, providers)
	if err != nil || page.Total != 2 || len(page.Items) != 1 || page.NextCursor == "" || !page.ResourceRefreshPending || page.Schema != protocol.DiagnosticsVersion {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	first := page
	req.Cursor = page.NextCursor
	page, err = Probe(context.Background(), store, req, true, providers)
	if err != nil || !page.Complete || len(page.Items) != 1 || first.Items[0].ID == page.Items[0].ID {
		t.Fatalf("second=%+v %v", page, err)
	}
	for _, item := range append(first.Items, page.Items...) {
		v := item.Runtime
		if v == nil || v.ID != item.ID || v.OwnershipEvidence != "unknown_not_checked" || v.LeaseEvidence != "unknown_not_checked" || v.CachedIdentityCount != 1 || !strings.HasPrefix(v.Observation.ID, "runtime-id:") {
			t.Fatalf("bad evidence=%+v", v)
		}
		if v.Provider == "sbx" && (v.IdentityComparison != "differs_from_cached_identity" || len(v.Name) > 520 || v.Observation.State != "unknown") {
			t.Fatalf("sbx=%+v", v)
		}
		if v.Provider == "tmux" && v.IdentityComparison != "matches_cached_identity" {
			t.Fatalf("tmux=%+v", v)
		}
	}
	raw, _ := json.Marshal(first)
	for _, private := range []string{"private-token", "different-lifetime", "private-state", "tmux:session:1:2:$3"} {
		if strings.Contains(string(raw), private) {
			t.Fatalf("raw identity/payload escaped: %s", raw)
		}
	}
	if len(calls) != 4 || calls[0] != "sbx:"+name {
		t.Fatalf("full native identity not used: %v", calls)
	}
	calls = nil
	exact, err := Probe(context.Background(), store, protocol.DiagnosticsRequest{Section: "runtime_probe", ParentID: group.ID, ID: first.Items[0].ID}, false, providers)
	if err != nil || exact.Total != 1 || len(calls) != 1 {
		t.Fatalf("exact probe=%+v %v calls=%v", exact, err, calls)
	}
	for _, invalid := range []protocol.DiagnosticsRequest{{Section: "runtime_probe"}, {Section: "runtime_probe", ParentID: group.ID, Workspace: path + "-other"}, {Section: "runtime_probe", ParentID: "unknown"}, {Section: "runtime_probe", ParentID: group.ID, State: "done"}, {Section: "runtime_probe", ParentID: group.ID, Limit: 51}, {Section: "tasks", ParentID: group.ID}} {
		calls = nil
		if _, err := Probe(context.Background(), store, invalid, false, providers); err == nil || len(calls) != 0 {
			t.Fatalf("invalid selector probed: %+v %v %v", invalid, err, calls)
		}
	}
	calls = nil
	if _, err := Read(store, protocol.DiagnosticsRequest{Section: "runtime_probe", ParentID: group.ID}, false, providers); err == nil || len(calls) != 0 {
		t.Fatal("cached RPC can implicitly probe")
	}
	for _, section := range []string{"tasks", "workspaces"} {
		if _, err := Read(store, protocol.DiagnosticsRequest{Section: section}, false, providers); err != nil {
			t.Fatal(err)
		}
	}
	if len(calls) != 0 {
		t.Fatal("ordinary inventory probed runtime")
	}
	sbx.view.ID = "changed-again"
	if _, err := Probe(context.Background(), store, req, true, integration.NewRegistry(workspace.Source{}, sbx, tmux)); err == nil {
		t.Fatal("changed runtime reused cursor")
	}
	afterRecords, afterRefs, afterRevision := store.InspectionSnapshot()
	afterRegistry, _ := os.ReadFile(workspacegroup.Path(root))
	if beforeRevision != afterRevision || !reflect.DeepEqual(beforeRecords, afterRecords) || !reflect.DeepEqual(beforeRefs, afterRefs) || string(beforeRegistry) != string(afterRegistry) {
		t.Fatal("probe mutated retained state or registry")
	}
}

func TestRuntimeProbeUnknownCancellationAmbiguityAndRegistrationDrift(t *testing.T) {
	store, _, path := fixture(t)
	group := protocol.DiagnosticWorkspace{ID: "registered", Path: path, SandboxName: "sbx", SessionName: "tmux"}
	reg := &probeRegistrationFixture{registrations: []protocol.DiagnosticWorkspace{group}}
	calls := []string{}
	sbx := runtimeProbeFixture{source: "sbx", calls: &calls, view: protocol.DiagnosticRuntimeObservation{Presence: "absent", State: "private-state", ID: "private-id"}}
	req := protocol.DiagnosticsRequest{Section: "runtime_probe", ParentID: group.ID}
	page, err := Probe(context.Background(), store, req, false, integration.NewRegistry(reg, sbx))
	if err != nil || page.Total != 2 {
		t.Fatalf("page=%+v %v", page, err)
	}
	for _, item := range page.Items {
		v := item.Runtime
		if v.Provider == "tmux" && (v.Observation.Presence != "unknown" || v.Observation.Problem != "unsupported") {
			t.Fatalf("unsupported reported absent: %+v", v)
		}
		if v.Provider == "sbx" && (v.Observation.Presence != "absent" || v.Observation.ID != "" || v.Observation.State != "unknown") {
			t.Fatalf("absence leaked evidence: %+v", v)
		}
	}
	for _, view := range []protocol.DiagnosticRuntimeObservation{{Presence: "present"}, {Presence: "invented", ID: "raw"}, {Presence: "present", ID: "raw", Problem: "private-error"}} {
		sbx.view = view
		page, err = Probe(context.Background(), store, req, false, integration.NewRegistry(reg, sbx))
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			if item.Runtime.Provider == "sbx" && (item.Runtime.Observation.Presence != "unknown" || strings.Contains(item.Runtime.Observation.Problem, "private")) {
				t.Fatalf("invalid evidence escaped: %+v", item)
			}
		}
	}
	calls = nil
	other := group
	other.ID = "other"
	other.Path = path + "-other"
	reg.registrations = append(reg.registrations, other)
	page, err = Probe(context.Background(), store, req, false, integration.NewRegistry(reg, sbx))
	if err != nil || len(calls) != 0 {
		t.Fatalf("shared name probed=%v %v", calls, err)
	}
	for _, item := range page.Items {
		if item.Runtime.Observation.Problem != "shared_registered_name" {
			t.Fatal("ambiguous registration not explicit")
		}
	}
	reg.registrations = reg.registrations[:1]
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Probe(ctx, store, req, false, integration.NewRegistry(reg, sbx)); err == nil || len(calls) != 0 {
		t.Fatal("cancelled request probed")
	}
	sbx.run = func(context.Context) { reg.registrations[0].SandboxName = "replaced" }
	if _, err := Probe(context.Background(), store, req, false, integration.NewRegistry(reg, sbx)); err == nil {
		t.Fatal("registration drift ignored")
	}
	reg.err = errors.New("private-registry-error")
	if _, err := Probe(context.Background(), store, req, false, integration.NewRegistry(reg, sbx)); err == nil || strings.Contains(err.Error(), "private") {
		t.Fatal("raw registration failure escaped")
	}
}

func TestRuntimeProbeCancelsRemainingProvidersAndRejectsCacheDrift(t *testing.T) {
	store, _, path := fixture(t)
	reg := &probeRegistrationFixture{registrations: []protocol.DiagnosticWorkspace{{ID: "registered", Path: path, SandboxName: "sbx", SessionName: "tmux"}}}
	req := protocol.DiagnosticsRequest{Section: "runtime_probe", ParentID: "registered"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := []string{}
	sbx := runtimeProbeFixture{source: "sbx", calls: &calls, view: protocol.DiagnosticRuntimeObservation{Presence: "present", ID: "stale-lifetime"}, run: func(context.Context) { cancel() }}
	tmux := runtimeProbeFixture{source: "tmux", calls: &calls}
	page, err := Probe(ctx, store, req, false, integration.NewRegistry(reg, sbx, tmux))
	if err != nil || len(calls) != 1 {
		t.Fatalf("cancel=%+v %v %v", page, err, calls)
	}
	for _, item := range page.Items {
		if item.Runtime.Observation.Presence != "unknown" || item.Runtime.Observation.Problem != "cancelled_or_timed_out" {
			t.Fatal("cancelled evidence reported fresh")
		}
	}
	sbx.run = func(context.Context) { store.SetTasks([]protocol.Task{task(path, "test:ABC-124")}) }
	if _, err := Probe(context.Background(), store, req, false, integration.NewRegistry(reg, sbx, tmux)); err == nil {
		t.Fatal("cache drift ignored")
	}
}

func TestRuntimeProbeAmbiguousCacheUnsupportedProviderAndFullIdentity(t *testing.T) {
	store, _, path := fixture(t)
	name := strings.Repeat("same-prefix", 60)
	reg := &probeRegistrationFixture{registrations: []protocol.DiagnosticWorkspace{{ID: "registered", Path: path, SandboxName: name + "first"}}}
	input := task(path, "test:ABC-125")
	for _, id := range []string{"first-lifetime", "second-lifetime"} {
		input.SourceRefs = append(input.SourceRefs, protocol.SourceRef{ID: "sbx:sandbox:" + id, Source: "sbx", Path: path, Metadata: map[string]string{"name": name + "first", "id": id}})
	}
	store.SetTasks([]protocol.Task{input})
	calls := []string{}
	sbx := runtimeProbeFixture{source: "sbx", calls: &calls, view: protocol.DiagnosticRuntimeObservation{Presence: "present", ID: "first-lifetime", State: "running"}}
	req := protocol.DiagnosticsRequest{Section: "runtime_probe", ParentID: "registered"}
	page, err := Probe(context.Background(), store, req, false, integration.NewRegistry(reg, sbx))
	if err != nil || page.Total != 1 {
		t.Fatalf("ambiguous=%+v %v", page, err)
	}
	v := page.Items[0].Runtime
	if v.CachedIdentityCount != 2 || v.IdentityComparison != "unknown_ambiguous_cached_identity" {
		t.Fatalf("ambiguous cache invented a match: %+v", v)
	}
	firstID, firstName := v.ID, v.Name
	reg.registrations[0].SandboxName = name + "second"
	page, err = Probe(context.Background(), store, req, false, integration.NewRegistry(reg, sbx))
	if err != nil || page.Items[0].ID == firstID || page.Items[0].Runtime.Name != firstName || calls[len(calls)-1] != "sbx:"+name+"second" {
		t.Fatal("clipped native identities collided")
	}
	calls = nil
	page, err = Probe(context.Background(), store, req, false, integration.NewRegistry(reg, sbx, sbx))
	if err != nil || len(calls) != 0 || page.Items[0].Runtime.Observation.Problem != "unsupported" || page.Items[0].Runtime.Observation.Presence != "unknown" {
		t.Fatalf("ambiguous provider did not fail closed: %+v %v", page, err)
	}
}
