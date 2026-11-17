package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"radar/internal/cleanup"
	"radar/internal/collector"
	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/state"
	"radar/internal/taskservice"
)

type refreshFixtureSource struct {
	name       string
	calls      atomic.Int32
	incomplete bool
}

func (s *refreshFixtureSource) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: s.name}
}
func (s *refreshFixtureSource) Collect(context.Context, integration.CollectRequest) integration.CollectResult {
	s.calls.Add(1)
	status := "ok"
	if s.incomplete {
		status = "error"
	}
	return integration.CollectResult{Complete: !s.incomplete, SourceStatus: &protocol.SourceStatus{Name: s.name, Status: status, Detail: "private-provider-token-and-payload"}}
}

func refreshFixture(t *testing.T, sources ...integration.Integration) (*Server, *state.Store) {
	t.Helper()
	t.Setenv("RADAR_STATE", filepath.Join(t.TempDir(), "tasks.db"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := state.NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}
	registry := integration.NewRegistry(sources...)
	tasks := taskservice.New(store, logger, registry)
	s := New(store, logger, func() { t.Error("ordinary refresh/GC callback invoked") }, nil, func() (protocol.GarbageCollectionResult, error) {
		t.Error("GC invoked")
		return protocol.GarbageCollectionResult{}, nil
	}, registry, cleanup.New(nil))
	s.SetDiagnosticRefresh(tasks.RefreshDiagnostics).SetResourcePublication(tasks.MarkResourceRefreshPending, tasks.ResourceSnapshot)
	return s, store
}
func refreshRequest(mode string, plan string) protocol.DiagnosticMaintenanceRequest {
	return protocol.DiagnosticMaintenanceRequest{Schema: protocol.DiagnosticsVersion, Operation: "refresh", Mode: mode, Scope: "installation", PlanID: plan}
}
func mustRefreshPreview(t *testing.T, s *Server) protocol.DiagnosticMaintenance {
	t.Helper()
	view, err := s.maintainDiagnostics(context.Background(), refreshRequest("preview", ""))
	if err != nil {
		t.Fatal(err)
	}
	return view
}
func refreshRPC(t *testing.T, s *Server, request protocol.Request) protocol.Response {
	t.Helper()
	a, b := net.Pipe()
	done := make(chan struct{})
	go func() { defer close(done); s.handle(a) }()
	if err := json.NewEncoder(b).Encode(request); err != nil {
		t.Fatal(err)
	}
	var response protocol.Response
	if err := json.NewDecoder(b).Decode(&response); err != nil {
		t.Fatal(err)
	}
	b.Close()
	<-done
	return response
}

func TestDiagnosticMaintenancePreviewAndActualNativeCollectionRPC(t *testing.T) {
	one, two := &refreshFixtureSource{name: "one"}, &refreshFixtureSource{name: "two", incomplete: true}
	s, store := refreshFixture(t, one, two)
	before := store.Revision()
	response := refreshRPC(t, s, protocol.Request{Method: "diagnostics-maintenance", Maintenance: ptrRefresh(refreshRequest("preview", ""))})
	if !response.OK || response.Maintenance == nil {
		t.Fatalf("preview=%+v", response)
	}
	preview := response.Maintenance
	if preview.Schema != protocol.DiagnosticsVersion || preview.Status != "preview" || preview.Counts.Unverified != 2 || one.calls.Load() != 0 || two.calls.Load() != 0 || before != store.Revision() || !preview.ResourceRefreshPending {
		t.Fatalf("preview side effect or false evidence: %+v", preview)
	}
	if !strings.HasPrefix(preview.Targets[0].ID, "integration:") || preview.Targets[0].ID >= preview.Targets[1].ID {
		t.Fatal("identities not sorted")
	}
	response = refreshRPC(t, s, protocol.Request{Method: "diagnostics-maintenance", Maintenance: ptrRefresh(refreshRequest("apply", preview.PlanID))})
	view := response.Maintenance
	if !response.OK || view == nil || view.Status != "returned" || view.Problem != "incomplete_or_unverified" || view.Counts.Complete != 1 || view.Counts.Incomplete != 1 || one.calls.Load() != 1 || two.calls.Load() != 1 || view.OperationID == "" {
		t.Fatalf("native partial=%+v response=%+v", view, response)
	}
	encoded, _ := json.Marshal(response)
	for _, private := range []string{"private-provider-token-and-payload", `"task_title":`, `"tasks":`, `"sources":`, `"detail":`, `"path":`} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("private projection %s: %s", private, encoded)
		}
	}
	_, _, ops, _ := store.OperationInspectionSnapshot()
	if len(ops) != 1 || ops[0].Kind != "refresh" || ops[0].Status != "partial" || ops[0].StartedAt == "" || ops[0].FinishedAt == "" {
		t.Fatalf("operation=%+v", ops)
	}
	again := refreshRPC(t, s, protocol.Request{Method: "diagnostics-maintenance", Maintenance: ptrRefresh(refreshRequest("apply", preview.PlanID))})
	if !again.OK || again.Maintenance.OperationID != view.OperationID || one.calls.Load() != 1 || two.calls.Load() != 1 {
		t.Fatal("duplicate apply reexecuted")
	}
}
func ptrRefresh(v protocol.DiagnosticMaintenanceRequest) *protocol.DiagnosticMaintenanceRequest {
	return &v
}

