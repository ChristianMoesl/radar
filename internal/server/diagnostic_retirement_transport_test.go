package server

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"radar/internal/client"
	"radar/internal/protocol"
	"strings"
	"testing"
	"time"
)

func TestDiagnosticRetirementTransportRejectsBeforeNativeDispatch(t *testing.T) {
	s, p, q, _ := retirementAdapterFixture(t)
	raw, _ := json.Marshal(protocol.Request{Method: "diagnostics-maintenance", Maintenance: &q})
	valid := string(raw)
	frames := map[string]string{
		"old_schema":        strings.Replace(valid, `"diagnostics_schema":41`, `"diagnostics_schema":35`, 1),
		"new_schema":        strings.Replace(valid, `"diagnostics_schema":41`, `"diagnostics_schema":42`, 1),
		"unknown_envelope":  strings.Replace(valid, `"method":`, `"private":null,"method":`, 1),
		"folded":            strings.Replace(valid, `"parent_id":`, `"PARENT_ID":`, 1),
		"duplicate":         strings.Replace(valid, `"operation":"retire"`, `"operation":"retire","operation":"retire"`, 1),
		"escaped_duplicate": strings.Replace(valid, `"operation":"retire"`, `"oper\u0061tion":"retire","operation":"retire"`, 1),
	}
	for _, field := range []string{"diagnostics_schema", "operation", "mode", "scope", "parent_id", "feedback_workspace", "workspace"} {
		var frame map[string]any
		_ = json.Unmarshal(raw, &frame)
		frame["maintenance"].(map[string]any)[field] = nil
		bad, _ := json.Marshal(frame)
		frames["null_"+field] = string(bad)
	}
	for _, field := range []string{`"command":"PRIVATE"`, `"workspace_id":"origin"`, `"id":"origin"`, `"cursor":""`, `"limit":1`, `"plan_id":""`, `"recipe":null`, `"native_plan_id":"PRIVATE"`} {
		frames[field] = strings.Replace(valid, `"operation":"retire"`, `"operation":"retire",`+field, 1)
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
			if v.OK || p.previews != 0 || p.calls != 0 || len(s.cleanupPlans) != 0 || strings.Contains(v.Error, "PRIVATE") {
				t.Fatal("invalid frame reached backend or leaked", v)
			}
		})
	}
}
func TestDiagnosticRetirementTransportNativeClientRoundTrip(t *testing.T) {
	s, p, req, _ := retirementAdapterFixture(t)
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
	if err != nil || !response.OK || response.DiagnosticRetirement == nil {
		t.Fatal("actual typed preview failed", response, err)
	}
	req.Mode, req.PlanID = "apply", response.DiagnosticRetirement.PlanID
	response, err = client.MaintainDiagnostics(path, req, 2*time.Second)
	if err != nil || !response.OK || response.DiagnosticRetirement == nil || response.DiagnosticRetirement.Outcome != "native_bundle_returned_unverified" {
		t.Fatal("actual typed apply failed", response, err)
	}
	replay, err := client.MaintainDiagnostics(path, req, 2*time.Second)
	if err != nil || replay.OK || replay.DiagnosticRetirement != nil {
		t.Fatal("actual one-use client replay accepted", replay, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if p.previews != 1 || p.calls != 1 {
		t.Fatal("client duplicated native plan/execution")
	}
}
