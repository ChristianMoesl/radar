package client

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"radar/internal/protocol"
)

func manualWireFixture() []byte {
	zero := 0
	v := protocol.DiagnosticCleanup{Schema: protocol.DiagnosticsVersion, Operation: "cleanup", Mode: "preview", Scope: "origin", RecordID: "record", TaskID: 7, FeedbackWorkspaceID: "feedback", ScopeWorkspaceID: "origin", Evidence: "native_exact_manual_cleanup_plan_not_consent", Status: "preview", Problem: "none", PlanID: strings.Repeat("a", 64), ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano), Revision: 1, Targets: []protocol.DiagnosticCleanupTarget{{ID: "cleanup-target:" + strings.Repeat("b", 64), RecordID: "record", TaskID: 7, Provider: "git", Effect: "remove_worktree_and_local_changes", BranchEffect: "retain_local_branch", CleanupMode: "manual", Preserved: []string{"remote_repositories"}, Safety: []protocol.DiagnosticCleanupSafety{{Kind: "local_changes", BlocksAutomatic: true, Expires: true, Count: 1}}, SafetyNoticeCount: 1, AutomaticBlockerCount: 1, OwnershipEvidence: "fresh_bounded_registration", AgentOwnerCount: &zero, LeaseEvidence: "unknown_no_lock_file", ExecutionEligibility: "unknown_fresh_execution_guards_not_run"}}, Limitations: protocol.DiagnosticCleanupLimitations()}
	raw, _ := json.Marshal(protocol.Response{OK: true, DiagnosticCleanup: &v})
	return raw
}
func TestDiagnosticManualCleanupDecoderExactPublicShape(t *testing.T) {
	raw := manualWireFixture()
	req := protocol.DiagnosticMaintenanceRequest{Schema: protocol.DiagnosticsVersion, Operation: "cleanup", Mode: "preview", Scope: "origin", ParentID: "record"}
	response, err := decodeDiagnosticCleanup(raw)
	if err != nil || !response.OK || response.DiagnosticCleanup == nil || !protocol.ValidDiagnosticCleanup(*response.DiagnosticCleanup, req) {
		t.Fatalf("valid public projection failed %+v %v", response, err)
	}
	for _, kind := range []string{"private_envelope", "private_plan", "private_target", "private_safety", "duplicate_envelope", "duplicate_plan", "duplicate_target", "duplicate_safety", "folded", "escaped_duplicate", "missing_ok", "missing_schema", "missing_pending", "missing_zero_owner", "missing_notice_bool", "null_pending", "null_safety", "null_owner", "null_plan", "private_error_sibling", "contradictory_ok", "success_with_error", "failure_with_plan", "empty_error", "trailing_frame", "deep", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			s := string(raw)
			switch kind {
			case "private_envelope":
				s = strings.TrimSuffix(s, "}") + `,"private":"private-sentinel"}`
			case "private_plan":
				s = strings.Replace(s, `"diagnostic_cleanup":{`, `"diagnostic_cleanup":{"private":"private-sentinel",`, 1)
			case "private_target":
				s = strings.Replace(s, `"targets":[{`, `"targets":[{"path":"private-sentinel",`, 1)
			case "private_safety":
				s = strings.Replace(s, `"safety":[{`, `"safety":[{"message":"private-sentinel",`, 1)
			case "duplicate_envelope":
				s = strings.Replace(s, `"ok":true`, `"ok":false,"ok":true`, 1)
			case "duplicate_plan":
				s = strings.Replace(s, `"mode":"preview"`, `"mode":"apply","mode":"preview"`, 1)
			case "duplicate_target":
				s = strings.Replace(s, `"provider":"git"`, `"provider":"sbx","provider":"git"`, 1)
			case "duplicate_safety":
				s = strings.Replace(s, `"count":1`, `"count":2,"count":1`, 1)
			case "folded":
				s = strings.Replace(s, `"mode":"preview"`, `"MODE":"preview"`, 1)
			case "escaped_duplicate":
				s = strings.Replace(s, `"mode":"preview"`, `"m\u006fde":"preview","mode":"preview"`, 1)
			case "missing_ok":
				s = strings.Replace(s, `,"ok":true`, "", 1)
			case "missing_schema":
				s = strings.Replace(s, `"diagnostics_schema":`+strconv.Itoa(protocol.DiagnosticsVersion)+`,`, "", 1)
			case "missing_pending":
				s = strings.Replace(s, `,"resource_refresh_pending":false`, "", 1)
			case "missing_zero_owner":
				s = strings.Replace(s, `,"agent_owner_count":0`, "", 1)
			case "missing_notice_bool":
				s = strings.Replace(s, `,"expires":true`, "", 1)
			case "null_pending":
				s = strings.Replace(s, `"resource_refresh_pending":false`, `"resource_refresh_pending":null`, 1)
			case "null_safety":
				s = strings.Replace(s, `"safety":[{"kind":"local_changes","blocks_automatic":true,"expires":true,"count":1}]`, `"safety":null`, 1)
			case "null_owner":
				s = strings.Replace(s, `"agent_owner_count":0`, `"agent_owner_count":null`, 1)
			case "null_plan":
				s = `{"ok":true,"diagnostic_cleanup":null}`
			case "private_error_sibling":
				s = `{"ok":false,"error":"private-sentinel","logs":[]}`
			case "contradictory_ok":
				s = `{"ok":true}`
			case "success_with_error":
				s = strings.TrimSuffix(s, "}") + `,"error":"private-sentinel"}`
			case "failure_with_plan":
				s = strings.Replace(s, `"ok":true`, `"ok":false,"error":"private-sentinel"`, 1)
			case "empty_error":
				s = `{"ok":false,"error":""}`
			case "trailing_frame":
				s += ` {"ok":false,"error":"private-sentinel"}`
			case "deep":
				s = strings.Repeat("[", 18) + s + strings.Repeat("]", 18)
			case "oversized":
				s += strings.Repeat(" ", 36*1024)
			}
			if s == string(raw) {
				t.Fatal("invalid fixture did not change input")
			}
			response, err := decodeDiagnosticCleanup([]byte(s))
			if err == nil || response.DiagnosticCleanup != nil || strings.Contains(err.Error(), "private-sentinel") {
				t.Fatalf("invalid shape accepted or leaked: %+v %v", response, err)
			}
		})
	}
}
func TestDiagnosticManualCleanupDecoderSanitizedNativeFailure(t *testing.T) {
	response, err := decodeDiagnosticCleanup([]byte(`{"ok":false,"error":"exact native manual cleanup unavailable"}`))
	if err != nil || response.OK || response.DiagnosticCleanup != nil {
		t.Fatalf("finite native failure: %+v %v", response, err)
	}
}