func TestDiagnosticMaintenanceFreshnessAndAdmissionFailClosed(t *testing.T) {
	for _, mode := range []string{"revision", "expired", "foreign", "replacement", "targets", "unsupported", "busy", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			source := &refreshFixtureSource{name: "one"}
			s, store := refreshFixture(t, source)
			preview := mustRefreshPreview(t, s)
			id := preview.PlanID
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "revision":
				store.NotifyChange()
			case "expired":
				s.refreshPlan.expires = time.Now().Add(-time.Second)
			case "foreign":
				id = strings.Repeat("a", 64)
			case "replacement":
				mustRefreshPreview(t, s)
			case "targets":
				s.integrations = integration.NewRegistry(&refreshFixtureSource{name: "other"})
			case "unsupported":
				s.diagnosticRefresh = nil
			case "busy":
				s.maintenanceMu.Lock()
				defer s.maintenanceMu.Unlock()
			case "cancelled":
				cancel()
			}
			_, err := s.maintainDiagnostics(ctx, refreshRequest("apply", id))
			if err == nil || source.calls.Load() != 0 {
				t.Fatalf("unsafe %s admission: %v", mode, err)
			}
			_, _, ops, _ := store.OperationInspectionSnapshot()
			if len(ops) != 0 {
				t.Fatal("rejected admission created operation")
			}
		})
	}
}

func TestDiagnosticMaintenanceOneUseConcurrentAndInFlightResults(t *testing.T) {
	s, store := refreshFixture(t, &refreshFixtureSource{name: "one"})
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	s.SetDiagnosticRefresh(func(ctx context.Context) (collector.Result, error) {
		calls.Add(1)
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return collector.Result{}, nil
	})
	preview := mustRefreshPreview(t, s)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := s.maintainDiagnostics(context.Background(), refreshRequest("apply", preview.PlanID))
		if err != nil {
			t.Error(err)
		}
	}()
	<-started
	inflight, err := s.maintainDiagnostics(context.Background(), refreshRequest("apply", preview.PlanID))
	if err != nil || inflight.Status != "running" || inflight.Counts.Unverified != 1 || inflight.OperationID == "" {
		t.Fatalf("inflight=%+v %v", inflight, err)
	}
	if _, err := s.maintainDiagnostics(context.Background(), refreshRequest("preview", "")); err == nil {
		t.Fatal("second preview admitted while running")
	}
	inflight.Targets[0].ID = "private"
	inflight.Limitations[0] = "private"
	close(release)
	wg.Wait()
	final, err := s.maintainDiagnostics(context.Background(), refreshRequest("apply", preview.PlanID))
	if err != nil || final.Status != "returned" || final.Problem != "incomplete_or_unverified" || final.Targets[0].ID == "private" || final.Limitations[0] == "private" || calls.Load() != 1 {
		t.Fatalf("one-use result=%+v %v", final, err)
	}
	_, _, ops, _ := store.OperationInspectionSnapshot()
	if len(ops) != 1 {
		t.Fatal("duplicate operations")
	}
}

