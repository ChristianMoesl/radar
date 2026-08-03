package datadog

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"radar/internal/integration"
	"radar/internal/integration/contracttest"
	"radar/internal/protocol"
)

type fakeSearcher struct {
	response monitorSearchResponse
	err      error
	query    string
}

func (s *fakeSearcher) Search(_ context.Context, _ credentials, query string) (monitorSearchResponse, error) {
	s.query = query
	return s.response, s.err
}

func TestCollectProjectsUnhealthyMonitors(t *testing.T) {
	configureDatadog(t, "tag:team:cap")
	configureDatadogCredentials(t)
	priority := 1
	searcher := &fakeSearcher{response: monitorSearchResponse{Monitors: []monitor{
		{ID: 3, Name: "No heartbeat", Status: "No Data"},
		{ID: 1, Name: "API errors", Status: "Alert", Priority: &priority, Tags: []string{"team:cap"}, LastTriggeredUnix: 1_750_000_000},
		{ID: 2, Name: "Slow responses", Status: "Warn"},
		{ID: 4, Name: "Healthy", Status: "OK"},
	}}}

	result := (Source{searcher: searcher}).Collect(context.Background(), integration.CollectRequest{Logger: testLogger()})
	if !result.Complete {
		t.Fatal("collection is incomplete")
	}
	if searcher.query != "tag:team:cap" {
		t.Fatalf("query = %q", searcher.query)
	}
	if len(result.Observations) != 3 {
		t.Fatalf("observations = %d, want 3: %+v", len(result.Observations), result.Observations)
	}

	alert := result.Observations[0]
	if alert.Ref.ID != "datadog:monitor:1" || alert.Ref.CanonicalKey != alert.Ref.ID {
		t.Fatalf("alert identity = %+v", alert.Ref)
	}
	if alert.Ref.Source != "datadog" || alert.Ref.Kind != "monitor" || alert.Ref.SourceLabel != "Datadog" {
		t.Fatalf("alert source ref = %+v", alert.Ref)
	}
	if alert.Ref.URL != "https://app.datadoghq.eu/monitors/1" {
		t.Fatalf("alert URL = %q", alert.Ref.URL)
	}
	if alert.Signal != integration.SignalImmediate || alert.Reason != "Datadog monitor is alerting" {
		t.Fatalf("alert observation = %+v", alert)
	}
	if alert.Ref.Metadata["priority"] != "1" || alert.Ref.Metadata["tags"] != "team:cap" || alert.Ref.Metadata["last_triggered_at"] == "" {
		t.Fatalf("alert metadata = %+v", alert.Ref.Metadata)
	}
	if result.Observations[1].Signal != integration.SignalAttention || result.Observations[2].Signal != integration.SignalAttention {
		t.Fatalf("non-alert signals = %+v", result.Observations)
	}
	if result.SourceStatus == nil || result.SourceStatus.Detail != "3 unhealthy monitors" {
		t.Fatalf("source status = %+v", result.SourceStatus)
	}
}

func TestMonitorSourceRefContract(t *testing.T) {
	observation, ok := observationFromMonitor(credentials{AppBaseURL: "https://app.datadoghq.eu"}, monitor{ID: 42, Name: "API errors", Status: "Alert"})
	if !ok {
		t.Fatal("observation was rejected")
	}
	contracttest.AssertValidSourceRefs(t, "datadog", []protocol.SourceRef{observation.Ref})
}

