package diagnostics

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"radar/internal/agentservice"
	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/state"
)

const RuntimeProbeTimeout = 5 * time.Second

// Probe is separate from Read: only an explicitly selected registered workspace
// or agent may trigger bounded native runtime listings or runner socket reads.
// It never calls collection, cleanup preview/execution, refresh, login, startup
// or lease acquisition.
func Probe(ctx context.Context, store *state.Store, req protocol.DiagnosticsRequest, pending bool, providers integration.Registry) (protocol.DiagnosticsPage, error) {
	if req.Section == "agent_probe" {
		return probeAgent(ctx, store, req, pending)
	}
	if req.Section != "runtime_probe" || req.ParentID == "" || len(req.ParentID) > 512 || len(req.ID) > 512 || len(req.Cursor) > 512 || req.State != "" || (req.Workspace != "" && (!filepath.IsAbs(req.Workspace) || len(req.Workspace) > 4096)) || req.Limit < 0 || req.Limit > 50 {
		return protocol.DiagnosticsPage{}, fmt.Errorf("runtime probe requires an exact registered workspace parent and bounded selectors")
	}
	if req.Limit == 0 {
		req.Limit = 20
	}
	ctx, cancel := context.WithTimeout(ctx, RuntimeProbeTimeout)
	defer cancel()
	if ctx.Err() != nil {
		return protocol.DiagnosticsPage{}, fmt.Errorf("runtime probe cancelled")
	}
	provider, err := providers.WorkspaceDiagnostics()
	if err != nil {
		return protocol.DiagnosticsPage{}, fmt.Errorf("runtime probe registration capability unsupported")
	}
	groups, err := provider.DiagnosticWorkspaceRegistrations()
	if err != nil {
		return protocol.DiagnosticsPage{}, fmt.Errorf("runtime probe registration unavailable")
	}
	registrationSnapshot, _ := json.Marshal(groups)
	var selected *protocol.DiagnosticWorkspace
	for _, group := range groups {
		if group.ID != req.ParentID || (req.Workspace != "" && filepath.Clean(req.Workspace) != filepath.Clean(group.Path)) {
			continue
		}
		if selected != nil {
			return protocol.DiagnosticsPage{}, fmt.Errorf("runtime probe registration ambiguous")
		}
		copy := group
		selected = &copy
	}
	if selected == nil {
		return protocol.DiagnosticsPage{}, fmt.Errorf("runtime probe parent is not registered in the selected scope")
	}
	_, refs, revision := store.InspectionSnapshot()
	items := []protocol.DiagnosticItem{}
	for _, source := range []string{"sbx", "tmux"} {
		name := selected.SandboxName
		if source == "tmux" {
			name = selected.SessionName
		}
		if strings.TrimSpace(name) == "" {
			continue
		}
		identity, _ := json.Marshal([]string{selected.ID, source, name})
		sum := sha256.Sum256(identity)
		id := "runtime:" + hex.EncodeToString(sum[:])
		if req.ID != "" && req.ID != id {
			continue
		}
		view := protocol.DiagnosticRuntime{ID: id, WorkspaceID: clip(selected.ID), Provider: source, Name: clip(name), IdentityComparison: "unknown_no_cached_identity", OwnershipEvidence: "unknown_not_checked", LeaseEvidence: "unknown_not_checked", Observation: protocol.DiagnosticRuntimeObservation{Presence: "unknown", State: "unknown", Problem: "unsupported"}}
		expected := map[string]bool{}
		for _, ref := range refs {
			if !ref.Active || ref.Snapshot.Source != source || !recordInWorkspace([]state.SourceRefRecord{ref}, selected.Path) {
				continue
			}
			cachedName, cachedID := ref.Snapshot.Metadata["name"], ref.Snapshot.Metadata["id"]
			if source == "tmux" {
				cachedName, cachedID = ref.Snapshot.Title, ref.Snapshot.ID
			}
			if cachedName == name && strings.TrimSpace(cachedID) != "" {
				expected[cachedID] = true
			}
		}
		view.CachedIdentityCount = len(expected)
		owners := 0
		for _, group := range groups {
			other := group.SandboxName
			if source == "tmux" {
				other = group.SessionName
			}
			if other == name {
				owners++
			}
		}
		probe, probeErr := providers.RuntimeProbe(source)
		switch {
		case ctx.Err() != nil:
			view.Observation.Problem = "cancelled_or_timed_out"
		case owners != 1:
			view.Observation.Problem = "shared_registered_name"
		case probeErr == nil:
			view.Observation = runtimeObservation(probe.DiagnosticRuntime(ctx, name))
		}
		if ctx.Err() != nil {
			view.Observation = protocol.DiagnosticRuntimeObservation{Presence: "unknown", State: "unknown", Problem: "cancelled_or_timed_out"}
		}
		if view.Observation.Presence == "present" {
			if len(expected) > 1 {
				view.IdentityComparison = "unknown_ambiguous_cached_identity"
			} else if len(expected) == 1 {
				view.IdentityComparison = "differs_from_cached_identity"
				if expected[view.Observation.ID] {
					view.IdentityComparison = "matches_cached_identity"
				}
			}
			// Compare full native identity before replacing it with an opaque ID.
			hash := sha256.Sum256([]byte(view.Observation.ID))
			view.Observation.ID = "runtime-id:" + hex.EncodeToString(hash[:])
		} else {
			view.IdentityComparison = "not_observed"
		}
		items = append(items, protocol.DiagnosticItem{ID: id, Runtime: &view})
	}
	current, err := provider.DiagnosticWorkspaceRegistrations()
	if err != nil {
		return protocol.DiagnosticsPage{}, fmt.Errorf("runtime registration unavailable after probe")
	}
	after, _ := json.Marshal(current)
	if string(registrationSnapshot) != string(after) {
		return protocol.DiagnosticsPage{}, fmt.Errorf("runtime registration changed; probe again")
	}
	if store.Revision() != revision {
		return protocol.DiagnosticsPage{}, fmt.Errorf("diagnostic snapshot changed; probe again")
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return paginate(req, items, revision, pending, "explicit_runtime_probe_with_cached_identity_comparison")
}

func runtimeObservation(view protocol.DiagnosticRuntimeObservation) protocol.DiagnosticRuntimeObservation {
	if view.Presence != "present" && view.Presence != "absent" && view.Presence != "unknown" {
		return protocol.DiagnosticRuntimeObservation{Presence: "unknown", State: "unknown", Problem: "invalid_evidence"}
	}
	if view.Presence == "present" && strings.TrimSpace(view.ID) == "" {
		return protocol.DiagnosticRuntimeObservation{Presence: "unknown", State: "unknown", Problem: "invalid_evidence"}
	}
	if view.Presence != "present" {
		view.ID = ""
	}
	if view.State != "running" && view.State != "stopped" {
		view.State = "unknown"
	}
	if view.Problem != "" {
		if view.Problem != "cancelled_or_timed_out" {
			view.Problem = "probe_failed"
		}
		return protocol.DiagnosticRuntimeObservation{Presence: "unknown", State: "unknown", Problem: view.Problem}
	}
	if view.Presence != "present" {
		view.State = "unknown"
	}
	return view
}

func probeAgent(ctx context.Context, store *state.Store, req protocol.DiagnosticsRequest, pending bool) (protocol.DiagnosticsPage, error) {
	if req.ParentID == "" || len(req.ParentID) > 64 || req.ID != "" || req.State != "" || req.Cursor != "" || req.Limit < 0 || req.Limit > 50 || (req.Workspace != "" && (!filepath.IsAbs(req.Workspace) || len(req.Workspace) > 4096)) {
		return protocol.DiagnosticsPage{}, fmt.Errorf("agent probe requires one exact registered agent parent and no row/state/cursor selectors")
	}
	revision := store.Revision()
	view, err := agentservice.ProbeDiagnosticAgent(ctx, req.Workspace, req.ParentID)
	if err != nil {
		return protocol.DiagnosticsPage{}, err
	}
	if revision != store.Revision() {
		return protocol.DiagnosticsPage{}, fmt.Errorf("diagnostic snapshot changed; probe again")
	}
	req.Limit = 1
	return paginate(req, []protocol.DiagnosticItem{{ID: view.ID, AgentProbe: &view}}, revision, pending, "explicit_agent_socket_probe_no_checkpoint_fallback")
}
