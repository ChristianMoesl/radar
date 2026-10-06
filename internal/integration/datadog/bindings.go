package datadog

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"radar/internal/config"
	"radar/internal/integration"
	"radar/internal/protocol"
)

func (Source) ResolveBindings(_ context.Context, req integration.BindingRequest) integration.CollectResult {
	result := integration.CollectResult{Complete: true}
	if len(req.Bindings) == 0 {
		return result
	}
	cfg, err := config.Load()
	if err != nil {
		datadogBindingFailure(&result, req, err)
		return result
	}
	if availability := integration.OptionalStatus("datadog", cfg.Datadog.Enabled, ""); !availability.CanRun {
		return integration.CollectResult{SourceStatus: &availability.Status}
	}
	if strings.TrimSpace(cfg.Datadog.MonitorQuery) == "" {
		status := integration.OptionalStatus("datadog", cfg.Datadog.Enabled, "missing datadog.monitor_query").Status
		return integration.CollectResult{SourceStatus: &status}
	}
	credentials, missing, err := loadCredentials()
	if err != nil {
		datadogBindingFailure(&result, req, err)
		return result
	}
	if len(missing) > 0 {
		status := integration.OptionalStatus("datadog", cfg.Datadog.Enabled, "missing "+strings.Join(missing, ", ")).Status
		return integration.CollectResult{SourceStatus: &status}
	}
	// Complete configured unhealthy-search absence is the existing Datadog
	// recovery evidence. A failed, disabled or truncated search proves nothing,
	// even if the bound monitor has disappeared from its partial observations.
	if !req.Result.Complete || (req.Result.SourceStatus != nil && req.Result.SourceStatus.Status != "ok") {
		return integration.CollectResult{}
	}

	seen := map[string]bool{}
	for _, binding := range req.Bindings {
		id, ok := boundMonitorID(binding)
		if !ok {
			datadogBindingFailure(&result, req, fmt.Errorf("invalid monitor binding %q", binding.ID))
			continue
		}
		if seen[binding.ID] {
			continue
		}
		seen[binding.ID] = true
		if observedBoundMonitor(req.Result.Observations, binding.ID) {
			continue
		}
		ref, found := cachedBoundMonitor(req.Previous, binding.ID)
		if !found {
			// This is not a guessed monitor status: full configured search has
			// proved it is no longer in the tracked unhealthy set. After a cache
			// reset only its provider-owned identity and site are reconstructible.
			ref = protocol.SourceRef{
				ID: binding.ID, Source: "datadog", SourceLabel: "Datadog", Kind: "monitor",
				Role: protocol.SourceRefRoleAuthoritative, EntityID: binding.ID,
				Lifecycle: protocol.SourceRefLifecycleWorkItem, Authority: protocol.SourceRefAuthorityContributing,
				RetainInactive: true, CanonicalKey: binding.ID,
				Title: fmt.Sprintf("Datadog monitor %d", id), URL: fmt.Sprintf("%s/monitors/%d", credentials.AppBaseURL, id),
				Metadata: map[string]string{"monitor_id": strconv.FormatInt(id, 10)},
			}
		}
		ref.Signal = string(integration.SignalDone)
		ref.Status = "Recovered"
		result.Observations = append(result.Observations, integration.Observation{Ref: ref, Signal: integration.SignalDone, Reason: "Datadog monitor recovered"})
	}
	return result
}

func boundMonitorID(binding protocol.SourceBinding) (int64, bool) {
	if binding.Source != "datadog" || binding.Kind != "monitor" || binding.Key != "" || !binding.WorkItem {
		return 0, false
	}
	value, ok := strings.CutPrefix(binding.ID, "datadog:monitor:")
	id, err := strconv.ParseInt(value, 10, 64)
	return id, ok && err == nil && id > 0 && strconv.FormatInt(id, 10) == value
}

func contributingBoundMonitor(ref protocol.SourceRef) bool {
	return ref.Source == "datadog" && ref.Kind == "monitor" && ref.Role == protocol.SourceRefRoleAuthoritative &&
		ref.Lifecycle == protocol.SourceRefLifecycleWorkItem && ref.Authority == protocol.SourceRefAuthorityContributing
}

func observedBoundMonitor(observations []integration.Observation, id string) bool {
	for _, observation := range observations {
		if observation.Ref.ID == id && contributingBoundMonitor(observation.Ref) {
			return true
		}
	}
	return false
}

func cachedBoundMonitor(tasks []protocol.Task, id string) (protocol.SourceRef, bool) {
	for _, task := range tasks {
		for _, ref := range task.SourceRefs {
			if ref.ID == id && contributingBoundMonitor(ref) {
				return ref, true
			}
		}
	}
	return protocol.SourceRef{}, false
}

func datadogBindingFailure(result *integration.CollectResult, req integration.BindingRequest, err error) {
	result.Complete = false
	status := protocol.SourceStatus{Name: "datadog", Status: "error", Detail: "bound monitor resolution failed: " + err.Error()}
	result.SourceStatus = &status
	if req.Logger != nil {
		req.Logger.Warn("datadog bound monitor resolution failed", "error", err)
	}
}

var _ integration.BoundSourceResolver = Source{}
