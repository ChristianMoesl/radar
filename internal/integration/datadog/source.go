package datadog

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"radar/internal/config"
	"radar/internal/integration"
	"radar/internal/protocol"
)

type Source struct {
	searcher monitorSearcher
}

func NewSource() Source {
	return Source{searcher: apiClient{}}
}

func (Source) Name() string {
	return "datadog"
}

func (Source) Status(_ context.Context, logger *slog.Logger) integration.StatusResult {
	status := protocol.SourceStatus{Name: "datadog", Status: "ok"}
	cfg, err := config.Load()
	if err != nil {
		status.Status = "error"
		status.Detail = "could not load config"
		return integration.StatusResult{Status: status, CanRun: false}
	}
	if strings.TrimSpace(cfg.Datadog.MonitorQuery) == "" {
		status.Status = "disabled"
		status.Detail = "missing datadog.monitor_query"
		return integration.StatusResult{Status: status, CanRun: false}
	}

	_, missing, err := credentialsFromEnv()
	if err != nil {
		logger.Debug("datadog collector configuration is invalid", "error", err)
		status.Status = "error"
		status.Detail = err.Error()
		return integration.StatusResult{Status: status, CanRun: false}
	}
	if len(missing) > 0 {
		status.Status = "disabled"
		status.Detail = "missing " + strings.Join(missing, ", ")
		return integration.StatusResult{Status: status, CanRun: false}
	}
	return integration.StatusResult{Status: status, CanRun: true}
}

func (s Source) Collect(ctx context.Context, req integration.CollectRequest) integration.CollectResult {
	status := protocol.SourceStatus{Name: "datadog", Status: "ok"}
	userConfig, err := config.Load()
	if err != nil {
		return failedCollection(req, status, "could not load config", err)
	}
	query := strings.TrimSpace(userConfig.Datadog.MonitorQuery)
	if query == "" {
		status.Status = "disabled"
		status.Detail = "missing datadog.monitor_query"
		return integration.CollectResult{SourceStatus: &status}
	}

	credentials, missing, err := credentialsFromEnv()
	if err != nil {
		return failedCollection(req, status, err.Error(), err)
	}
	if len(missing) > 0 {
		status.Status = "disabled"
		status.Detail = "missing " + strings.Join(missing, ", ")
		return integration.CollectResult{Observations: previousObservations(req.Previous), SourceStatus: &status}
	}

	searcher := s.searcher
	if searcher == nil {
		searcher = apiClient{}
	}
	response, err := searcher.Search(ctx, credentials, query)
	if err != nil {
		return failedCollection(req, status, "monitor search failed", err)
	}
	if response.Metadata.PageCount > 1 || response.Metadata.TotalCount > len(response.Monitors) {
		detail := fmt.Sprintf("query matched %d monitors; narrow datadog.monitor_query below %d", response.Metadata.TotalCount, monitorPageSize+1)
		return failedCollection(req, status, detail, fmt.Errorf("Datadog monitor search was truncated"))
	}

	sort.SliceStable(response.Monitors, func(i, j int) bool { return response.Monitors[i].ID < response.Monitors[j].ID })
	observations := make([]integration.Observation, 0, len(response.Monitors))
	for _, monitor := range response.Monitors {
		observation, ok := observationFromMonitor(credentials, monitor)
		if ok {
			observations = append(observations, observation)
		}
	}
	status.Detail = fmt.Sprintf("%d unhealthy monitors", len(observations))
	return integration.CollectResult{Observations: observations, Complete: true, SourceStatus: &status}
}

