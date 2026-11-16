package diagnostics

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"time"

	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/state"
)

func integrationItems(req protocol.DiagnosticsRequest, refs []state.SourceRefRecord, statuses []protocol.SourceStatus, attempts map[string]protocol.DiagnosticCollectionEvidence, providers integration.Registry) ([]protocol.DiagnosticItem, error) {
	registrations := map[string][]protocol.DiagnosticIntegration{}
	cached := map[string][]protocol.SourceStatus{}
	scoped := map[string][]state.SourceRefRecord{}
	names := map[string]bool{}
	for _, registration := range providers.DiagnosticIntegrations() {
		if registration.Name == "" {
			return nil, fmt.Errorf("integration registration identity unavailable")
		}
		registrations[registration.Name] = append(registrations[registration.Name], registration)
		if req.Workspace == "" {
			names[registration.Name] = true
		}
	}
	for _, status := range statuses {
		if status.Name == "" {
			return nil, fmt.Errorf("cached integration status identity unavailable")
		}
		cached[status.Name] = append(cached[status.Name], status)
		if req.Workspace == "" {
			names[status.Name] = true
		}
	}
	for _, ref := range refs {
		if req.Workspace != "" && !recordInWorkspace([]state.SourceRefRecord{ref}, req.Workspace) {
			continue
		}
		if ref.Source == "" {
			return nil, fmt.Errorf("retained integration reference identity unavailable")
		}
		names[ref.Source] = true
		scoped[ref.Source] = append(scoped[ref.Source], ref)
	}
	if req.Workspace == "" {
		for name := range attempts {
			names[name] = true
		}
	}
	items := []protocol.DiagnosticItem{}
	for name := range names {
		hash := sha256.Sum256([]byte(name))
		id := "integration:" + hex.EncodeToString(hash[:])
		if req.ID != "" && req.ID != id {
			continue
		}
		view := protocol.DiagnosticIntegration{ID: id, Name: clip(name), RegistrationCount: len(registrations[name]), CachedStatusCount: len(cached[name]), CachedStatus: "unknown", CapabilityEvidence: "not_registered", StatusEvidence: "unavailable", AccessEvidence: "unknown_not_checked", FreshnessEvidence: "unknown_cache_freshness_not_established", CollectionEvidence: "unknown_no_current_daemon_attempt"}
		switch view.RegistrationCount {
		case 0:
		case 1:
			view.Label = clip(registrations[name][0].Label)
			view.Capabilities = registrations[name][0].Capabilities
			view.CapabilityEvidence = "registered_interfaces_only"
		default:
			view.CapabilityEvidence = "ambiguous_registration"
		}
		switch view.CachedStatusCount {
		case 0:
		case 1:
			status := cached[name][0]
			view.StatusEvidence = "cached_installation_status"
			switch status.Status {
			case "ok", "partial", "error", "disabled":
				view.CachedStatus = status.Status
			}
			view.DetailsAvailable = status.Detail != ""
			// Native omitempty persistence cannot distinguish an absent count from
			// zero (including a skipped collection). Do not turn that into none.
			if req.Workspace == "" && status.SourceRefCount > 0 {
				count := status.SourceRefCount
				view.InstallationReportedRefCount = &count
			}
		default:
			view.StatusEvidence = "ambiguous_cached_status"
		}
		if attempt, ok := attempts[name]; ok {
			if view.RegistrationCount > 1 {
				view.CollectionEvidence = "ambiguous_registration"
			} else {
				copy := attempt
				view.CollectionEvidence, view.Collection = "current_daemon_completed_attempt", &copy
			}
		}
		var latest time.Time
		for _, ref := range scoped[name] {
			if ref.Active {
				view.ActiveRefs++
			} else {
				view.InactiveRefs++
			}
			timestamp, err := time.Parse(time.RFC3339Nano, ref.ObservedAt)
			if err != nil || timestamp.IsZero() {
				view.UnknownRefObservedAt++
				continue
			}
			if timestamp.After(latest) {
				latest = timestamp
			}
		}
		if !latest.IsZero() {
			view.LatestRefObservedAt = latest.UTC().Format(time.RFC3339Nano)
		}
		items = append(items, protocol.DiagnosticItem{ID: id, Integration: &view})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items, nil
}
