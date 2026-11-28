package terraform

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/setup"
)

func TestTerraformReadOperationFailurePrivacy(t *testing.T) {
	for _, endpoint := range []struct{ name, path, operation string }{
		{"account", "/account/details?private-query=private-value", "account"},
		{"workspace", "/organizations/private-org/workspaces/private-space", "workspace"},
		{"workspace_list", "/organizations/private-org/workspaces?page%5Bnumber%5D=2", "workspace list"},
		{"run_list", "/workspaces/ws-private/runs?page%5Bnumber%5D=2", "run list"},
		{"run", "/runs/run-private?include=private-value", "run"},
		{"plan", "/plans/plan-private", "plan"},
		{"ingress", "/configuration-versions/cv-private/ingress-attributes", "ingress"},
		{"unknown", "/private-endpoint/private-value?token=private-value", "API"},
	} {
		t.Run(endpoint.name, func(t *testing.T) {
			for _, outcome := range []string{"exit", "timeout", "canceled", "invalid_json", "api_errors", "missing_data", "healthy"} {
				t.Run(outcome, func(t *testing.T) {
					const private = "private-cli-body https://private-host/private-value token=private-token HTTP 403"
					calls := 0
					c := client{profile: "private-profile", output: func(_ context.Context, command setup.Command) (string, error) {
						calls++
						want := []string{"api", endpoint.path, "--method=GET", "--profile=private-profile", "--json", "--no-color"}
						if command.Name != "tfctl" || !reflect.DeepEqual(command.Args, want) || command.CaptureStderr {
							t.Fatal("read evidence changed native request or captured subprocess diagnostics")
						}
						switch outcome {
						case "exit":
							return private, errors.New(private)
						case "timeout":
							return private, fmt.Errorf("%s: %w", private, context.DeadlineExceeded)
						case "canceled":
							return private, fmt.Errorf("%s: %w", private, context.Canceled)
						case "invalid_json":
							return private, nil
						case "api_errors":
							return encode(map[string]any{"data": []any{}, "errors": []any{map[string]any{"status": "403", "title": private}}}), nil
						case "missing_data":
							return encode(map[string]any{"meta": private}), nil
						default:
							return `{"data":[]}`, nil
						}
					}}
					doc, err := c.get(context.Background(), endpoint.path)
					if calls != 1 {
						t.Fatal("read evidence introduced replay or fallback", calls)
					}
					if outcome == "healthy" {
						if err != nil || string(doc.Data) != "[]" {
							t.Fatal("healthy read changed", err)
						}
						return
					}
					want := "Terraform " + endpoint.operation + " read failed; check tfctl authentication, access and connectivity"
					switch outcome {
					case "timeout":
						want = "tfctl " + endpoint.operation + " read timed out: context deadline exceeded"
					case "canceled":
						want = "tfctl " + endpoint.operation + " read canceled: context canceled"
					case "invalid_json", "api_errors", "missing_data":
						want = "tfctl returned an invalid API response (" + endpoint.operation + " read)"
					}
					if err == nil || err.Error() != want || len(err.Error()) > 160 || strings.Contains(fmt.Sprintf("%+v", err), "private") || strings.Contains(err.Error(), "403") {
						t.Fatal("read failure lost its finite operation or exposed private/native cause", err)
					}
					if errors.Is(err, context.DeadlineExceeded) != (outcome == "timeout") || errors.Is(err, context.Canceled) != (outcome == "canceled") {
						t.Fatal("operation label changed cancellation/timeout identity", err)
					}
				})
			}
		})
	}
}

func TestTerraformReadOperationInvalidPathNeverDispatches(t *testing.T) {
	c := client{output: func(context.Context, setup.Command) (string, error) {
		t.Fatal("invalid path dispatched")
		return "", nil
	}}
	for _, path := range []string{"", "private-relative", "//private-host/private", "https://private-host/private"} {
		if _, err := c.get(context.Background(), path); err == nil || err.Error() != "invalid Terraform API path" {
			t.Fatal("invalid path handling changed", err)
		}
	}
}

func TestTerraformReadOperationCollectionRetainsKnownFacts(t *testing.T) {
	for _, operation := range []string{"account", "run list", "run"} {
		t.Run(operation, func(t *testing.T) {
			run := runFixture("run-one", "planning", "user-me")
			s, api, _ := fixtureSource(t, run)
			api.direct[run.ID] = run
			first := s.Collect(context.Background(), integration.CollectRequest{})
			if !first.Complete || len(first.Observations) != 1 {
				t.Fatal("healthy fixture unavailable")
			}
			if operation == "run" {
				api.runs = nil // Force the ordinary tracked-ID read, not disappearance-based completion.
			}
			calls := 0
			s.output = func(ctx context.Context, command setup.Command) (string, error) {
				if command.Args[0] == "api" {
					u, _ := url.Parse(command.Args[1])
					if (operation == "account" && u.Path == "/account/details") ||
						(operation == "run list" && u.Path == "/workspaces/ws-one/runs") ||
						(operation == "run" && u.Path == "/runs/run-one") {
						calls++
						return "private-response", errors.New("private-error HTTP 401 token=private-token")
					}
				}
				return api.output(ctx, command)
			}
			result := s.Collect(context.Background(), integration.CollectRequest{Previous: tasksFrom(first)})
			wantStatus := "partial"
			if operation == "account" {
				wantStatus = "error"
			}
			if result.Complete || result.SourceStatus == nil || result.SourceStatus.Status != wantStatus || calls != 1 ||
				!strings.Contains(result.SourceStatus.Detail, "Terraform "+operation+" read failed;") ||
				strings.Contains(encode(result), "private-") || len(result.Observations) != 1 ||
				!reflect.DeepEqual(result.Observations[0].Ref, first.Observations[0].Ref) {
				t.Fatal("finite operation evidence changed partial/error retention, privacy or retries", result.SourceStatus, calls)
			}
		})
	}
}

func TestTerraformReadOperationColdBindingRefusesUnknownOutcome(t *testing.T) {
	run := runFixture("run-one", "planning", "user-me")
	s, api, _ := fixtureSource(t, run)
	s.output = func(ctx context.Context, command setup.Command) (string, error) {
		if command.Args[0] == "api" && strings.HasPrefix(command.Args[1], "/runs/") {
			return "private-response", errors.New("private-error")
		}
		return api.output(ctx, command)
	}
	result := s.ResolveBindings(context.Background(), integration.BindingRequest{Bindings: []protocol.SourceBinding{{Source: "terraform", Kind: "run", ID: runIdentity("app.terraform.io", run.ID), WorkItem: true}}})
	if result.Complete || len(result.Observations) != 0 || result.SourceStatus == nil ||
		result.SourceStatus.Detail != "Terraform run read failed; check tfctl authentication, access and connectivity" || strings.Contains(encode(result), "private-") {
		t.Fatal("cold binding invented a run outcome or lost safe failure operation", result.SourceStatus)
	}
}
