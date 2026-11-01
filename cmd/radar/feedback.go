package main

import (
	"context"
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
	"radar/internal/socket"
	"radar/internal/version"
)

// This command is the fixed, read-only host boundary for feedback sessions. It
// never starts/restarts the daemon, refreshes providers, exports state or reads
// secrets. Log contents require a separate confirmation in the Pi tool.
func runFeedbackDiagnostics(args []string) {
	flags := outputFlags("radar feedback-diagnostics")
	current := flags.String("workspace", "", "registered feedback workspace")
	section := flags.String("section", "overview", "overview or logs")
	_ = parseFlags(flags, args)
	if flags.NArg() != 0 || *current == "" || (*section != "overview" && *section != "logs") {
		fatal(errors.New("usage: radar feedback-diagnostics --workspace <feedback workspace> --section overview|logs --json"))
	}
	if !jsonOutput {
		fatal(errors.New("feedback-diagnostics requires --json"))
	}
	result, err := feedbackDiagnostics(*current, *section)
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
		return map[string]any{"section": section, "private": true, "logs": text, "truncated": truncated,
			"warning": "Logs may contain private values. Do not publish raw logs; review and include only approved, redacted evidence."}, nil
	}
	result := map[string]any{
		"section": "overview", "version": version.Number, "commit": version.Commit,
		"built": version.Date, "os": runtime.GOOS, "arch": runtime.GOARCH,
		"daemon": map[string]any{"available": false},
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
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			resources, inspectErr := workspace.InspectWorkspaceResources(ctx, workspace.ExecRunner{}, origin.Path, root)
			cancel()
			if inspectErr == nil {
				members := make([]map[string]string, 0, len(resources.Members))
				for _, member := range resources.Members {
					members = append(members, map[string]string{"path": member.Path, "repository": member.Repository})
				}
				// Do not forward provider stderr/runtime_error strings in the
				// automatic projection. Detailed evidence belongs in approved logs.
				var sandbox any
				if resources.Sandbox != nil {
					sandbox = map[string]any{"name": resources.Sandbox.Name, "shared_directory_ready": resources.Sandbox.SharedDirectoryReady,
						"runtime_error": resources.Sandbox.RuntimeError != ""}
				}
				result["origin"] = map[string]any{"available": true, "path": origin.Path, "members": members, "sandbox": sandbox}
			}
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
