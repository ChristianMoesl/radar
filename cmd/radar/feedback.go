package main

import (
	"errors"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"radar/internal/client"
	"radar/internal/config"
	"radar/internal/integration/workspace"
	"radar/internal/logging"
	"radar/internal/protocol"
	"radar/internal/socket"
	"radar/internal/version"
)

// This command is the fixed, read-only host boundary for feedback sessions. It
// never starts/restarts the daemon, refreshes providers, exports raw state or reads
// secrets. Log contents require a separate confirmation in the Pi tool.
func runFeedbackDiagnostics(args []string) {
	flags := outputFlags("radar feedback-diagnostics")
	current := flags.String("workspace", "", "registered feedback workspace")
	section := flags.String("section", "overview", "overview, tasks, workspaces, source_refs, bindings or logs")
	scope := flags.String("scope", "origin", "origin, feedback or installation (inventories only)")
	id := flags.String("id", "", "stable entity ID filter")
	parentID := flags.String("parent-id", "", "source_refs: task ID; bindings: retained source-ref ID")
	state := flags.String("state", "", "exact retained task state filter")
	limit := flags.Int("limit", 20, "inventory page size (1-50)")
	cursor := flags.String("cursor", "", "opaque inventory continuation cursor")
	_ = parseFlags(flags, args)
	if flags.NArg() != 0 || *current == "" || (*section != "overview" && *section != "logs" && *section != "tasks" && *section != "workspaces" && *section != "source_refs" && *section != "bindings") {
		fatal(errors.New("usage: radar feedback-diagnostics --workspace <feedback workspace> --section overview|tasks|workspaces|source_refs|bindings|logs --json"))
	}
	if !jsonOutput {
		fatal(errors.New("feedback-diagnostics requires --json"))
	}
	var result map[string]any
	var err error
	if *section == "tasks" || *section == "workspaces" || *section == "source_refs" || *section == "bindings" {
		result, err = feedbackInventory(*current, *scope, protocol.DiagnosticsRequest{Section: *section, ParentID: *parentID, ID: *id, State: *state, Limit: *limit, Cursor: *cursor})
	} else {
		if *scope != "origin" || *id != "" || *parentID != "" || *state != "" || *cursor != "" || *limit != 20 {
			fatal(errors.New("inventory selectors require an inventory section"))
		}
		result, err = feedbackDiagnostics(*current, *section)
	}
	if err != nil {
		fatal(err)
	}
	printResult(result)
}

func feedbackDiagnostics(current, section string) (map[string]any, error) {
	root, group, found, err := workspace.RegisteredWorkspace(current, "")
	if err != nil || !found || !group.Feedback {
		return nil, errors.New("diagnostics require a registered Radar feedback workspace")
	}
	if section == "logs" {
		path, err := logging.Path()
		if err != nil {
			return nil, errors.New("Radar log path unavailable")
		}
		text, truncated, err := feedbackLogTail(path)
		if err != nil {
			return nil, errors.New("Radar log unavailable")
		}
		return map[string]any{"diagnostics_schema": protocol.DiagnosticsVersion, "section": section, "private": true, "logs": text, "truncated": truncated,
			"warning": "Logs may contain private values. Do not publish raw logs; review and include only approved, redacted evidence."}, nil
	}
	result := map[string]any{
		"diagnostics_schema": protocol.DiagnosticsVersion, "section": "overview", "version": version.Number, "commit": version.Commit,
		"built": version.Date, "os": runtime.GOOS, "arch": runtime.GOARCH,
		"daemon":       map[string]any{"available": false},
		"capabilities": map[string]any{"sections": []string{"overview", "tasks", "workspaces", "source_refs", "bindings", "logs"}, "maintenance": false, "runtime_probes": false, "retained_inventory_requires_matching_daemon": true},
	}
	// Explicit allowlist: do not serialize config.Config, credentials, provider
	// endpoints, environment files, startup commands or task/note contents.
	if cfg, err := config.Load(); err == nil {
		result["settings"] = map[string]any{"model": cfg.Model, "thinking": cfg.Thinking,
			"workspace_auto_confirm": cfg.Workspace.AutoConfirm, "feedback_sandboxed": group.Sandbox != nil}
	}
	if path, err := socket.Path(); err == nil {
		if response, err := client.CallWithTimeout(path, "summary", time.Second); err == nil && response.OK {
			providers := make([]map[string]any, 0, len(response.Sources))
			for _, source := range response.Sources {
				providers = append(providers, map[string]any{"name": source.Name, "status": source.Status,
					"details_available": source.Detail != ""})
			}
			result["daemon"] = map[string]any{"available": true, "version": response.Version, "providers": providers}
		}
	}
	if group.FeedbackOrigin != "" {
		// Resolve again so an unregistered origin cannot become arbitrary
		// host filesystem access. Never enroll an unmanaged directory.
		_, origin, exists, err := workspace.RegisteredWorkspace(group.FeedbackOrigin, root)
		if err == nil && exists && origin.Path == group.FeedbackOrigin {
			members := make([]map[string]string, 0, len(origin.Members))
			for _, member := range origin.Members[:min(len(origin.Members), 16)] {
				members = append(members, map[string]string{"path": member.Path, "repository": member.Repository})
			}
			var sandbox any
			if origin.Sandbox != nil {
				sandbox = map[string]any{"name": origin.Sandbox.Name, "runtime_evidence": "unknown_not_probed"}
			}
			result["origin"] = map[string]any{"available": true, "id": origin.ID, "path": origin.Path, "members": members, "sandbox": sandbox, "evidence": "fresh_registration", "truncated": len(origin.Members) > 16}

		}
		if result["origin"] == nil {
			result["origin"] = map[string]any{"available": false}
		}
	}
	return result, nil
}