func TestCollectPreservesPreviousAlertsWhenSearchFails(t *testing.T) {
	configureDatadog(t, "tag:team:cap")
	configureDatadogCredentials(t)
	searcher := &fakeSearcher{err: errors.New("network unavailable")}
	previous := []protocol.Task{{
		ID: 8, Title: "API errors", Attention: "immediate",
		SourceRefs: []protocol.SourceRef{{ID: "datadog:monitor:1", Source: "datadog", Kind: "monitor", Status: "Alert"}},
	}}

	result := (Source{searcher: searcher}).Collect(context.Background(), integration.CollectRequest{Previous: previous, Logger: testLogger()})
	if result.Complete {
		t.Fatal("failed collection is complete")
	}
	if len(result.Observations) != 1 || result.Observations[0].Ref.ID != "datadog:monitor:1" {
		t.Fatalf("preserved observations = %+v", result.Observations)
	}
	if result.SourceStatus == nil || result.SourceStatus.Status != "error" {
		t.Fatalf("source status = %+v", result.SourceStatus)
	}
}

func TestCollectRejectsTruncatedMonitorSearch(t *testing.T) {
	configureDatadog(t, "tag:team:cap")
	configureDatadogCredentials(t)
	response := monitorSearchResponse{Monitors: make([]monitor, monitorPageSize)}
	response.Metadata.TotalCount = monitorPageSize + 1
	response.Metadata.PageCount = 2

	result := (Source{searcher: &fakeSearcher{response: response}}).Collect(context.Background(), integration.CollectRequest{Logger: testLogger()})
	if result.Complete || result.SourceStatus == nil || result.SourceStatus.Status != "error" {
		t.Fatalf("result = %+v", result)
	}
}

func TestStatusRequiresQueryAndEnvironmentCredentials(t *testing.T) {
	configureDatadog(t, "")
	t.Setenv("RADAR_DATADOG_API_KEY", "")
	t.Setenv("RADAR_DATADOG_APP_KEY", "")
	status := NewSource().Status(context.Background(), testLogger())
	if status.CanRun || status.Status.Status != "disabled" || status.Status.Detail != "missing datadog.monitor_query" {
		t.Fatalf("status without query = %+v", status)
	}

	configureDatadog(t, "tag:team:cap")
	status = NewSource().Status(context.Background(), testLogger())
	if status.CanRun || status.Status.Detail != "missing RADAR_DATADOG_API_KEY, RADAR_DATADOG_APP_KEY" {
		t.Fatalf("status without credentials = %+v", status)
	}

	configureDatadogCredentials(t)
	status = NewSource().Status(context.Background(), testLogger())
	if !status.CanRun || status.Status.Status != "ok" {
		t.Fatalf("configured status = %+v", status)
	}
}

func TestReconcileMarksDisappearedMonitorDone(t *testing.T) {
	previous := []protocol.Task{{
		ID: 9, Title: "API errors", URL: "https://app.datadoghq.eu/monitors/42", Attention: "immediate",
		SourceRefs: []protocol.SourceRef{{
			ID: "datadog:monitor:42", Source: "datadog", SourceLabel: "Datadog", Kind: "monitor", Title: "API errors",
			URL: "https://app.datadoghq.eu/monitors/42", Status: "Alert", Signal: "immediate", CanonicalKey: "datadog:monitor:42",
		}},
	}}

	done := NewSource().Reconcile(context.Background(), integration.ReconcileRequest{Previous: previous, Result: integration.CollectResult{Complete: true}})
	if len(done) != 1 {
		t.Fatalf("done = %+v", done)
	}
	if done[0].Signal != integration.SignalDone || done[0].Reason != "Datadog monitor recovered" || done[0].Ref.Status != "Recovered" {
		t.Fatalf("done observation = %+v", done[0])
	}
}

func configureDatadog(t *testing.T, query string) {
	t.Helper()
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	path := filepath.Join(configHome, "radar", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"linking_mark_prefixes":["RAD"],"datadog":{"monitor_query":` + strconvQuote(query) + `}}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func configureDatadogCredentials(t *testing.T) {
	t.Helper()
	t.Setenv("RADAR_DATADOG_API_KEY", "api-secret")
	t.Setenv("RADAR_DATADOG_APP_KEY", "app-secret")
	t.Setenv("RADAR_DATADOG_SITE", "datadoghq.eu")
}

func strconvQuote(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
