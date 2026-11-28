package client

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"radar/internal/protocol"
)

func agentStopWireFixture() []byte {
	v := protocol.DiagnosticAgentStop{Schema: protocol.DiagnosticsVersion, Operation: "agent_stop", Mode: "preview", Scope: "origin", RecordID: "record", TaskID: 7, AgentID: "worker", FeedbackWorkspaceID: "feedback", ScopeWorkspaceID: "origin", AgentWorkspaceID: "origin", Evidence: "native_exact_agent_stop_plan_not_consent", PlanID: strings.Repeat("a", 64), ExpiresAt: time.Now().Add(30 * time.Second).UTC().Format(time.RFC3339Nano), Revision: 1, DesiredBefore: true, RunningBefore: false, DispatchEvidence: "not_dispatched", Problem: "none", Outcome: "not_attempted", Observation: protocol.UnknownDiagnosticAgentStopObservation(), Limitations: protocol.DiagnosticAgentStopLimitations()}
	raw, _ := json.Marshal(protocol.Response{OK: true, DiagnosticAgentStop: &v})
	return raw
}
func TestDiagnosticAgentStopDecoderStrictCompleteShape(t *testing.T) {
	raw := string(agentStopWireFixture())
	response, err := decodeDiagnosticAgentStop([]byte(raw))
	if err != nil || !response.OK || response.DiagnosticAgentStop == nil {
		t.Fatal("valid disclosure refused", err)
	}
	changes := map[string]string{
		"envelope_private":      strings.TrimSuffix(raw, "}") + `,"private":"PRIVATE"}`,
		"view_private":          strings.Replace(raw, `"diagnostic_agent_stop":{`, `"diagnostic_agent_stop":{"pid":123,`, 1),
		"private_sibling":       strings.TrimSuffix(raw, "}") + `,"agent_result":{"token":"PRIVATE"}}`,
		"folded":                strings.Replace(raw, `"agent_id":`, `"AGENT_ID":`, 1),
		"duplicate":             strings.Replace(raw, `"agent_id":"worker"`, `"agent_id":"other","agent_id":"worker"`, 1),
		"escaped_duplicate":     strings.Replace(raw, `"agent_id":"worker"`, `"agent\u005fid":"other","agent_id":"worker"`, 1),
		"success_claim":         strings.Replace(raw, `"outcome":"not_attempted"`, `"outcome":"process_exited"`, 1),
		"error_payload":         strings.Replace(raw, `"ok":true`, `"ok":false,"error":"PRIVATE"`, 1),
		"null_view":             `{"ok":true,"diagnostic_agent_stop":null}`,
		"error_private_sibling": `{"ok":false,"error":"PRIVATE","tasks":[]}`,
		"missing_ok":            strings.Replace(raw, `,"ok":true`, "", 1),
		"missing_error":         `{"ok":false}`,
		"trailing":              raw + ` {"ok":false}`,
		"deep":                  strings.Repeat("[", 18) + raw + strings.Repeat("]", 18),
		"oversized":             raw + strings.Repeat(" ", 36*1024),
	}
	var frame map[string]any
	_ = json.Unmarshal([]byte(raw), &frame)
	for field := range frame["diagnostic_agent_stop"].(map[string]any) {
		for _, kind := range []string{"missing", "null"} {
			var copy map[string]any
			_ = json.Unmarshal([]byte(raw), &copy)
			v := copy["diagnostic_agent_stop"].(map[string]any)
			if kind == "missing" {
				delete(v, field)
			} else {
				v[field] = nil
			}
			bad, _ := json.Marshal(copy)
			changes[kind+"_"+field] = string(bad)
		}
	}
	for name, bad := range changes {
		t.Run(name, func(t *testing.T) {
			if bad == raw {
				t.Fatal("unchanged mutation")
			}
			v, err := decodeDiagnosticAgentStop([]byte(bad))
			if err == nil || v.DiagnosticAgentStop != nil || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatal("private/malformed disclosure accepted or echoed", err)
			}
		})
	}
	v, err := decodeDiagnosticAgentStop([]byte(`{"ok":false,"error":"PRIVATE"}`))
	if err != nil || v.OK || strings.Contains(v.Error, "PRIVATE") {
		t.Fatal("raw native error escaped", err)
	}
}
func TestDiagnosticAgentStopClientExactRequestAndLostReplyNoReplay(t *testing.T) {
	for _, kind := range []string{"valid", "parent", "agent", "scope", "mode", "schema", "plan", "lost_reply"} {
		t.Run(kind, func(t *testing.T) {
			q := protocol.DiagnosticMaintenanceRequest{Schema: protocol.DiagnosticsVersion, Operation: "agent_stop", Mode: "preview", Scope: "origin", ParentID: "record", AgentID: "worker"}
			switch kind {
			case "parent":
				q.ParentID = "other"
			case "agent":
				q.AgentID = "other"
			case "scope":
				q.Scope = "feedback"
			case "mode", "lost_reply":
				q.Mode, q.PlanID = "apply", strings.Repeat("a", 64)
			case "schema":
				q.Schema--
			case "plan":
				q.PlanID = strings.Repeat("a", 64)
			}
			dir, err := os.MkdirTemp("", "asc-")
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
			calls := 0
			go func() {
				c, err := listener.Accept()
				if err != nil {
					done <- err
					return
				}
				defer c.Close()
				var req protocol.Request
				if err := json.NewDecoder(c).Decode(&req); err != nil {
					done <- err
					return
				}
				calls++
				if req.Method != "diagnostics-maintenance" || req.Agent != nil || req.Maintenance == nil || *req.Maintenance != q {
					done <- os.ErrInvalid
					return
				}
				if kind != "lost_reply" {
					_, err = c.Write(append(agentStopWireFixture(), '\n'))
				}
				done <- err
			}()
			v, err := MaintainDiagnostics(path, q, time.Second)
			if e := <-done; e != nil {
				t.Fatal(e)
			}
			if (kind == "valid") != (err == nil) || calls != 1 || kind != "valid" && v.DiagnosticAgentStop != nil {
				t.Fatal("request mismatch or lost reply became success/replay", err, calls)
			}
			if kind == "lost_reply" {
				if err := listener.(*net.UnixListener).SetDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
					t.Fatal(err)
				}
				unexpected, err := listener.Accept()
				if err == nil {
					unexpected.Close()
					t.Fatal("lost reply automatically replayed")
				}
				if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
					t.Fatal(err)
				}
			}
		})
	}
}
