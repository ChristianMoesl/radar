package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/logging"
	"radar/internal/protocol"
)

func feedbackDiagnosticsFixture(t *testing.T) (string, string, string) {
	t.Helper()
	home := t.TempDir()
	root := filepath.Join(home, "workspaces")
	configDir := filepath.Join(home, "config/radar")
	anchor := filepath.Join(root, "feedback")
	for _, path := range []string{configDir, anchor} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("RADAR_SOCKET", filepath.Join(home, "absent.sock"))
	log := filepath.Join(home, "radar.log")
	t.Setenv("RADAR_LOG", log)
	config := fmt.Sprintf("workspace:\n  root_dir: %s\nmodel: example/model\n", root)
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	group := workspacegroup.Workspace{ID: workspacegroup.ID(anchor), Name: "Feedback", Path: anchor, Feedback: true, Members: []workspacegroup.Member{}}
	if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
		t.Fatal(err)
	}
	return root, anchor, log
}

func TestFeedbackDiagnosticsAreScopedReadOnlyAndDoNotReadLogsOrSecretsByDefault(t *testing.T) {
	root, anchor, log := feedbackDiagnosticsFixture(t)
	private := "do-not-include-private-content"
	if err := os.WriteFile(log, []byte(private+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	secrets := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "radar/secrets.yaml")
	if err := os.WriteFile(secrets, []byte("unknown-provider:\n  token: "+private+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(workspacegroup.Path(root))
	result, err := feedbackDiagnostics(anchor, "overview", logging.LogQuery{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(result)
	if strings.Contains(string(data), private) || result["logs"] != nil || result["settings"] == nil || result["daemon"].(map[string]any)["available"] != false {
		t.Fatalf("unexpected automatic diagnostic data: %s", data)
	}
	after, _ := os.ReadFile(workspacegroup.Path(root))
	if string(before) != string(after) {
		t.Fatal("diagnostics mutated the registry")
	}
	if _, err := os.Stat(os.Getenv("RADAR_SOCKET")); !os.IsNotExist(err) {
		t.Fatal("diagnostics started a daemon")
	}
	result, err = feedbackDiagnostics(anchor, "logs", logging.LogQuery{Limit: 20})
	if err != nil || result["private"] != true || len(result["logs"].(logging.LogPage).Entries) != 1 {
		t.Fatalf("explicit log projection: %+v %v", result, err)
	}
	registry, _ := workspacegroup.Load(root)
	registry.Workspaces[0].Feedback = false
	if err := workspacegroup.Save(root, registry); err != nil {
		t.Fatal(err)
	}
	if _, err := feedbackDiagnostics(anchor, "overview", logging.LogQuery{Limit: 20}); err == nil {
		t.Fatal("ordinary workspace gained host diagnostics")
	}
	if _, err := feedbackDiagnostics(t.TempDir(), "logs", logging.LogQuery{Limit: 20}); err == nil {
		t.Fatal("unmanaged directory gained host diagnostics")
	}
}

func TestFeedbackDiagnosticsDoNotEnrollRemovedOrigin(t *testing.T) {
	root, anchor, _ := feedbackDiagnosticsFixture(t)
	registry, _ := workspacegroup.Load(root)
	registry.Workspaces[0].FeedbackOrigin = filepath.Join(root, "removed-origin")
	if err := workspacegroup.Save(root, registry); err != nil {
		t.Fatal(err)
	}
	result, err := feedbackDiagnostics(anchor, "overview", logging.LogQuery{Limit: 20})
	if err != nil || result["origin"].(map[string]any)["available"] != false {
		t.Fatalf("removed origin was inspected/enrolled: %+v %v", result, err)
	}
}

func TestFeedbackDiagnosticsCLIUsesStrictMachineBoundary(t *testing.T) {
	_, anchor, _ := feedbackDiagnosticsFixture(t)
	env := []string{"HOME=" + os.Getenv("HOME"), "XDG_CONFIG_HOME=" + os.Getenv("XDG_CONFIG_HOME"), "RADAR_SOCKET=" + os.Getenv("RADAR_SOCKET")}
	stdout, stderr, code := outputCLI(t, []string{"feedback-diagnostics", "--workspace", anchor, "--section", "overview", "--json"}, "", env...)
	if code != 0 || !strings.Contains(stdout, `"section":"overview"`) {
		t.Fatalf("%d %s %s", code, stdout, stderr)
	}
	for _, args := range [][]string{
		{"feedback-diagnostics", "--workspace", anchor},
		{"feedback-diagnostics", "--workspace", anchor, "--section", "arbitrary", "--json"},
		{"feedback-diagnostics", "--workspace", anchor, "--path", "/private", "--json"},
	} {
		if _, _, code := outputCLI(t, args, "", env...); code == 0 {
			t.Fatalf("accepted invalid request: %v", args)
		}
	}
}

func TestFeedbackOverviewDoesNotProbeRegisteredRuntime(t *testing.T) {
	root, anchor, _ := feedbackDiagnosticsFixture(t)
	registry, _ := workspacegroup.Load(root)
	origin := workspacegroup.Workspace{ID: workspacegroup.ID(filepath.Join(root, "origin")), Name: "origin", Path: filepath.Join(root, "origin"), Sandbox: &workspacegroup.Sandbox{Name: "runtime", Agent: "shell"}}
	if err := os.MkdirAll(origin.Path, 0700); err != nil {
		t.Fatal(err)
	}
	registry.Workspaces[0].FeedbackOrigin = origin.Path
	registry.Workspaces = append(registry.Workspaces, origin)
	if err := workspacegroup.Save(root, registry); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	called := filepath.Join(bin, "called")
	if err := os.WriteFile(filepath.Join(bin, "sbx"), fmt.Appendf(nil, "#!/bin/sh\n: > %q\nexit 1\n", called), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	result, err := feedbackDiagnostics(anchor, "overview", logging.LogQuery{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if result["origin"].(map[string]any)["available"] != true {
		t.Fatal("registered origin unavailable")
	}
	if _, err := os.Stat(called); !os.IsNotExist(err) {
		t.Fatal("overview implicitly probed SBX")
	}
}

func TestFeedbackInventoryUnavailableIsNotEmptyOrImplicitInstallationScope(t *testing.T) {
	_, anchor, _ := feedbackDiagnosticsFixture(t)
	for _, scope := range []string{"origin", "feedback", "installation"} {
		result, err := feedbackInventory(anchor, scope, protocol.DiagnosticsRequest{Section: "tasks", Limit: 20})
		if err != nil || result["available"] != false || result["inventory"] != nil {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		want := "daemon_unavailable"
		if scope == "origin" {
			want = "origin_not_registered"
		}
		if result["reason"] != want {
			t.Fatalf("scope %s did not fail closed: %+v", scope, result)
		}
	}
	for _, scope := range []string{"arbitrary", "../host"} {
		if _, err := feedbackInventory(anchor, scope, protocol.DiagnosticsRequest{Section: "tasks", Limit: 20}); err == nil {
			t.Fatal("invalid scope")
		}
	}
	for _, limit := range []int{-1, 0, 51} {
		if _, err := feedbackInventory(anchor, "installation", protocol.DiagnosticsRequest{Section: "tasks", Limit: limit}); err == nil {
			t.Fatal("invalid limit")
		}
	}
}

func TestFeedbackInventoryResolvesOnlyRegisteredScopesAndChecksDaemonSchema(t *testing.T) {
	root, anchor, _ := feedbackDiagnosticsFixture(t)
	origin := filepath.Join(root, "origin")
	registry, _ := workspacegroup.Load(root)
	registry.Workspaces[0].FeedbackOrigin = origin
	registry.Workspaces = append(registry.Workspaces, workspacegroup.Workspace{ID: workspacegroup.ID(origin), Name: "origin", Path: origin})
	if err := workspacegroup.Save(root, registry); err != nil {
		t.Fatal(err)
	}
	socketDir, err := os.MkdirTemp("", "fd-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(socketDir)
	path := filepath.Join(socketDir, "s")
	t.Setenv("RADAR_SOCKET", path)
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	observed := make(chan protocol.Request, 30)
	done := make(chan error, 1)
	go func() {
		for i := 0; i < 30; i++ {
			conn, err := listener.Accept()
			if err != nil {
				done <- err
				return
			}
			var req protocol.Request
			err = json.NewDecoder(conn).Decode(&req)
			if err != nil {
				conn.Close()
				done <- err
				return
			}
			observed <- req
			response := protocol.Response{OK: true, Diagnostics: &protocol.DiagnosticsPage{Schema: protocol.DiagnosticsVersion, Section: req.Diagnostics.Section, Evidence: "cached_daemon_state", Items: []protocol.DiagnosticItem{}, Complete: true}}
			if req.Diagnostics.Section == "operations" {
				elapsed := int64(0)
				response.Diagnostics.Items = []protocol.DiagnosticItem{{ID: req.Diagnostics.ID, Operation: &protocol.DiagnosticOperation{ID: req.Diagnostics.ID, Kind: "cleanup", Status: "partial", TimingEvidence: "native_handler_monotonic_elapsed", StartedAt: "2026-11-17T14:00:01Z", FinishedAt: "2026-11-17T14:00:00Z", ElapsedMilliseconds: &elapsed, PhaseTimingEvidence: "native_completed_phase_aggregates", PhaseTimings: []protocol.OperationPhaseTiming{{Phase: "cleanup_execution", CompletedIntervals: 1, ElapsedMilliseconds: 0}}}}}
			}
			if i == 28 {
				response.Diagnostics.Schema = 99
			}
			if i == 29 {
				response = protocol.Response{OK: false, Error: "unknown method: diagnostics; private-provider-value"}
			}
			err = json.NewEncoder(conn).Encode(response)
			conn.Close()
			if err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	for index, scope := range []string{"origin", "feedback", "installation", "origin", "origin", "feedback", "installation", "origin", "feedback", "installation", "origin", "feedback", "installation", "origin", "feedback", "installation", "origin", "feedback", "installation", "origin", "feedback", "installation", "origin", "feedback", "installation", "origin", "feedback", "installation", "origin", "feedback", "installation"} {
		section := []string{"tasks", "source_refs", "bindings", "agents", "workspace_resources", "workspace_resources", "workspace_resources", "agent_schedules", "agent_schedules", "agent_schedules", "gc_plan", "gc_plan", "gc_plan", "runtime_probe", "runtime_probe", "runtime_probe", "integrations", "integrations", "integrations", "agent_probe", "agent_probe", "agent_probe", "retirement_preview", "retirement_preview", "retirement_preview", "cleanup_preview", "cleanup_preview", "cleanup_preview", "operations", "operations", "operations"}[index]
		if section == "cleanup_preview" {
			continue
		}
		parent := ""
		if index == 1 || index == 2 || section == "workspace_resources" || section == "agent_schedules" || section == "gc_plan" || section == "runtime_probe" || section == "agent_probe" || section == "retirement_preview" || section == "cleanup_preview" {
			parent = "opaque-parent"
		}
		state := "done"
		if section == "agents" || section == "workspace_resources" || section == "agent_schedules" || section == "runtime_probe" || section == "integrations" || section == "agent_probe" || section == "retirement_preview" || section == "cleanup_preview" || section == "operations" {
			state = ""
		}
		id, cursor := "123", "opaque"
		if section == "agent_probe" || section == "retirement_preview" {
			id, cursor = "", ""
		}
		cleanupMode, workspaceID := "", ""
		if section == "cleanup_preview" {
			cleanupMode, workspaceID = "expired", "registered-candidate"
		}
		result, err := feedbackInventory(anchor, scope, protocol.DiagnosticsRequest{Section: section, ParentID: parent, Workspace: "/must-not-pass-through", Limit: 7, ID: id, State: state, Cursor: cursor, CleanupMode: cleanupMode, WorkspaceID: workspaceID})
		if err != nil || result["available"] != true {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		req := <-observed
		want := ""
		if scope == "origin" {
			want = origin
		}
		if scope == "feedback" {
			want = anchor
		}
		method := "diagnostics"
		if section == "runtime_probe" || section == "agent_probe" || section == "retirement_preview" || section == "cleanup_preview" {
			method = "diagnostics-probe"
		}
		if req.Method != method || req.Diagnostics == nil || req.Diagnostics.Workspace != want || req.Diagnostics.Section != section || req.Diagnostics.ParentID != parent || req.Diagnostics.ID != id || req.Diagnostics.Limit != 7 || req.Diagnostics.Cursor != cursor || req.Diagnostics.State != state || req.Diagnostics.CleanupMode != cleanupMode || req.Diagnostics.WorkspaceID != workspaceID {
			t.Fatalf("unexpected request: %+v", req)
		}
		if result["feedback_workspace_id"] != workspacegroup.ID(anchor) {
			t.Fatal("lost feedback identity")
		}
		if section == "operations" {
			page := result["inventory"].(*protocol.DiagnosticsPage)
			if page.Items[0].Operation.PhaseTimingEvidence != "native_completed_phase_aggregates" || len(page.Items[0].Operation.PhaseTimings) != 1 || page.Items[0].Operation.PhaseTimings[0].CompletedIntervals != 1 || page.Items[0].Operation.PhaseTimings[0].ElapsedMilliseconds != 0 || page.Items[0].Operation.TimingEvidence != "native_handler_monotonic_elapsed" || *page.Items[0].Operation.ElapsedMilliseconds != 0 || page.Items[0].Operation.FinishedAt != "2026-11-17T14:00:00Z" {
				t.Fatal("facade lost native monotonic zero/backward-wall timing")
			}
		}
		if scope == "origin" && result["origin_workspace_id"] != workspacegroup.ID(origin) {
			t.Fatal("lost origin identity")
		}
	}
	for i := 0; i < 2; i++ {
		result, err := feedbackInventory(anchor, "installation", protocol.DiagnosticsRequest{Section: "tasks", Limit: 20})
		<-observed
		if err == nil || result != nil || strings.Contains(err.Error(), "private-provider-value") {
			t.Fatalf("mismatch did not fail privately: %+v %v", result, err)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestFeedbackLogCLIFiltersNativeMetadataAndRejectsMixedSelectors(t *testing.T) {
	_, anchor, log := feedbackDiagnosticsFixture(t)
	private := "private-secret-conversation"
	raw := "2026-11-16 12:00:00 WRN source collection skipped source=github duration=1ms error=" + private + "\n"
	if err := os.WriteFile(log, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	env := []string{"HOME=" + os.Getenv("HOME"), "XDG_CONFIG_HOME=" + os.Getenv("XDG_CONFIG_HOME"), "RADAR_LOG=" + log}
	base := []string{"feedback-diagnostics", "--workspace", anchor, "--section", "logs", "--json"}
	stdout, stderr, code := outputCLI(t, append(append([]string{}, base...), "--severity", "warn", "--since", "2026-11-16 12:00:00", "--until", "2026-11-16 12:00:00", "--limit", "100"), "", env...)
	if code != 0 || strings.Contains(stdout, private) || !strings.Contains(stdout, `"event":"source_collection_skipped"`) || !strings.Contains(stdout, `"outcome":"unknown_not_recorded"`) || !strings.Contains(stdout, `"scope":"installation"`) {
		t.Fatalf("%d %s %s", code, stdout, stderr)
	}
	for _, selectors := range [][]string{{"--scope", "origin"}, {"--id", "entity"}, {"--parent-id", "workspace"}, {"--state", "done"}, {"--provider", "github"}, {"--severity", "fatal"}, {"--since", "2026-02-30 00:00:00"}, {"--limit", "101"}, {"--path", log}} {
		if _, _, code := outputCLI(t, append(append([]string{}, base...), selectors...), "", env...); code == 0 {
			t.Fatalf("accepted invalid log selectors %v", selectors)
		}
	}
	stdout, stderr, code = outputCLI(t, append(append([]string{}, base...), "--cursor", "stale"), "", env...)
	if code != 0 || !strings.Contains(stdout, `"reason":"snapshot_changed_restart_pagination"`) || !strings.Contains(stdout, `"available":false`) || strings.Contains(stdout, `"logs":`) {
		t.Fatalf("drift became empty/success: %d %s %s", code, stdout, stderr)
	}
	for _, section := range []string{"overview", "tasks", "runtime_probe"} {
		if _, _, code := outputCLI(t, []string{"feedback-diagnostics", "--workspace", anchor, "--section", section, "--severity", "error", "--json"}, "", env...); code == 0 {
			t.Fatal("accepted log selector on non-log section")
		}
	}
}

func TestFeedbackOverviewReturnsNativePolicySourcesWithoutPrivateSettings(t *testing.T) {
	root, anchor, _ := feedbackDiagnosticsFixture(t)
	path := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "radar", "config.yaml")
	private := "private-model-command-env-path-ABC-123"
	data := fmt.Sprintf("workspace:\n  root_dir: %s\n  auto_confirm: false\nsbx:\n  enabled: false\n  env_file: /%s\n  ready_command: [%s]\nmodel: %s\n", root, private, private, private)
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := feedbackDiagnostics(anchor, "overview", logging.LogQuery{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), private) || result["diagnostics_schema"] != protocol.DiagnosticsVersion || result["feedback_sandbox_registered"] != false {
		t.Fatalf("private or invalid overview: %s", encoded)
	}
	policy := result["settings"].(protocol.DiagnosticPolicy)
	if !policy.Available || policy.Scope != "cli_process_global_defaults" || len(policy.Settings) != 10 {
		t.Fatalf("%+v", policy)
	}
	for _, setting := range policy.Settings {
		if setting.Key == "workspace.auto_confirm" && (setting.Value != "false" || setting.Origin != "configuration_file") {
			t.Fatal("native configuration provenance was lost")
		}
	}
	capabilities := result["capabilities"].(map[string]any)
	if capabilities["configuration_policy_origins"] != true || capabilities["daemon_process_configuration"] != true || capabilities["workspace_effective_policy"] != false || capabilities["daemon_environment"] != false || capabilities["maintenance"] != true || capabilities["collection_only_refresh"] != true || capabilities["cleanup_apply"] != true || capabilities["expiry_cleanup_apply"] != true || capabilities["runtime_controls"] != false || capabilities["agent_stop_daemon_client_transport"] != true || capabilities["agent_stop_previews"] != true || capabilities["agent_stop_apply"] != true || capabilities["current_daemon_managed_operations"] != true || capabilities["native_managed_handler_timing"] != true || capabilities["native_cleanup_completed_phase_aggregates"] != true || capabilities["native_cleanup_returned_target_summaries"] != true || capabilities["native_cleanup_execution_boundaries"] != true || capabilities["durable_operation_history"] != false || capabilities["verified_operation_resource_outcomes"] != false || capabilities["cleanup_preview_lease_contention_probes"] != true || capabilities["current_daemon_agent_supervision"] != true || capabilities["agent_supervision_history"] != false {
		t.Fatalf("policy advertised as authority: %+v", capabilities)
	}
}

func TestFeedbackOverviewUnsafePolicyRemainsUnavailable(t *testing.T) {
	_, anchor, _ := feedbackDiagnosticsFixture(t)
	path := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "radar", "config.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(filepath.Dir(path), "linked.yaml")
	if err := os.WriteFile(target, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	// Normal registration lookup still accepts the native configured symlink;
	// the narrower metadata read cannot silently substitute usable defaults.
	result, err := feedbackDiagnostics(anchor, "overview", logging.LogQuery{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	policy := result["settings"].(protocol.DiagnosticPolicy)
	if policy.Available || policy.Evidence != "unavailable" || len(policy.Settings) != 0 || policy.Revision != "" {
		t.Fatalf("unsafe policy became usable defaults: %+v", policy)
	}
}

func TestFeedbackOverviewKeepsDaemonPolicyDistinctAndRequiresMatchingSchema(t *testing.T) {
	_, anchor, _ := feedbackDiagnosticsFixture(t)
	socketDir, err := os.MkdirTemp("", "fo-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(socketDir)
	path := filepath.Join(socketDir, "s")
	t.Setenv("RADAR_SOCKET", path)
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	observed := make(chan protocol.Request, 4)
	done := make(chan error, 1)
	go func() {
		for i := 0; i < 4; i++ {
			conn, err := listener.Accept()
			if err != nil {
				done <- err
				return
			}
			var req protocol.Request
			if err := json.NewDecoder(conn).Decode(&req); err != nil {
				conn.Close()
				done <- err
				return
			}
			observed <- req
			policy := protocol.DiagnosticPolicy{Scope: "daemon_process_global_defaults", Available: true, LocationOrigin: "user_home", Evidence: "compiled_defaults_configuration_absent", CapturedAt: "2026-11-17T09:00:00Z", Settings: []protocol.DiagnosticSetting{{Key: "workspace.auto_confirm", Value: "false", Origin: "configuration_file", Declaration: "value"}}}
			response := protocol.Response{OK: true, Version: "daemon-version", ResourceRefreshPending: true, DiagnosticOverview: &protocol.DiagnosticOverview{Schema: protocol.DiagnosticsVersion, Settings: policy, Providers: []protocol.DiagnosticProviderStatus{{Name: "test", Status: "partial", DetailsAvailable: true}}}}
			if i == 1 {
				response.DiagnosticOverview.Schema--
			}
			if i == 2 {
				response = protocol.Response{OK: true, Version: "old-summary"}
			}
			if i == 3 {
				response = protocol.Response{OK: false, Error: "private-provider-error"}
			}
			err = json.NewEncoder(conn).Encode(response)
			conn.Close()
			if err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	for i := 0; i < 4; i++ {
		result, err := feedbackDiagnostics(anchor, "overview", logging.LogQuery{Limit: 20})
		if err != nil {
			t.Fatal(err)
		}
		req := <-observed
		if req.Method != "diagnostics" || req.Diagnostics == nil || *req.Diagnostics != (protocol.DiagnosticsRequest{Section: "overview"}) {
			t.Fatalf("overview used general command/fallback: %+v", req)
		}
		daemon := result["daemon"].(map[string]any)
		raw, _ := json.Marshal(result)
		if strings.Contains(string(raw), "private-provider-error") {
			t.Fatal("daemon error escaped")
		}
		if i == 0 {
			policy := daemon["settings"].(protocol.DiagnosticPolicy)
			cli := result["settings"].(protocol.DiagnosticPolicy)
			if daemon["available"] != true || policy.Scope == cli.Scope || policy.Settings[0].Value == cli.Settings[0].Value || daemon["resource_refresh_pending"] != true {
				t.Fatalf("daemon/CLI evidence conflated: %s", raw)
			}
		} else if daemon["available"] != false || daemon["reason"] != "matching_daemon_overview_required" || daemon["settings"] != nil {
			t.Fatalf("version mismatch used fallback: %s", raw)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