func TestDiagnosticMaintenanceCancelledPartialAndUnknownOutcomes(t *testing.T) {
	for _, mode := range []string{"cancelled", "error", "unknown", "complete", "skipped"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := refreshFixture(t, &refreshFixtureSource{name: "one"})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s.SetDiagnosticRefresh(func(context.Context) (collector.Result, error) {
				r := collector.Result{Attempts: map[string]protocol.DiagnosticCollectionAttempt{"one": {Outcome: "private-error"}}}
				switch mode {
				case "cancelled":
					cancel()
				case "error":
					return r, errors.New("private-token")
				case "complete":
					r.Attempts["one"] = protocol.DiagnosticCollectionAttempt{Outcome: "complete"}
				case "skipped":
					r.Attempts["one"] = protocol.DiagnosticCollectionAttempt{Outcome: "skipped_before_collection"}
				}
				return r, nil
			})
			view, err := s.maintainDiagnostics(ctx, refreshRequest("apply", mustRefreshPreview(t, s).PlanID))
			if err != nil || view.Status != "returned" {
				t.Fatalf("view=%+v %v", view, err)
			}
			want := "incomplete_or_unverified"
			switch mode {
			case "cancelled":
				want = "cancelled_or_timed_out"
			case "error":
				want = "native_error"
			case "complete", "skipped":
				want = "none"
			}
			if view.Problem != want {
				t.Fatalf("problem=%s want=%s", view.Problem, want)
			}
			raw, _ := json.Marshal(view)
			if strings.Contains(string(raw), "private") {
				t.Fatal("raw error leaked")
			}
		})
	}
}

func TestDiagnosticMaintenanceRejectsSelectorAndIdentityInjection(t *testing.T) {
	s, _ := refreshFixture(t, &refreshFixtureSource{name: "one"})
	for _, req := range []protocol.DiagnosticMaintenanceRequest{{}, {Operation: "cleanup", Scope: "installation", Mode: "apply"}, refreshRequest("preview", strings.Repeat("a", 64)), refreshRequest("apply", ""), {Operation: "refresh", Mode: "preview", Scope: "origin"}, {Operation: "refresh", Mode: "preview", Scope: "feedback"}, refreshRequest("apply", strings.Repeat("A", 64))} {
		if _, err := s.maintainDiagnostics(context.Background(), req); err == nil {
			t.Fatalf("accepted %+v", req)
		}
	}
	for _, req := range []protocol.Request{
		{Method: "diagnostics", Maintenance: ptrRefresh(refreshRequest("preview", ""))},
		{Method: "diagnostics-probe", Maintenance: ptrRefresh(refreshRequest("preview", ""))},
		{Method: "diagnostics-maintenance", Maintenance: ptrRefresh(refreshRequest("preview", "")), Diagnostics: &protocol.DiagnosticsRequest{Section: "overview"}},
		{Method: "diagnostics-maintenance", Maintenance: ptrRefresh(refreshRequest("preview", "")), TaskID: 7},
	} {
		if response := refreshRPC(t, s, req); response.OK {
			t.Fatalf("accepted cross-boundary request %+v", req)
		}
	}
	for _, sources := range [][]integration.Integration{nil, {&refreshFixtureSource{name: ""}}, {&refreshFixtureSource{name: "one"}, &refreshFixtureSource{name: "one"}}} {
		s.integrations = integration.NewRegistry(sources...)
		if _, err := s.maintainDiagnostics(context.Background(), refreshRequest("preview", "")); err == nil {
			t.Fatal("ambiguous/absent native targets accepted")
		}
	}
}

func TestDiagnosticMaintenanceRejectsUnknownWireFieldsAndSchemaBeforeAnyAction(t *testing.T) {
	source := &refreshFixtureSource{name: "one"}
	s, store := refreshFixture(t, source)
	for _, mode := range []string{"schema", "missing-schema", "nested-unknown", "top-unknown", "zero-read-selector", "empty-read-selector"} {
		t.Run(mode, func(t *testing.T) {
			request := map[string]any{"method": "diagnostics-maintenance", "maintenance": map[string]any{"diagnostics_schema": protocol.DiagnosticsVersion, "operation": "refresh", "mode": "preview", "scope": "installation"}}
			native := request["maintenance"].(map[string]any)
			switch mode {
			case "schema":
				native["diagnostics_schema"] = protocol.DiagnosticsVersion - 1
			case "missing-schema":
				delete(native, "diagnostics_schema")
			case "nested-unknown":
				native["command"] = "private"
			case "top-unknown":
				request["command"] = "private"
			case "zero-read-selector":
				request["task_id"] = 0
			case "empty-read-selector":
				request["diagnostics"] = nil
			}
			a, b := net.Pipe()
			done := make(chan struct{})
			go func() { defer close(done); s.handle(a) }()
			if err := json.NewEncoder(b).Encode(request); err != nil {
				t.Fatal(err)
			}
			var response protocol.Response
			if err := json.NewDecoder(b).Decode(&response); err != nil {
				t.Fatal(err)
			}
			b.Close()
			<-done
			if response.OK || s.refreshPlan != nil || source.calls.Load() != 0 || store.Revision() != 0 {
				t.Fatalf("wire injection admitted: %+v", response)
			}
		})
	}
}
