package client

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"radar/internal/protocol"
	"strings"
	"testing"
	"time"
)

func recreationWireFixture() []byte {
	zero := 0
	v := protocol.DiagnosticRecreation{Schema: protocol.DiagnosticsVersion, Operation: "recreate", Mode: "preview", Scope: "origin", FeedbackWorkspaceID: "feedback", ScopeWorkspaceID: "origin", Evidence: "native_exact_workspace_recreation_plan_not_consent", PlanID: strings.Repeat("a", 64), ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano), Revision: 1, Proposal: protocol.DiagnosticRecreationPreview{ID: "origin", PreviewID: "recreation-preview:" + strings.Repeat("b", 64), Status: "recreate", PreviousState: "running", RuntimeIdentityObserved: true, ExecutionEligibility: "unknown_fresh_execution_guards_not_run", OwnershipEvidence: "fresh_bounded_registration", AgentOwnerCount: &zero, LeaseEvidence: "unknown_no_lock_file"}, Problem: "none", Outcome: "not_attempted", Limitations: protocol.DiagnosticRecreationLimitations()}
	raw, _ := json.Marshal(protocol.Response{OK: true, DiagnosticRecreation: &v})
	return raw
}
func TestDiagnosticRecreationDecoderCompleteShape(t *testing.T) {
	raw := string(recreationWireFixture())
	response, err := decodeDiagnosticRecreation([]byte(raw))
	if err != nil || response.DiagnosticRecreation == nil || !response.OK {
		t.Fatal("valid strict view", response, err)
	}
	changes := map[string]string{
		"private_envelope":      strings.TrimSuffix(raw, "}") + `,"private":"PRIVATE"}`,
		"private_view":          strings.Replace(raw, `"diagnostic_recreation":{`, `"diagnostic_recreation":{"private":"PRIVATE",`, 1),
		"private_proposal":      strings.Replace(raw, `"proposal":{`, `"proposal":{"recipe":"PRIVATE",`, 1),
		"native_identity":       strings.Replace(raw, `"proposal":{`, `"proposal":{"expected_plan_id":"PRIVATE",`, 1),
		"folded":                strings.Replace(raw, `"mode":`, `"MODE":`, 1),
		"duplicate":             strings.Replace(raw, `"mode":"preview"`, `"mode":"apply","mode":"preview"`, 1),
		"escaped_duplicate":     strings.Replace(raw, `"mode":"preview"`, `"m\u006fde":"preview","mode":"preview"`, 1),
		"missing_ok":            strings.Replace(raw, `,"ok":true`, "", 1),
		"missing_schema":        strings.Replace(raw, `"diagnostics_schema":35,`, "", 1),
		"missing_pending":       strings.Replace(raw, `,"resource_refresh_pending":false`, "", 1),
		"missing_identity_bool": strings.Replace(raw, `,"runtime_identity_observed":true`, "", 1),
		"missing_owner":         strings.Replace(raw, `,"agent_owner_count":0`, "", 1),
		"null_owner":            strings.Replace(raw, `"agent_owner_count":0`, `"agent_owner_count":null`, 1),
		"null_pending":          strings.Replace(raw, `"resource_refresh_pending":false`, `"resource_refresh_pending":null`, 1),
		"null_view":             `{"ok":true,"diagnostic_recreation":null}`,
		"old_schema":            strings.Replace(raw, `"diagnostics_schema":35`, `"diagnostics_schema":34`, 1),
		"unknown_outcome":       strings.Replace(raw, `"outcome":"not_attempted"`, `"outcome":"verified_recovery"`, 1),
		"bad_status":            strings.Replace(raw, `"status":"recreate"`, `"status":"healthy"`, 1),
		"eligible":              strings.Replace(raw, `"execution_eligibility":"unknown_fresh_execution_guards_not_run"`, `"execution_eligibility":"eligible"`, 1),
		"error_with_payload":    strings.Replace(raw, `"ok":true`, `"ok":false,"error":"PRIVATE"`, 1),
		"error_private_sibling": `{"ok":false,"error":"PRIVATE","logs":[]}`,
		"missing_error":         `{"ok":false}`,
		"trailing":              raw + ` {"ok":false}`,
		"deep":                  strings.Repeat("[", 18) + raw + strings.Repeat("]", 18),
		"oversized":             raw + strings.Repeat(" ", 36*1024),
	}
	for name, s := range changes {
		t.Run(name, func(t *testing.T) {
			if s == raw {
				t.Fatal("unchanged mutation")
			}
			v, err := decodeDiagnosticRecreation([]byte(s))
			if err == nil || v.DiagnosticRecreation != nil || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatal("private or inconsistent response accepted/leaked", v, err)
			}
		})
	}
	v, err := decodeDiagnosticRecreation([]byte(`{"ok":false,"error":"PRIVATE"}`))
	if err != nil || v.OK || strings.Contains(v.Error, "PRIVATE") {
		t.Fatal("unavailable error leaked", v, err)
	}
}
func TestDiagnosticRecreationClientBindsExactRequest(t *testing.T) {
	for _, kind := range []string{"target", "scope", "mode", "schema", "plan"} {
		t.Run(kind, func(t *testing.T) {
			q := protocol.DiagnosticMaintenanceRequest{Schema: protocol.DiagnosticsVersion, Operation: "recreate", Mode: "preview", Scope: "origin", ParentID: "origin"}
			switch kind {
			case "target":
				q.ParentID = "other"
			case "scope":
				q.Scope = "feedback"
			case "mode":
				q.Mode = "apply"
				q.PlanID = strings.Repeat("a", 64)
			case "schema":
				q.Schema--
			case "plan":
				q.PlanID = strings.Repeat("a", 64)
			}
			dir, err := os.MkdirTemp("", "rc-")
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
				_, err = c.Write(append(recreationWireFixture(), '\n'))
				done <- err
			}()
			response, err := MaintainDiagnostics(path, q, 2*time.Second)
			if e := <-done; e != nil {
				t.Fatal(e)
			}
			if err == nil || response.DiagnosticRecreation != nil {
				t.Fatal("mismatched native request accepted")
			}
		})
	}
}
