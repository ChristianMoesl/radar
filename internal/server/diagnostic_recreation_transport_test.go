package server

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"

	"radar/internal/client"
	"radar/internal/integration"
	"radar/internal/protocol"
	"strings"
	"testing"
	"time"
)

func TestDiagnosticRecreationTransportExactExistingBackend(t *testing.T) {
	for _, scope := range []string{"origin", "feedback", "installation"} {
		for _, kind := range []string{"success", "native_error", "incomplete"} {
			t.Run(scope+"/"+kind, func(t *testing.T) {
				s, p, q, pending := recreationAdmissionFixture(t)
				q.Scope = scope
				if scope == "feedback" {
					q.ParentID = "feedback"
					q.Workspace = q.FeedbackWorkspace
				}
				if scope == "installation" {
					q.Workspace = ""
				}
				preview := refreshRPC(t, s, protocol.Request{Method: "diagnostics-maintenance", Maintenance: &q})
				if !preview.OK || preview.DiagnosticRecreation == nil || !protocol.ValidDiagnosticRecreation(*preview.DiagnosticRecreation, q) || p.prepares != 1 || p.executes != 0 || *pending {
					t.Fatal("wrong exact typed preview", preview)
				}
				v := preview.DiagnosticRecreation
				raw, _ := json.Marshal(preview)
				if strings.Contains(string(raw), "PRIVATE") || v.PlanID == strings.Repeat("a", 64) || v.PlanID == v.Proposal.PreviewID {
					t.Fatal("private native identity escaped")
				}
				if kind != "success" {
					p.executeHook = func(context.Context) (integration.SandboxRecreateResult, error) {
						if kind == "native_error" {
							return integration.SandboxRecreateResult{}, errors.New("PRIVATE-native-error")
						}
						return integration.SandboxRecreateResult{OK: true, Recreated: 1}, nil
					}
				}
				q.Mode, q.PlanID = "apply", v.PlanID
				result := refreshRPC(t, s, protocol.Request{Method: "diagnostics-maintenance", Maintenance: &q})
				if !result.OK || result.DiagnosticRecreation == nil || !protocol.ValidDiagnosticRecreation(*result.DiagnosticRecreation, q) || p.prepares != 1 || p.executes != 1 || !*pending || p.captured.ExpectedPlanID != strings.Repeat("a", 64) {
					t.Fatal("wrong native apply", result)
				}
				actual := result.DiagnosticRecreation
				want := "native_restoration_checks_returned"
				if kind != "success" {
					want = "unverified_may_be_partial"
				}
				if actual.Outcome != want || actual.OperationID == "" {
					t.Fatal("invented verified outcome")
				}
				raw, _ = json.Marshal(result)
				if strings.Contains(string(raw), "PRIVATE") {
					t.Fatal("private return escaped")
				}
				replay := refreshRPC(t, s, protocol.Request{Method: "diagnostics-maintenance", Maintenance: &q})
				if replay.OK || p.executes != 1 {
					t.Fatal("wire replay dispatched")
				}
			})
		}
	}
}
func TestDiagnosticRecreationTransportRejectsBeforeNativeDispatch(t *testing.T) {
	s, p, q, _ := recreationAdmissionFixture(t)
	raw, _ := json.Marshal(protocol.Request{Method: "diagnostics-maintenance", Maintenance: &q})
	valid := string(raw)
	frames := map[string]string{
		"old_schema":        strings.Replace(valid, `"diagnostics_schema":38`, `"diagnostics_schema":34`, 1),
		"new_schema":        strings.Replace(valid, `"diagnostics_schema":38`, `"diagnostics_schema":39`, 1),
		"unknown_envelope":  strings.Replace(valid, `"method":`, `"private":null,"method":`, 1),
		"folded":            strings.Replace(valid, `"parent_id":`, `"PARENT_ID":`, 1),
		"duplicate":         strings.Replace(valid, `"operation":"recreate"`, `"operation":"recreate","operation":"recreate"`, 1),
		"escaped_duplicate": strings.Replace(valid, `"operation":"recreate"`, `"oper\u0061tion":"recreate","operation":"recreate"`, 1),
	}
	for _, field := range []string{"diagnostics_schema", "operation", "mode", "scope", "parent_id", "feedback_workspace", "workspace"} {
		var frame map[string]any
		_ = json.Unmarshal(raw, &frame)
		frame["maintenance"].(map[string]any)[field] = nil
		bad, _ := json.Marshal(frame)
		frames["null_"+field] = string(bad)
	}
	for _, field := range []string{`"command":"PRIVATE"`, `"workspace_id":"origin"`, `"id":"origin"`, `"cursor":""`, `"limit":1`, `"plan_id":""`, `"recipe":null`, `"native_plan_id":"PRIVATE"`} {
		frames[field] = strings.Replace(valid, `"operation":"recreate"`, `"operation":"recreate",`+field, 1)
	}
	for name, frame := range frames {
		t.Run(name, func(t *testing.T) {
			host, client := net.Pipe()
			done := make(chan struct{})
			go func() { defer close(done); s.handle(host) }()
			if _, err := client.Write([]byte(frame + "\n")); err != nil {
				t.Fatal(err)
			}
			var v protocol.Response
			if err := json.NewDecoder(client).Decode(&v); err != nil {
				t.Fatal(err)
			}
			client.Close()
			<-done
			if v.OK || p.prepares != 0 || p.executes != 0 || p.registrations != 0 || len(s.recreationPlans) != 0 || strings.Contains(v.Error, "PRIVATE") {
				t.Fatal("invalid frame reached backend or leaked", v)
			}
		})
	}
}
func TestDiagnosticRecreationTransportExpirySupersessionAndLostApply(t *testing.T) {
	for _, kind := range []string{"expired", "superseded", "wrong_feedback"} {
		t.Run(kind, func(t *testing.T) {
			s, p, q, _ := recreationAdmissionFixture(t)
			v := recreationAdmissionPreview(t, s, q)
			switch kind {
			case "expired":
				retained := s.recreationPlans[q.ParentID]
				retained.expires = time.Now().Add(-time.Second)
				s.recreationPlans[q.ParentID] = retained
			case "superseded":
				recreationAdmissionPreview(t, s, q)
			case "wrong_feedback":
				q.FeedbackWorkspace = q.Workspace
			}
			q = recreationAdmissionApply(q, v)
			res := refreshRPC(t, s, protocol.Request{Method: "diagnostics-maintenance", Maintenance: &q})
			if res.OK || p.executes != 0 {
				t.Fatal("stale or misbound wire plan executed")
			}
			res = refreshRPC(t, s, protocol.Request{Method: "diagnostics-maintenance", Maintenance: &q})
			if res.OK || p.executes != 0 {
				t.Fatal("rejected plan replayed")
			}
		})
	}
}

