package datadog

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/setup"
)

func TestOAuthFailureRetentionAndForegroundReconnection(t *testing.T) {
	configureDatadog(t, "tag:team:platform")
	configureDatadogCLI(t)
	for _, scenario := range []string{"expired", "missing refresh", "missing session", "status unavailable"} {
		t.Run(scenario, func(t *testing.T) {
			previous := []protocol.Task{{ID: 8, Attention: "immediate", SourceRefs: []protocol.SourceRef{{
				ID: "datadog:monitor:42", Source: "datadog", Kind: "monitor", Status: "Alert", Title: "API errors",
			}}}}
			reconnected, healthy, reads := false, false, 0
			var diagnostic bytes.Buffer
			source := Source{searcher: apiClient{output: func(_ context.Context, c setup.Command) (string, error) {
				args := strings.Join(c.Args, " ")
				if args == "--version" {
					return "pup 2.0.0", nil
				}
				if strings.Contains(args, "auth status") {
					if reconnected {
						return oauthStatus, nil
					}
					switch scenario {
					case "expired":
						return strings.Replace(oauthStatus, `"valid"`, `"expired"`, 1), nil
					case "missing refresh":
						return strings.Replace(oauthStatus, `"has_refresh":true`, `"has_refresh":false`, 1), nil
					case "missing session":
						return `{"authenticated":false,"status":"no token"}`, nil
					default:
						return "private-fixture", errors.New("private-fixture")
					}
				}
				if !strings.Contains(args, "api /api/v1/monitor/search") {
					t.Fatalf("background collection attempted unexpected command: %s", args)
				}
				reads++
				if healthy {
					return `{"metadata":{"total_count":0,"page_count":0},"monitors":[]}`, nil
				}
				return `{"metadata":{"total_count":1,"page_count":1},"monitors":[{"id":42,"name":"API errors","status":"Alert"}]}`, nil
			}}}
			req := integration.CollectRequest{Previous: previous, Logger: slog.New(slog.NewTextHandler(&diagnostic, nil))}
			result := source.Collect(context.Background(), req)
			if result.Complete || reads != 0 || len(result.Observations) != 1 || result.Observations[0].Ref.ID != "datadog:monitor:42" || result.Observations[0].Ref.Status != "Alert" || result.SourceStatus == nil || result.SourceStatus.Status != "error" {
				t.Fatalf("authentication failure lost facts or dispatched an API request: %+v", result)
			}
			if !strings.Contains(result.SourceStatus.Detail, "foreground") || strings.Contains(result.SourceStatus.Detail+diagnostic.String(), "private-fixture") {
				t.Fatalf("missing safe foreground recovery guidance: %s", result.SourceStatus.Detail)
			}
			if scenario == "expired" && !strings.Contains(result.SourceStatus.Detail, "could not silently renew") {
				t.Fatal("access-token expiry was not distinguished from a missing grant")
			}
			if len(source.Reconcile(context.Background(), integration.ReconcileRequest{Previous: previous, Result: result})) != 0 {
				t.Fatal("failed authentication recovered an active alert")
			}
			// Stand in for the user completing foreground setup. Collection
			// reuses the same source and task identity without a daemon restart.
			reconnected = true
			result = source.Collect(context.Background(), req)
			if !result.Complete || reads != 1 || result.SourceStatus.Status != "ok" || len(result.Observations) != 1 || result.Observations[0].Ref.ID != previous[0].SourceRefs[0].ID {
				t.Fatalf("collection did not resume after reconnection: %+v", result)
			}
			healthy = true
			result = source.Collect(context.Background(), req)
			if !result.Complete || len(source.Reconcile(context.Background(), integration.ReconcileRequest{Previous: previous, Result: result})) != 1 {
				t.Fatal("successful complete refresh did not recover the old alert")
			}
		})
	}
}
