package main

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

func TestFeedbackRefreshIsExplicitInstallationMaintenanceWithoutDaemonStartup(t *testing.T) {
	root, anchor, _ := feedbackDiagnosticsFixture(t)
	before, err := os.ReadFile(workspacegroup.Path(root))
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"preview", "apply"} {
		id := ""
		if mode == "apply" {
			id = strings.Repeat("a", 64)
		}
		result, err := feedbackRefresh(anchor, "installation", protocol.DiagnosticMaintenanceRequest{Operation: "refresh", Mode: mode, PlanID: id})
		want := "daemon_unavailable"
		if mode == "apply" {
			want = "request_outcome_unknown_do_not_replay"
		}
		if err != nil || result["available"] != false || result["reason"] != want || result["maintenance"] != nil || result["scope"] != "installation" {
			t.Fatalf("%s %+v %v", mode, result, err)
		}
	}
	if _, err := os.Stat(os.Getenv("RADAR_SOCKET")); !os.IsNotExist(err) {
		t.Fatal("maintenance started daemon")
	}
	after, err := os.ReadFile(workspacegroup.Path(root))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("unavailable action changed registry")
	}
	for _, scope := range []string{"origin", "feedback", "", "all"} {
		if _, err := feedbackRefresh(anchor, scope, protocol.DiagnosticMaintenanceRequest{Operation: "refresh", Mode: "preview"}); err == nil {
			t.Fatalf("accepted %s scope", scope)
		}
	}
	if _, err := feedbackRefresh(t.TempDir(), "installation", protocol.DiagnosticMaintenanceRequest{Operation: "refresh", Mode: "preview"}); err == nil {
		t.Fatal("unregistered workspace accepted")
	}
}

func TestFeedbackRefreshUsesOnlyExactTypedMaintenanceRPCNoRetry(t *testing.T) {
	_, anchor, _ := feedbackDiagnosticsFixture(t)
	dir, err := os.MkdirTemp("", "rf-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "s")
	t.Setenv("RADAR_SOCKET", path)
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	seen := make(chan protocol.Request, 3)
	done := make(chan error, 1)
	id := strings.Repeat("a", 64)
	go func() {
		for i := 0; i < 3; i++ {
			conn, err := listener.Accept()
			if err != nil {
				done <- err
				return
			}
			var req protocol.Request
			if err = json.NewDecoder(conn).Decode(&req); err != nil {
				conn.Close()
				done <- err
				return
			}
			seen <- req
			if i == 2 {
				conn.Close()
				continue
			} // ambiguous acceptance; never a retry
			view := &protocol.DiagnosticMaintenance{Schema: protocol.DiagnosticsVersion, Operation: "refresh", Mode: req.Maintenance.Mode, Scope: "installation", PlanID: id, Evidence: "native_collection_only_refresh"}
			err = json.NewEncoder(conn).Encode(protocol.Response{OK: true, Maintenance: view})
			conn.Close()
			if err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	for _, mode := range []string{"preview", "apply", "apply"} {
		plan := ""
		if mode == "apply" {
			plan = id
		}
		result, err := feedbackRefresh(anchor, "installation", protocol.DiagnosticMaintenanceRequest{Operation: "refresh", Mode: mode, PlanID: plan})
		if err != nil {
			t.Fatal(err)
		}
		req := <-seen
		if req.Method != "diagnostics-maintenance" || req.Maintenance == nil || req.Maintenance.Schema != protocol.DiagnosticsVersion || req.Maintenance.Operation != "refresh" || req.Maintenance.Scope != "installation" || req.Maintenance.Mode != mode || req.Maintenance.PlanID != plan || req != (protocol.Request{Method: "diagnostics-maintenance", Maintenance: req.Maintenance}) {
			t.Fatalf("unexpected host action %+v", req)
		}
		if result["available"] != true && result["reason"] != "request_outcome_unknown_do_not_replay" {
			t.Fatalf("unexpected unavailable %+v", result)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestFeedbackRefreshCLIRejectsReadSelectorsAndImplicitMutationScope(t *testing.T) {
	_, anchor, _ := feedbackDiagnosticsFixture(t)
	env := []string{"HOME=" + os.Getenv("HOME"), "XDG_CONFIG_HOME=" + os.Getenv("XDG_CONFIG_HOME"), "RADAR_SOCKET=" + os.Getenv("RADAR_SOCKET")}
	base := []string{"feedback-diagnostics", "--workspace", anchor, "--section", "refresh", "--json"}
	for _, extra := range [][]string{
		nil, {"--scope", "origin"}, {"--scope", "feedback"}, {"--scope", "installation", "--action", "apply"},
		{"--scope", "installation", "--action", "preview", "--plan-id", strings.Repeat("a", 64)},
		{"--scope", "installation", "--action", "cleanup"}, {"--scope", "installation", "--id", "target"},
		{"--scope", "installation", "--parent-id", "7"}, {"--scope", "installation", "--limit", "20"},
		{"--scope", "installation", "--cursor", ""}, {"--scope", "installation", "--cleanup-mode", "manual"},
		{"--scope", "installation", "--since", ""}, {"--scope", "installation", "--state", "done"},
	} {
		args := append(append([]string{}, base...), extra...)
		if _, _, code := outputCLI(t, args, "", env...); code == 0 {
			t.Fatalf("accepted invalid refresh %v", extra)
		}
	}
	for _, section := range []string{"overview", "tasks", "cleanup_preview", "logs"} {
		args := []string{"feedback-diagnostics", "--workspace", anchor, "--section", section, "--action", "preview", "--json"}
		if _, _, code := outputCLI(t, args, "", env...); code == 0 {
			t.Fatalf("read section accepted maintenance selector %s", section)
		}
	}
	stdout, stderr, code := outputCLI(t, append(base, "--scope", "installation"), "", env...)
	if code != 0 || !strings.Contains(stdout, `"section":"refresh"`) || !strings.Contains(stdout, `"available":false`) {
		t.Fatalf("preview unavailable=%d %s %s", code, stdout, stderr)
	}
}
