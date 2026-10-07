package datadog

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"radar/internal/integration"
	"radar/internal/integration/contracttest"
	"radar/internal/protocol"
)

type bindingSearcher struct {
	fakeSearcher
	calls int
}

func (s *bindingSearcher) Search(ctx context.Context, cfg credentials, query string, statuses []string) (monitorSearchResponse, error) {
	s.calls++
	return s.fakeSearcher.Search(ctx, cfg, query, statuses)
}

func TestResolveBindingsUsesCompleteMonitorSearchForActiveAndColdRecovery(t *testing.T) {
	for _, active := range []bool{true, false} {
		t.Run(map[bool]string{true: "active", false: "recovered while stopped"}[active], func(t *testing.T) {
			configureDatadog(t, "tag:team:example")
			configureDatadogCredentials(t)
			searcher := &bindingSearcher{}
			if active {
				searcher.response.Monitors = []monitor{{ID: 7, Name: "API errors", Status: "Alert"}}
			}
			source := Source{searcher: searcher}
			req := boundDatadogRequest()
			req.Result = source.Collect(context.Background(), integration.CollectRequest{Logger: req.Logger})
			result := source.ResolveBindings(context.Background(), req)
			if !result.Complete || searcher.calls != 1 {
				t.Fatalf("result=%+v calls=%d", result, searcher.calls)
			}
			if active {
				if len(result.Observations) != 0 || len(req.Result.Observations) != 1 {
					t.Fatalf("fresh observation duplicated: %+v", result)
				}
				return
			}
			if len(result.Observations) != 1 {
				t.Fatalf("cold recovery = %+v", result)
			}
			got := result.Observations[0]
			contracttest.AssertValidSourceRefs(t, "datadog", []protocol.SourceRef{got.Ref})
			if got.Signal != integration.SignalDone || got.Ref.Signal != "done" || got.Ref.Status != "Recovered" || got.Ref.Title != "Datadog monitor 7" || got.Ref.URL != "https://app.datadoghq.eu/monitors/7" || got.Ref.Metadata["monitor_id"] != "7" {
				t.Fatalf("recovered = %+v", got)
			}
		})
	}
}

func TestResolveBindingsNeverRecoversFromIncompleteErrorOrDisabledSearch(t *testing.T) {
	for _, name := range []string{"incomplete", "error", "truncated", "disabled", "no query", "missing credentials", "invalid site"} {
		t.Run(name, func(t *testing.T) {
			configureDatadog(t, "tag:team:example")
			configureDatadogCredentials(t)
			searcher := &bindingSearcher{}
			switch name {
			case "error":
				searcher.err = errors.New("unavailable")
			case "truncated":
				searcher.response.Metadata.PageCount = 2
			case "disabled":
				path := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "radar", "config.yaml")
				if err := os.WriteFile(path, []byte(`linking_mark_prefixes:
  - ABC
datadog:
  enabled: false
  monitor_query: tag:team:example`), 0o600); err != nil {
					t.Fatal(err)
				}
			case "no query":
				configureDatadog(t, "")
			case "missing credentials":
				t.Setenv("RADAR_DATADOG_APP_KEY", "")
			case "invalid site":
				t.Setenv("RADAR_DATADOG_SITE", "invalid.example.test")
			}
			source := Source{searcher: searcher}
			req := boundDatadogRequest()
			req.Result = source.Collect(context.Background(), integration.CollectRequest{Logger: req.Logger})
			if name == "incomplete" {
				req.Result.Complete = false
			}
			result := source.ResolveBindings(context.Background(), req)
			if result.Complete || len(result.Observations) != 0 {
				t.Fatalf("unsupported recovery = %+v", result)
			}
			// Even contradictory caller evidence cannot turn an error/disabled
			// source status into a complete configured search.
			req.Result.Complete = true
			req.Result.SourceStatus = &protocol.SourceStatus{Name: "datadog", Status: "error"}
			result = source.ResolveBindings(context.Background(), req)
			if result.Complete || len(result.Observations) != 0 {
				t.Fatalf("error status recovery = %+v", result)
			}
		})
	}
}

