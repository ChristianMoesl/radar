package server

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"radar/internal/client"
	"radar/internal/protocol"
)

func stopRawRPC(t *testing.T, s *Server, raw string) protocol.Response {
	t.Helper()
	host, peer := net.Pipe()
	done := make(chan struct{})
	go func() { defer close(done); s.handle(host) }()
	if _, err := peer.Write([]byte(raw + "\n")); err != nil {
		t.Fatal(err)
	}
	var response protocol.Response
	if err := json.NewDecoder(peer).Decode(&response); err != nil {
		t.Fatal(err)
	}
	peer.Close()
	<-done
	return response
}
func TestDiagnosticAgentStopTransportRejectsAmbiguousPrivateAndCrossOperationFields(t *testing.T) {
	s, _, provider, q, file, pending := stopAdapterFixture(t, "origin")
	raw, _ := json.Marshal(protocol.Request{Method: "diagnostics-maintenance", Maintenance: &q})
	valid := string(raw)
	frames := map[string]string{
		"unknown_envelope":  strings.Replace(valid, `"method":`, `"private":null,"method":`, 1),
		"folded":            strings.Replace(valid, `"agent_id":`, `"AGENT_ID":`, 1),
		"duplicate":         strings.Replace(valid, `"operation":"agent_stop"`, `"operation":"agent_stop","operation":"agent_stop"`, 1),
		"escaped_duplicate": strings.Replace(valid, `"agent_id":"worker"`, `"agent\u005fid":"other","agent_id":"worker"`, 1),
		"method_duplicate":  strings.Replace(valid, `"method":"diagnostics-maintenance"`, `"method":"agent","method":"diagnostics-maintenance"`, 1),
	}
	for _, field := range []string{"diagnostics_schema", "operation", "mode", "scope", "parent_id", "agent_id", "feedback_workspace", "workspace"} {
		for _, kind := range []string{"missing", "null"} {
			var copy map[string]any
			_ = json.Unmarshal(raw, &copy)
			fields := copy["maintenance"].(map[string]any)
			if kind == "missing" {
				delete(fields, field)
			} else {
				fields[field] = nil
			}
			bad, _ := json.Marshal(copy)
			frames[kind+"_"+field] = string(bad)
		}
	}
	for _, field := range []string{`"command":"PRIVATE"`, `"id":"worker"`, `"workspace_id":"origin"`, `"plan_id":""`, `"pid":123`, `"native_plan":null`, `"guard":true`, `"consent":true`, `"limit":1`, `"cursor":""`, `"agent":{"operation":"stop"}`} {
		frames[field] = strings.Replace(valid, `"operation":"agent_stop"`, `"operation":"agent_stop",`+field, 1)
	}
	for _, delta := range []int{-1, 1} {
		copy := q
		copy.Schema += delta
		bad, _ := json.Marshal(protocol.Request{Method: "diagnostics-maintenance", Maintenance: &copy})
		frames["schema_"+string(rune('b'+delta))] = string(bad)
	}
	for name, frame := range frames {
		t.Run(name, func(t *testing.T) {
			v := stopRawRPC(t, s, frame)
			if v.OK || provider.prepares != 0 || !stopDesired(t, file) || *pending || len(s.cleanupPlans) != 0 || strings.Contains(v.Error, "PRIVATE") {
				t.Fatal("unsafe transport reached native capture/effects", v)
			}
		})
	}
	for _, operation := range []string{"refresh", "cleanup", "cleanup_check", "recreate", "retire", "expire"} {
		for _, value := range []string{`"worker"`, `""`, `null`} {
			frame := strings.Replace(valid, `"operation":"agent_stop"`, `"operation":"`+operation+`"`, 1)
			frame = strings.Replace(frame, `"agent_id":"worker"`, `"agent_id":`+value, 1)
			if v := stopRawRPC(t, s, frame); v.OK || provider.prepares != 0 {
				t.Fatal("agent selector leaked to another operation", operation, value)
			}
		}
	}
}
func TestDiagnosticAgentStopTransportNativeClientExactPlanManagedEvidence(t *testing.T) {
	for _, scope := range []string{"origin", "feedback", "installation"} {
		t.Run(scope, func(t *testing.T) {
			target := scope
			if scope == "installation" {
				target = "other"
			}
			s, _, provider, q, file, pending := stopAdapterFixture(t, target)
			q.Scope = scope
			if scope == "feedback" {
				q.Workspace = q.FeedbackWorkspace
			}
			if scope == "installation" {
				q.Workspace = ""
			}
			dir, err := os.MkdirTemp("", "asr-")
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
					c, err := listener.Accept()
					if err != nil {
						done <- err
						return
					}
					s.handle(c)
				}
				done <- nil
			}()
			before := s.store.Revision()
			response, err := client.MaintainDiagnostics(path, q, 2*time.Second)
			v := response.DiagnosticAgentStop
			if err != nil || !response.OK || v == nil || !protocol.ValidDiagnosticAgentStop(*v, q) || s.store.Revision() != before || *pending || !stopDesired(t, file) {
				t.Fatal("finite native preview failed", response, err)
			}
			raw, _ := json.Marshal(response)
			for _, secret := range []string{"PRIVATE", q.Workspace, q.FeedbackWorkspace} {
				if secret != "" && strings.Contains(string(raw), secret) {
					t.Fatal("private native data escaped")
				}
			}
			q.Mode, q.PlanID = "apply", v.PlanID
			response, err = client.MaintainDiagnostics(path, q, 2*time.Second)
			v = response.DiagnosticAgentStop
			if err != nil || !response.OK || v == nil || v.OperationID == "" || !protocol.ValidDiagnosticAgentStop(*v, q) || v.Outcome != "native_handler_returned_unverified" || !v.DesiredBefore || v.RunningBefore || !v.ResourceRefreshPending || !*pending || stopDesired(t, file) {
				t.Fatal("finite native apply failed/overclaimed", response, err)
			}
			_, _, ops, _ := s.store.OperationInspectionSnapshot()
			if len(ops) != 1 || ops[0].ID != v.OperationID || ops[0].Kind != "agent-stop" || ops[0].Status != "succeeded" || ops[0].WorkspaceID != target {
				t.Fatal("missing/wrong managed dispatch evidence", ops)
			}
			replay, err := client.MaintainDiagnostics(path, q, 2*time.Second)
			if err != nil || replay.OK || replay.DiagnosticAgentStop != nil || provider.prepares != 1 {
				t.Fatal("lost-reply one-use plan replayed", replay, err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}