func (Source) ReconcileDone(_ context.Context, req integration.ReconcileRequest) []protocol.Task {
	if !req.Result.Complete {
		return nil
	}

	active := map[string]bool{}
	for _, task := range req.Active {
		for _, ref := range task.SourceRefs {
			if ref.Source == "datadog" && ref.Kind == "monitor" {
				active[ref.ID] = true
			}
		}
	}

	done := make([]protocol.Task, 0)
	seen := map[string]bool{}
	for _, task := range req.Previous {
		if task.Attention == "done" {
			continue
		}
		for _, ref := range task.SourceRefs {
			if ref.Source != "datadog" || ref.Kind != "monitor" || ref.ID == "" || active[ref.ID] || seen[ref.ID] {
				continue
			}
			seen[ref.ID] = true
			ref.Signal = string(integration.SignalDone)
			ref.Status = "Recovered"
			title := ref.Title
			if title == "" {
				title = task.Title
			}
			done = append(done, protocol.Task{
				Kind:       "datadog_monitor",
				Title:      title,
				URL:        ref.URL,
				Attention:  string(integration.SignalDone),
				Reason:     "Datadog monitor recovered",
				DoneAt:     time.Now().UTC().Format(time.RFC3339),
				SourceRefs: []protocol.SourceRef{ref},
			})
		}
	}
	return done
}

func observationFromMonitor(cfg credentials, monitor monitor) (integration.Observation, bool) {
	signal, reason, ok := signalForStatus(monitor.Status)
	if !ok || monitor.ID <= 0 {
		return integration.Observation{}, false
	}

	id := "datadog:monitor:" + strconv.FormatInt(monitor.ID, 10)
	metadata := map[string]string{"monitor_id": strconv.FormatInt(monitor.ID, 10)}
	if monitor.Priority != nil {
		metadata["priority"] = fmt.Sprint(*monitor.Priority)
	}
	if len(monitor.Tags) > 0 {
		metadata["tags"] = strings.Join(monitor.Tags, ", ")
	}
	if len(monitor.Scopes) > 0 {
		metadata["scopes"] = strings.Join(monitor.Scopes, ", ")
	}
	if monitor.LastTriggeredUnix > 0 {
		metadata["last_triggered_at"] = time.Unix(monitor.LastTriggeredUnix, 0).UTC().Format(time.RFC3339)
	}
	if monitor.OverallStateModified > 0 {
		metadata["state_changed_at"] = time.Unix(monitor.OverallStateModified, 0).UTC().Format(time.RFC3339)
	}

	title := strings.TrimSpace(monitor.Name)
	if title == "" {
		title = fmt.Sprintf("Datadog monitor %d", monitor.ID)
	}
	monitorURL := fmt.Sprintf("%s/monitors/%d", cfg.AppBaseURL, monitor.ID)
	return integration.Observation{
		Ref: protocol.SourceRef{
			ID:           id,
			Source:       "datadog",
			SourceLabel:  "Datadog",
			Kind:         "monitor",
			Title:        title,
			URL:          monitorURL,
			Status:       monitor.Status,
			CanonicalKey: id,
			Metadata:     metadata,
		},
		Signal: signal,
		Reason: reason,
	}, true
}

func signalForStatus(status string) (integration.WorkSignal, string, bool) {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "alert":
		return integration.SignalImmediate, "Datadog monitor is alerting", true
	case "warn":
		return integration.SignalAttention, "Datadog monitor is warning", true
	case "no data":
		return integration.SignalAttention, "Datadog monitor has no data", true
	default:
		return "", "", false
	}
}

func failedCollection(req integration.CollectRequest, status protocol.SourceStatus, detail string, err error) integration.CollectResult {
	status.Status = "error"
	status.Detail = detail
	if req.Logger != nil {
		req.Logger.Warn("datadog collection failed", "error", err)
	}
	return integration.CollectResult{Observations: previousObservations(req.Previous), SourceStatus: &status}
}

func previousObservations(tasks []protocol.Task) []integration.Observation {
	observations := make([]integration.Observation, 0)
	for _, task := range tasks {
		if task.Attention == "done" {
			continue
		}
		for _, ref := range task.SourceRefs {
			if ref.Source != "datadog" || ref.Kind != "monitor" {
				continue
			}
			signal, reason, ok := signalForStatus(ref.Status)
			if !ok {
				continue
			}
			observations = append(observations, integration.Observation{Ref: ref, Signal: signal, Reason: reason})
		}
	}
	return observations
}

var _ integration.Source = Source{}
var _ integration.StatusReporter = Source{}
var _ integration.Reconciler = Source{}