func feedbackLogTail(path string) (string, bool, error) {
	const maxBytes = 32 * 1024
	file, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", false, err
	}
	if !info.Mode().IsRegular() {
		return "", false, errors.New("log is not a regular file")
	}
	offset := max(int64(0), info.Size()-maxBytes)
	data := make([]byte, min(info.Size(), maxBytes))
	n, err := file.ReadAt(data, offset)
	if err != nil && err != io.EOF {
		return "", false, err
	}
	text := string(data[:n])
	if offset > 0 {
		if newline := strings.IndexByte(text, '\n'); newline >= 0 {
			text = text[newline+1:]
		}
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	truncated := offset > 0 || len(lines) > 100
	if len(lines) > 100 {
		lines = lines[len(lines)-100:]
	}
	return strings.Join(lines, "\n"), truncated, nil
}

func feedbackInventory(current, scope string, req protocol.DiagnosticsRequest) (map[string]any, error) {
	if scope != "origin" && scope != "feedback" && scope != "installation" {
		return nil, errors.New("invalid diagnostic scope")
	}
	if req.Limit < 1 || req.Limit > 50 || len(req.ID) > 512 || len(req.ParentID) > 512 || len(req.State) > 64 || len(req.Cursor) > 512 {
		return nil, errors.New("diagnostic selector exceeds bounds")
	}
	if req.ParentID != "" && req.Section != "source_refs" && req.Section != "bindings" {
		return nil, errors.New("parent-id requires source_refs or bindings")
	}
	root, group, found, err := workspace.RegisteredWorkspace(current, "")
	if err != nil || !found || !group.Feedback {
		return nil, errors.New("diagnostics require a registered Radar feedback workspace")
	}
	result := map[string]any{"diagnostics_schema": protocol.DiagnosticsVersion, "section": req.Section, "scope": scope, "available": false, "feedback_workspace_id": group.ID, "evidence": "unavailable"}
	req.Workspace = "" // Only the registered scope below may supply a path.
	if scope == "feedback" {
		req.Workspace = group.Path
	}
	if scope == "origin" {
		if group.FeedbackOrigin == "" {
			result["reason"] = "origin_not_registered"
			return result, nil
		}
		_, origin, exists, err := workspace.RegisteredWorkspace(group.FeedbackOrigin, root)
		if err != nil || !exists || origin.Path != group.FeedbackOrigin {
			result["reason"] = "origin_not_registered"
			return result, nil
		}
		req.Workspace = origin.Path
		result["origin_workspace_id"] = origin.ID
	}
	path, err := socket.Path()
	if err != nil {
		result["reason"] = "daemon_unavailable"
		return result, nil
	}
	response, err := client.InspectDiagnostics(path, req, 2*time.Second)
	if err != nil {
		result["reason"] = "daemon_unavailable"
		return result, nil
	}
	if !response.OK && strings.HasPrefix(response.Error, "diagnostic snapshot changed;") {
		result["reason"] = "snapshot_changed_restart_pagination"
		return result, nil
	}
	if !response.OK || response.Diagnostics == nil {
		return nil, errors.New("diagnostic inventory unavailable: matching daemon/CLI support and a fresh pagination snapshot are required")
	}
	if response.Diagnostics.Schema != protocol.DiagnosticsVersion || response.Diagnostics.Section != req.Section {
		return nil, errors.New("diagnostic inventory version mismatch")
	}
	result["available"] = true
	result["evidence"] = response.Diagnostics.Evidence
	result["inventory"] = response.Diagnostics
	return result, nil
}
