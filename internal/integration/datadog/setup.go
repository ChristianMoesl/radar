package datadog

import (
	"context"
	"strings"

	"radar/internal/integration"
	datadogsettings "radar/internal/integration/datadog/settings"
	"radar/internal/setup"
)

func configureSetup(ctx context.Context, req integration.SetupRequest) (bool, error) {
	if err := legacyEnvironmentError(); err != nil {
		return false, err
	}
	cfg := req.Config
	site, err := req.UI.Input(setup.Question{Title: "Datadog site", Initial: datadogSiteDefault(cfg.Datadog.Site), Hint: "For example datadoghq.eu or us3.datadoghq.com", Validate: func(v string) error { _, e := datadogsettings.NormalizeSite(v); return e }})
	if err != nil {
		return false, err
	}
	cfg.Datadog.Site, err = datadogsettings.NormalizeSite(site)
	if err != nil {
		return false, err
	}
	org, err := req.UI.Input(setup.Question{Title: "Pup OAuth session name", Initial: cfg.Datadog.Org, Hint: "A named Pup session isolates the intended Datadog organization. Sign in to that organization in the browser.", Validate: setup.Required})
	if err != nil {
		return false, err
	}
	cfg.Datadog.Org = strings.TrimSpace(org)
	query, err := req.UI.Input(setup.Question{Title: "Datadog monitor query", Initial: cfg.Datadog.MonitorQuery, Hint: "For example tag:team:platform. Do not add a status filter.", Validate: setup.Required})
	if err != nil {
		return false, err
	}
	cfg.Datadog.MonitorQuery = query
	connection := connection{Site: cfg.Datadog.Site, Org: cfg.Datadog.Org}
	install := "curl -fsSL https://raw.githubusercontent.com/DataDog/pup/v2.0.0/install.sh | PUP_VERSION=2.0.0 sh"
	connected, err := setup.ConnectCLI(ctx, req.UI, req.System, setup.CLIConnection{
		Name: "pup", Service: "Datadog", MinimumVersion: "2.0.0", Reason: "OAuth monitor access and automatic credential refresh",
		InstallInstructions: install, Install: &setup.Command{Name: "bash", Args: []string{"-o", "pipefail", "-c", install}},
		Login: setup.Command{Name: "pup", Args: []string{"auth", "login", "--site", connection.Site, "--org", connection.Org, "--scopes", "monitors_read"}, Env: pupEnv(connection.Site, connection.Org)},
		Verify: func(ctx context.Context) error {
			_, err := (apiClient{output: req.System.Output}).Search(ctx, connection, query, cfg.Datadog.MonitorStatuses)
			return err
		},
	}, req.Completed)
	if err == nil && connected {
		req.UI.Print("✓ Datadog connected at %s with Pup OAuth session %s (monitor read verified)\n", connection.Site, connection.Org)
	}
	return connected, err
}

func datadogSiteDefault(site string) string {
	if site == "" {
		return defaultSite
	}
	if normalized, err := datadogsettings.NormalizeSite(site); err == nil {
		return normalized
	}
	return site
}
func (Source) SetupStage() integration.SetupStage {
	return integration.SetupStage{Phase: integration.SetupConnections, Order: 2}
}
func (Source) Setup(ctx context.Context, req integration.SetupRequest) (setup.Plan, error) {
	original := req.Config.Datadog
	enabled, err := req.UI.Confirm("Connect Radar to Datadog?", !req.Draft.Existing || req.Config.Datadog.Enabled == nil || *req.Config.Datadog.Enabled)
	if err != nil {
		return setup.Plan{}, err
	}
	req.Config.Datadog.Enabled = &enabled
	if enabled {
		connected, e := configureSetup(ctx, req)
		connected = connected && e == nil
		if !connected {
			original.Enabled = &connected
			req.Config.Datadog = original
		} else {
			*req.Config.Datadog.Enabled = connected
		}
		err = e
	}
	return setup.Plan{}, err
}
