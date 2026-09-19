package app_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/app"
	"radar/internal/collector"
	"radar/internal/config"
	"radar/internal/integration"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

// These tests must never discover the developer's tools, credentials or state.
func isolatedSetup(t *testing.T) string {
	t.Helper()
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "RADAR_") || strings.HasPrefix(key, "GH_") || key == "GITHUB_TOKEN" {
			t.Setenv(key, "")
		}
	}
	home := t.TempDir()
	for key, value := range map[string]string{
		"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, "config"),
		"XDG_DATA_HOME": filepath.Join(home, "data"), "XDG_STATE_HOME": filepath.Join(home, "state"),
		"PATH": filepath.Join(home, "bin"), "TMUX": "",
	} {
		t.Setenv(key, value)
	}
	if err := os.MkdirAll(os.Getenv("PATH"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RADAR_TEST_COMMAND_LOG", filepath.Join(home, "commands"))
	return home
}

func setupConfig(t *testing.T, value string) {
	t.Helper()
	path, err := config.EnsureFile()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
}

func setupTool(t *testing.T, name, body string) {
	t.Helper()
	script := "#!/bin/sh\nprintf '%s\\n' '" + name + " '" + `"$*" >> "$RADAR_TEST_COMMAND_LOG"` + "\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(os.Getenv("PATH"), name), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
}

const githubTool = `case "$1 $2" in
 "auth token") printf 'fixture-token\n'; exit 0;;
 "api rate_limit") printf '%s\n' '{"resources":{"core":{"limit":5000,"remaining":4999},"search":{"limit":30,"remaining":30},"graphql":{"limit":5000,"remaining":4999}}}'; exit 0;;
 *) exit 99;;
esac`

func TestFreshInstallCollectsWithoutAnyIntegrations(t *testing.T) {
	isolatedSetup(t)
	path, err := config.EnsureFile()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(); err != nil {
		t.Fatalf("first-run config cannot load: %v", err)
	}
	result := collector.Collect(context.Background(), nil, slog.New(slog.NewTextHandler(io.Discard, nil)), app.DefaultIntegrations().Sources())
	if len(result.Tasks) != 0 {
		t.Fatalf("unexpected tasks: %+v", result.Tasks)
	}
	for _, status := range result.Sources {
		want := "disabled"
		if status.Name == "workspace" {
			want = "ok"
		}
		if status.Status != want {
			t.Errorf("%s: %+v, want %s", status.Name, status, want)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"enabled"`) {
		t.Fatalf("generated config freezes detection: %s", data)
	}
}

func TestOptionalIntegrationActivationMatrix(t *testing.T) {
	for _, name := range []string{"github", "jira", "datadog", "sbx"} {
		for _, scenario := range []struct {
			name, enabled, setup, want string
		}{
			{"auto missing", "", "missing", "disabled"},
			{"auto partial", "", "partial", "disabled"},
			{"auto available", "", "ready", "ok"},
			{"enabled missing", "true", "missing", "error"},
			{"enabled partial", "true", "partial", "error"},
			{"enabled available", "true", "ready", "ok"},
			{"disabled missing", "false", "missing", "disabled"},
			{"disabled available", "false", "ready", "disabled"},
		} {
			t.Run(name+"/"+scenario.name, func(t *testing.T) {
				isolatedSetup(t)
				options := map[string]any{}
				if scenario.enabled != "" {
					options["enabled"] = scenario.enabled == "true"
				}
				if name == "github" && scenario.setup != "missing" {
					body := "exit 1"
					if scenario.setup == "ready" {
						body = githubTool
					}
					setupTool(t, "gh", body)
				}
				if name == "jira" && scenario.setup != "missing" {
					t.Setenv("RADAR_JIRA_BASE_URL", "https://jira.example.test")
					if scenario.setup == "ready" {
						t.Setenv("RADAR_JIRA_EMAIL", "radar@example.test")
						t.Setenv("RADAR_JIRA_API_TOKEN", "fixture")
						t.Setenv("RADAR_JIRA_CLOUD_ID", "fixture")
					}
				}
				if name == "datadog" && scenario.setup != "missing" {
					options["monitor_query"] = "tag:team:platform"
					if scenario.setup == "ready" {
						t.Setenv("RADAR_DATADOG_API_KEY", "fixture")
						t.Setenv("RADAR_DATADOG_APP_KEY", "fixture")
					}
				}
				if name == "sbx" && scenario.setup == "ready" {
					setupTool(t, "sbx", `printf '{"sandboxes":[]}'`)
				}
				data, _ := json.Marshal(map[string]any{name: options})
				setupConfig(t, string(data))
				var got integration.StatusResult
				for _, source := range app.DefaultIntegrations().Sources() {
					if source.Descriptor().Name == name {
						got = source.(integration.StatusReporter).Status(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)))
					}
				}
				if got.Status.Status != scenario.want || got.CanRun != (scenario.want == "ok") {
					t.Fatalf("status = %+v, want %s", got, scenario.want)
				}
				if scenario.enabled == "false" {
					if got.Status.Detail != "disabled by config" {
						t.Fatalf("status = %+v", got)
					}
					if data, _ := os.ReadFile(os.Getenv("RADAR_TEST_COMMAND_LOG")); len(data) > 0 {
						t.Fatalf("disabled source executed a tool: %s", data)
					}
				}
			})
		}
	}
}

func TestToolsInstalledAfterFirstLaunchAreDetected(t *testing.T) {
	isolatedSetup(t)
	if _, err := config.EnsureFile(); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, source := range app.DefaultIntegrations().Sources() {
		name := source.Descriptor().Name
		if name != "github" && name != "sbx" && name != "tmux" && name != "git" {
			continue
		}
		reporter := source.(integration.StatusReporter)
		if got := reporter.Status(context.Background(), logger); got.CanRun {
			t.Fatalf("%s initially available", name)
		}
		tool, body := name, "exit 0"
		if name == "github" {
			tool, body = "gh", githubTool
		}
		setupTool(t, tool, body)
		if got := reporter.Status(context.Background(), logger); !got.CanRun {
			t.Fatalf("%s not detected: %+v", name, got)
		}
	}
}

func TestInstalledButFailingSandboxIsAnErrorNotDisabled(t *testing.T) {
	isolatedSetup(t)
	setupConfig(t, `{}`)
	setupTool(t, "sbx", `printf 'not signed in; run sbx login\n' >&2; exit 1`)
	result := collector.Collect(context.Background(), nil, slog.New(slog.NewTextHandler(io.Discard, nil)), app.DefaultIntegrations().Sources())
	for _, status := range result.Sources {
		if status.Name == "sbx" && status.Status != "error" {
			t.Fatalf("status = %+v", status)
		}
		if status.Name == "workspace" && status.Status != "ok" {
			t.Fatalf("healthy source was affected: %+v", status)
		}
	}
	data, _ := os.ReadFile(os.Getenv("RADAR_TEST_COMMAND_LOG"))
	if strings.Contains(string(data), "login") {
		t.Fatalf("unexpected interactive login: %s", data)
	}
}

func TestDisabledSandboxStillObservesRegisteredRuntimes(t *testing.T) {
	home := isolatedSetup(t)
	root := filepath.Join(home, "workspaces")
	anchor := filepath.Join(root, "managed")
	setupConfig(t, `{"sbx":{"enabled":false},"workspace":{"root_dir":`+fmt.Sprintf("%q", root)+`}}`)
	if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{{
		ID: workspacegroup.ID(anchor), Name: "managed", Path: anchor,
		Sandbox: &workspacegroup.Sandbox{Name: "managed", Agent: "shell"},
	}}}); err != nil {
		t.Fatal(err)
	}
	setupTool(t, "sbx", `printf '%s\n' '{"sandboxes":[{"name":"managed","id":"one","workspaces":["/ignored"]},{"name":"unmanaged","id":"two","workspaces":["/ignored"]}]}'`)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, source := range app.DefaultIntegrations().Sources() {
		if source.Descriptor().Name != "sbx" {
			continue
		}
		collected := collector.CollectSources(context.Background(), nil, logger, []integration.Source{source})
		if len(collected.Observations) != 1 || collected.Observations[0].Ref.Path != anchor || collected.Observations[0].Ref.Title != "managed" {
			t.Fatalf("managed sandbox was hidden or unmanaged sandbox leaked: %+v", collected)
		}
		_, err := source.(integration.CleanupProvider).Cleanup(context.Background(), integration.CleanupRequest{Target: protocol.CleanupTarget{Source: "sbx", Kind: "sandbox", ResourceID: "managed"}, Force: true})
		if err != nil {
			t.Fatalf("explicit cleanup failed while disabled: %v", err)
		}
	}
	data, _ := os.ReadFile(os.Getenv("RADAR_TEST_COMMAND_LOG"))
	if !strings.Contains(string(data), "rm --force managed") {
		t.Fatalf("cleanup was skipped: %s", data)
	}
}