func TestDiagnosticRecreationTransportNativeClientRoundTrip(t *testing.T) {
	s, p, req, _ := recreationAdmissionFixture(t)
	dir, err := os.MkdirTemp("", "rrc-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "s")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() {
		for i := 0; i < 3; i++ {
			conn, err := listener.Accept()
			if err != nil {
				done <- err
				return
			}
			s.handle(conn)
		}
		done <- nil
	}()
	response, err := client.MaintainDiagnostics(path, req, 2*time.Second)
	if err != nil || !response.OK || response.DiagnosticRecreation == nil {
		t.Fatal("actual typed preview failed", response, err)
	}
	req.Mode, req.PlanID = "apply", response.DiagnosticRecreation.PlanID
	response, err = client.MaintainDiagnostics(path, req, 2*time.Second)
	if err != nil || !response.OK || response.DiagnosticRecreation == nil || response.DiagnosticRecreation.Outcome != "native_restoration_checks_returned" {
		t.Fatal("actual typed apply failed", response, err)
	}
	replay, err := client.MaintainDiagnostics(path, req, 2*time.Second)
	if err != nil || replay.OK || replay.DiagnosticRecreation != nil {
		t.Fatal("actual one-use client replay accepted", replay, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if p.prepares != 1 || p.executes != 1 {
		t.Fatal("client duplicated native plan/execution")
	}
}
func TestDiagnosticRecreationTransportLostApplyReplyCannotReplay(t *testing.T) {
	s, p, q, _ := recreationAdmissionFixture(t)
	v := recreationAdmissionPreview(t, s, q)
	q = recreationAdmissionApply(q, v)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	p.executeHook = func(context.Context) (integration.SandboxRecreateResult, error) {
		close(entered)
		<-release
		return integration.SandboxRecreateResult{}, errors.New("PRIVATE-partial-native-error")
	}
	host, c := net.Pipe()
	go func() { defer close(done); s.handle(host) }()
	if err := json.NewEncoder(c).Encode(protocol.Request{Method: "diagnostics-maintenance", Maintenance: &q}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("native dispatch did not enter")
	}
	c.Close()
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("lost-reply handler did not finish")
	}
	if p.executes != 1 || len(s.recreationPlans) != 0 {
		t.Fatal("unknown apply retained identity")
	}
	replay := refreshRPC(t, s, protocol.Request{Method: "diagnostics-maintenance", Maintenance: &q})
	if replay.OK || p.executes != 1 {
		t.Fatal("lost-reply apply replayed")
	}
}
