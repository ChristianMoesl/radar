package server

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"

	"radar/internal/integration"
	"radar/internal/protocol"
)

type recreationRPCSource struct {
	calls   int
	pending *bool
}

func (*recreationRPCSource) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: "workspace"}
}
func (*recreationRPCSource) DiagnosticWorkspaceRegistrations() ([]protocol.DiagnosticWorkspace, error) {
	panic("no fallback inventory")
}
func (p *recreationRPCSource) DiagnosticRecreationPreview(_ context.Context, id, scope string) (protocol.DiagnosticRecreationPreview, error) {
	p.calls++
	if scope != "/registered" {
		panic("wrong scope")
	}
	if p.pending != nil {
		*p.pending = true
	}
	return protocol.DiagnosticRecreationPreview{ID: id, PreviewID: "recreation-preview:" + strings.Repeat("a", 64), Status: "blocked", PreviousState: "unknown", ExecutionEligibility: "unknown_fresh_execution_guards_not_run"}, nil
}

func TestRecreationPreviewStrictRPCNoImplicitDispatch(t *testing.T) {
	p := &recreationRPCSource{}
	s, _ := refreshFixture(t, p)
	pending := false
	s.SetResourcePublication(nil, func() protocol.Response { return protocol.Response{ResourceRefreshPending: pending} })
	valid := `{"method":"diagnostics-probe","diagnostics":{"section":"recreation_preview","parent_id":"registered","workspace":"/registered","limit":20}}`
	frames := []string{
		strings.Replace(valid, `"diagnostics-probe"`, `"diagnostics"`, 1),
		strings.Replace(valid, `"diagnostics-probe"`, `"diagnostics-maintenance"`, 1),
		strings.Replace(valid, `"parent_id":"registered"`, `"parent_id":""`, 1),
		strings.Replace(valid, `"workspace":"/registered"`, `"workspace":"relative"`, 1),
		strings.Replace(valid, `"limit":20`, `"limit":51`, 1),
		strings.Replace(valid, `"section":"recreation_preview"`, `"section":"recreation_preview","section":"recreation_preview"`, 1),
		strings.Replace(valid, `"section":"recreation_preview"`, `"Section":"recreation_preview"`, 1),
		strings.Replace(valid, `"method":"diagnostics-probe"`, `"method":"diagnostics-probe","task_id":0`, 1),
		strings.Replace(valid, `"method":"diagnostics-probe"`, `"Method":"diagnostics-probe"`, 1),
	}
	for _, field := range []string{`"id":""`, `"state":null`, `"cursor":""`, `"cleanup_mode":null`, `"workspace_id":""`, `"plan_id":"private"`, `"apply":true`, `"command":"private"`, `"raw":null`} {
		frames = append(frames, strings.Replace(valid, `"limit":20`, `"limit":20,`+field, 1))
	}
	for i, frame := range append(frames, valid) {
		host, client := net.Pipe()
		done := make(chan struct{})
		go func() { defer close(done); s.handle(host) }()
		if _, err := client.Write([]byte(frame + "\n")); err != nil {
			t.Fatal(err)
		}
		var result protocol.Response
		if err := json.NewDecoder(client).Decode(&result); err != nil {
			t.Fatal(err)
		}
		client.Close()
		<-done
		if i < len(frames) {
			if result.OK || p.calls != 0 {
				t.Fatalf("invalid frame reached native planner: %s %+v", frame, result)
			}
		} else if !result.OK || p.calls != 1 || result.Diagnostics == nil || result.Diagnostics.Schema != protocol.DiagnosticsVersion || result.Diagnostics.Items[0].RecreationPreview == nil {
			t.Fatalf("exact native route missing: %+v", result)
		}
	}
	// Publication can change during the slow native read; do not return a stale proposal.
	p.pending = &pending
	req := protocol.DiagnosticsRequest{Section: "recreation_preview", ParentID: "registered", Workspace: "/registered"}
	result := refreshRPC(t, s, protocol.Request{Method: "diagnostics-probe", Diagnostics: &req})
	if result.OK || result.Diagnostics != nil || p.calls != 2 {
		t.Fatal("post-native pending publication accepted")
	}
	result = refreshRPC(t, s, protocol.Request{Method: "diagnostics-probe", Diagnostics: &req})
	if result.OK || p.calls != 2 {
		t.Fatal("pending publication reached native planner")
	}
}
