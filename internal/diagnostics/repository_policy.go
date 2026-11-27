package diagnostics

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"

	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/state"
)

const repositoryPolicyEvidence = "explicit_current_registered_member_repository_declarations"

var repositoryMemberID = regexp.MustCompile(`^workspace-resource:[a-f0-9]{64}$`)

func repositoryPolicy(ctx context.Context, store *state.Store, req protocol.DiagnosticsRequest, pending bool, providers integration.Registry) (protocol.DiagnosticsPage, error) {
	if req.ParentID == "" || len(req.ParentID) > 512 || !repositoryMemberID.MatchString(req.ID) || req.State != "" || req.Cursor != "" || req.Limit < 0 || req.Limit > 50 || len(req.Workspace) > 4096 || (req.Workspace != "" && !filepath.IsAbs(req.Workspace)) {
		return protocol.DiagnosticsPage{}, fmt.Errorf("repository policy requires one exact registered workspace parent/member ID and no state/cursor selectors")
	}
	provider, err := providers.WorkspaceDiagnostics()
	if err != nil {
		return protocol.DiagnosticsPage{}, fmt.Errorf("repository policy registration capability unavailable")
	}
	reader, ok := provider.(integration.RepositoryPolicyDiagnostics)
	if !ok {
		return protocol.DiagnosticsPage{}, fmt.Errorf("repository policy diagnostics unsupported")
	}
	ctx, cancel := context.WithTimeout(ctx, RuntimeProbeTimeout)
	defer cancel()
	if previewExpired(ctx) {
		return protocol.DiagnosticsPage{}, fmt.Errorf("repository policy cancelled")
	}
	view, err := reader.DiagnosticRepositoryPolicy(ctx, req.ParentID, req.ID, req.Workspace)
	if err != nil {
		return protocol.DiagnosticsPage{}, fmt.Errorf("repository policy registration unavailable in selected scope")
	}
	if view.ID != req.ID || view.WorkspaceID != req.ParentID || view.Origin != "current_registered_member_repository" {
		return protocol.DiagnosticsPage{}, fmt.Errorf("repository policy evidence invalid")
	}
	// A provider's typed result is not itself a privacy boundary. Validate the
	// complete finite projection before publishing it to any native client, not
	// only to Pi. Detach borrowed settings so later provider writes cannot change
	// the already-validated page or its snapshot.
	if view.Settings == nil || len(view.Settings) > 10 {
		return protocol.DiagnosticsPage{}, fmt.Errorf("repository policy evidence invalid")
	}
	view.Settings = append([]protocol.DiagnosticSetting{}, view.Settings...)
	if !validRepositoryPolicy(view) {
		return protocol.DiagnosticsPage{}, fmt.Errorf("repository policy evidence invalid")
	}
	// A typed provider may finish after cancellation or before an expired
	// deadline's timer notification. Never publish its declarations as fresh.
	// Check before our own deferred cancel, preserving healthy observations.
	if previewExpired(ctx) {
		view.Available, view.Evidence, view.Reason = false, "unavailable", "cancelled_or_timed_out"
		view.Settings = []protocol.DiagnosticSetting{}
	}
	req.Limit = 1
	return paginate(req, []protocol.DiagnosticItem{{ID: view.ID, RepositoryPolicy: &view}}, store.Revision(), pending, repositoryPolicyEvidence)
}

// Match the existing repository declaration contract, not production policy
// evaluation: these are presence/count/mode values and current-file origins.
func validRepositoryPolicy(view protocol.DiagnosticRepositoryPolicy) bool {
	if !view.Available {
		if view.Evidence != "unavailable" || len(view.Settings) != 0 {
			return false
		}
		switch view.Reason {
		case "repository_unavailable", "configuration_read_unavailable", "configuration_not_regular_or_exceeds_bound", "configuration_changed_or_read_incomplete", "configuration_invalid", "registration_changed_or_unavailable", "cancelled_or_timed_out":
			return true
		}
		return false
	}
	if view.Reason != "" || (view.Evidence != "fresh_bounded_repository_configuration" && view.Evidence != "repository_configuration_absent") || len(view.Settings) != 10 {
		return false
	}
	seen := make(map[string]bool, len(view.Settings))
	for _, setting := range view.Settings {
		if seen[setting.Key] {
			return false
		}
		seen[setting.Key] = true
		defaultValue := "false"
		switch setting.Key {
		case "copy_files", "setup", "sbx.additional_mounts":
			// Counts must have the same canonical safe-integer representation as
			// the version-matched Pi contract, never arbitrary strings or floats.
			n, err := strconv.ParseUint(setting.Value, 10, 53)
			if err != nil || strconv.FormatUint(n, 10) != setting.Value {
				return false
			}
			defaultValue = "0"
		case "sbx.enabled":
			if setting.Value != "inherit" && setting.Value != "required" && setting.Value != "disabled" {
				return false
			}
			defaultValue = "inherit"
		case "model", "thinking", "sbx.ready_command", "sbx.env_file", "sbx.kit.name", "sbx.kit.path":
			if setting.Value != "true" && setting.Value != "false" {
				return false
			}
		default:
			return false
		}
		switch setting.Declaration {
		case "value":
			if setting.Origin != "repository_file" {
				return false
			}
		case "absent", "null":
			if setting.Origin != "repository_default" || setting.Value != defaultValue {
				return false
			}
		default:
			return false
		}
		if view.Evidence == "repository_configuration_absent" && setting.Declaration != "absent" {
			return false
		}
	}
	return true
}