func TestResolveBindingsPreservesConfiguredMonitorQueryAndStatusFilters(t *testing.T) {
	configureDatadogWithStatuses(t, "tag:team:example", []string{"Warn"})
	configureDatadogCredentials(t)
	searcher := &bindingSearcher{fakeSearcher: fakeSearcher{response: monitorSearchResponse{Monitors: []monitor{{ID: 7, Name: "API errors", Status: "Alert"}, {ID: 8, Name: "Latency", Status: "Warn"}}}}}
	source := Source{searcher: searcher}
	req := boundDatadogRequest()
	req.Result = source.Collect(context.Background(), integration.CollectRequest{Logger: req.Logger})
	result := source.ResolveBindings(context.Background(), req)
	if !result.Complete || len(result.Observations) != 1 || result.Observations[0].Signal != integration.SignalDone || searcher.calls != 1 {
		t.Fatalf("filtered recovery = %+v; calls=%d", result, searcher.calls)
	}
	if searcher.query != "tag:team:example" || !reflect.DeepEqual(searcher.statuses, []string{"Warn"}) || len(req.Result.Observations) != 1 || req.Result.Observations[0].Ref.ID != "datadog:monitor:8" {
		t.Fatalf("binding broadened filters: query=%q statuses=%v result=%+v", searcher.query, searcher.statuses, req.Result)
	}
}

func TestResolveBindingsReusesCachedMonitorFactsIncludingHistoricalTerminalRefs(t *testing.T) {
	configureDatadog(t, "tag:team:example")
	configureDatadogCredentials(t)
	original, ok := observationFromMonitor(credentials{AppBaseURL: "https://app.datadoghq.eu"}, []string{"Alert"}, monitor{ID: 7, Name: "Actual monitor name", Status: "Alert", Tags: []string{"team:example"}})
	if !ok {
		t.Fatal("fixture rejected")
	}
	for _, terminal := range []bool{false, true} {
		ref := original.Ref
		if terminal {
			ref.Signal, ref.Status = "done", "Recovered"
		}
		req := boundDatadogRequest()
		req.Previous = []protocol.Task{{Attention: "done", DoneAt: "2020-01-01T10:00:00Z", SourceRefs: []protocol.SourceRef{ref}}}
		result := NewSource().ResolveBindings(context.Background(), req)
		if !result.Complete || len(result.Observations) != 1 {
			t.Fatalf("result = %+v", result)
		}
		want := ref
		want.Signal, want.Status = "done", "Recovered"
		if !reflect.DeepEqual(result.Observations[0].Ref, want) {
			t.Fatalf("cached facts replaced: %+v want %+v", result.Observations[0].Ref, want)
		}
		// Search failure cannot create or republish recovered observations.
		req.Result.Complete = false
		if result := NewSource().ResolveBindings(context.Background(), req); result.Complete || len(result.Observations) != 0 {
			t.Fatalf("incomplete cached recovery = %+v", result)
		}
	}
}

func TestResolveBindingsValidatesMonitorBindingsAndDeduplicatesRecovery(t *testing.T) {
	configureDatadog(t, "tag:team:example")
	configureDatadogCredentials(t)
	for _, id := range []string{"datadog:monitor:0", "datadog:monitor:-7", "datadog:monitor:+7", "datadog:monitor:07", "datadog:monitor:7/path", "datadog:monitor:7?x", "datadog:monitor:9223372036854775808", "jira:issue:ABC-7"} {
		req := boundDatadogRequest()
		req.Bindings[0].ID = id
		if result := NewSource().ResolveBindings(context.Background(), req); result.Complete || len(result.Observations) != 0 || result.SourceStatus == nil || result.SourceStatus.Status != "error" {
			t.Fatalf("invalid %q accepted: %+v", id, result)
		}
	}
	for _, field := range []string{"source", "kind", "key", "work_item"} {
		req := boundDatadogRequest()
		switch field {
		case "source":
			req.Bindings[0].Source = "github"
		case "kind":
			req.Bindings[0].Kind = "pull_request"
		case "key":
			req.Bindings[0].Key = "lifetime"
		case "work_item":
			req.Bindings[0].WorkItem = false
		}
		if result := NewSource().ResolveBindings(context.Background(), req); result.Complete || len(result.Observations) != 0 {
			t.Fatalf("invalid %s accepted: %+v", field, result)
		}
	}
	req := boundDatadogRequest()
	req.Bindings = append(req.Bindings, req.Bindings[0])
	if result := NewSource().ResolveBindings(context.Background(), req); !result.Complete || len(result.Observations) != 1 {
		t.Fatalf("duplicates = %+v", result)
	}
}

func boundDatadogRequest() integration.BindingRequest {
	return integration.BindingRequest{Bindings: []protocol.SourceBinding{{Source: "datadog", Kind: "monitor", ID: "datadog:monitor:7", WorkItem: true}}, Result: integration.CollectResult{Complete: true, SourceStatus: &protocol.SourceStatus{Name: "datadog", Status: "ok"}}, Logger: testLogger()}
}
