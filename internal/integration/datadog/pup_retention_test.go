package datadog

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"radar/internal/command"
	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/setup"
)

// Exercise the released CLI and production collection together. Only fresh
// synthetic tokens enter a loopback API fixture; no OAuth endpoint is called.
func TestReleasedPupFailuresRetainMonitorFacts(t *testing.T) {
	binary := os.Getenv("RADAR_TEST_PUP")
	if binary == "" {
		t.Skip("set RADAR_TEST_PUP to the verified Pup 2.0.0 binary")
	}
	for _, scenario := range []string{"forbidden", "rate limited", "malformed", "truncated", "canceled", "deadline"} {
		t.Run(scenario, func(t *testing.T) {
			configureDatadog(t, "tag:team:platform")
			configureDatadogCLI(t)
			directory := t.TempDir()
			t.Setenv("PUP_CONFIG_DIR", directory)
			t.Setenv("DD_TOKEN_STORAGE", "file")
			tokens, err := json.Marshal(map[string]any{"radar": map[string]any{
				"access_token": "oauth-fixture", "refresh_token": "refresh-fixture",
				"token_type": "Bearer", "expires_in": 3600, "issued_at": time.Now().Unix(), "scope": "monitors_read",
			}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, "tokens_datadoghq_eu.json"), tokens, 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			var requested atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requested.Store(true)
				if r.Header.Get("Authorization") != "Bearer oauth-fixture" {
					t.Error("fixture did not receive the named OAuth session")
				}
				w.Header().Set("Content-Type", "application/json")
				switch scenario {
				case "forbidden":
					w.WriteHeader(http.StatusForbidden)
					_, _ = w.Write([]byte(`{"errors":["private-provider-diagnostic"]}`))
				case "rate limited":
					w.Header().Set("Retry-After", "600")
					w.WriteHeader(http.StatusTooManyRequests)
					_, _ = w.Write([]byte(`{"errors":["private-provider-diagnostic"]}`))
				case "malformed":
					_, _ = w.Write([]byte(`private-provider-diagnostic`))
				case "truncated":
					_, _ = w.Write([]byte(`{"metadata":{"total_count":2,"page_count":2},"monitors":[{"id":42,"name":"API errors","status":"Alert"}]}`))
				case "canceled":
					cancel()
					<-r.Context().Done()
				case "deadline":
					<-r.Context().Done()
				}
			}))
			defer server.Close()
			output := func(ctx context.Context, c setup.Command) (string, error) {
				data, err := command.Output(ctx, binary, c.Args, append(c.Env, "PUP_MOCK_SERVER="+server.URL))
				return string(data), err
			}
			previous := []protocol.Task{{ID: 8, Attention: "immediate", SourceRefs: []protocol.SourceRef{{
				ID: "datadog:monitor:42", Source: "datadog", Kind: "monitor", Status: "Alert", Title: "API errors",
			}}}}
			var diagnostic bytes.Buffer
			result := (Source{searcher: apiClient{output: output}}).Collect(ctx, integration.CollectRequest{
				Previous: previous, Logger: slog.New(slog.NewTextHandler(&diagnostic, &slog.HandlerOptions{Level: slog.LevelDebug})),
			})
			if !requested.Load() || result.Complete || result.SourceStatus == nil || result.SourceStatus.Status != "error" {
				t.Fatalf("requested=%v complete=%v status=%+v", requested.Load(), result.Complete, result.SourceStatus)
			}
			if len(result.Observations) != 1 || result.Observations[0].Ref.ID != "datadog:monitor:42" || result.Observations[0].Ref.Status != "Alert" {
				t.Fatalf("previous alert was lost: %+v", result.Observations)
			}
			if len((Source{}).Reconcile(ctx, integration.ReconcileRequest{Previous: previous, Result: result})) != 0 {
				t.Fatal("an unsuccessful CLI search recovered an existing alert")
			}
			if strings.Contains(diagnostic.String()+result.SourceStatus.Detail, "private-provider-diagnostic") {
				t.Fatal("provider diagnostics escaped into collection status/logs")
			}
		})
	}
}

func TestCollectRejectsUnscopedMonitorRequestBeforeCLI(t *testing.T) {
	configureDatadog(t, " \t ")
	configureDatadogCLI(t)
	called := false
	source := Source{searcher: apiClient{output: func(context.Context, setup.Command) (string, error) {
		called = true
		t.Fatal("unscoped collection invoked a provider CLI")
		return "", nil
	}}}
	result := source.Collect(context.Background(), integration.CollectRequest{Logger: testLogger()})
	if called || result.Complete || result.SourceStatus == nil || result.SourceStatus.Detail != "missing datadog.monitor_query" {
		t.Fatalf("unscoped search accepted: %+v", result)
	}
}