func TestDiagnosticManualCleanupClientBindsActualRequest(t *testing.T) {
	for _, kind := range []string{"record", "scope", "mode", "plan"} {
		t.Run(kind, func(t *testing.T) {
			var response protocol.Response
			if err := json.Unmarshal(manualWireFixture(), &response); err != nil {
				t.Fatal(err)
			}
			v := response.DiagnosticCleanup
			req := protocol.DiagnosticMaintenanceRequest{Schema: protocol.DiagnosticsVersion, Operation: "cleanup", Mode: "preview", Scope: "origin", ParentID: "record"}
			switch kind {
			case "record":
				req.ParentID = "different"
			case "scope":
				req.Scope = "installation"
			case "mode":
				req.Mode = "apply"
				req.PlanID = v.PlanID
			case "plan":
				req.Mode = "apply"
				req.PlanID = strings.Repeat("c", 64)
				v.Mode, v.Status, v.OperationID = "apply", "returned", "operation:1"
				v.ResourceRefreshPending = true
				one := 1
				v.CompletedTargets = &one
			}
			dir, err := os.MkdirTemp("", "mc-")
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
				conn, err := listener.Accept()
				if err != nil {
					done <- err
					return
				}
				defer conn.Close()
				var request protocol.Request
				if err = json.NewDecoder(conn).Decode(&request); err == nil {
					err = json.NewEncoder(conn).Encode(response)
				}
				done <- err
			}()
			got, err := MaintainDiagnostics(path, req, 2*time.Second)
			if err == nil || got.DiagnosticCleanup != nil {
				t.Fatalf("response adopted wrong %s request: %+v %v", kind, got, err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}
