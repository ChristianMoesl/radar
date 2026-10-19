package datadog

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"radar/internal/config"
	"radar/internal/integration"
	datadogsettings "radar/internal/integration/datadog/settings"
	"radar/internal/setup"
)

func configureSetup(ctx context.Context, req integration.SetupRequest) error {
	cfg := req.Config
	site, err := req.UI.Input(setup.Question{Title: "Datadog site or API endpoint", Hint: "For example datadoghq.eu or https://api.us3.datadoghq.com", Initial: datadogSiteDefault(cfg.Datadog.Site), Validate: func(value string) error { _, err := datadogsettings.NormalizeSite(value); return err }})
	if err != nil {
		return err
	}
	cfg.Datadog.Site, err = datadogsettings.NormalizeSite(site)
	if err != nil {
		return err
	}
	apiKey, err := req.Secret("Datadog API key", "", "RADAR_DATADOG_API_KEY", "datadog", "api_key")
	if err != nil {
		return err
	}
	appKey, err := req.Secret("Datadog application key", "The application key needs monitors_read access.", "RADAR_DATADOG_APP_KEY", "datadog", "app_key")
	if err != nil {
		return err
	}
	query, err := req.UI.Input(setup.Question{Title: "Datadog monitor query", Initial: cfg.Datadog.MonitorQuery, Hint: "Scope the monitors you want to watch, for example tag:team:platform. Do not add a status filter.", Validate: setup.Required})
	if err != nil {
		return err
	}
	cfg.Datadog.MonitorQuery = query
	req.UI.Print("Checking Datadog credentials and monitor access…\n")
	values := url.Values{"query": {query}, "per_page": {"1"}}
	if err := setup.Get(ctx, req.HTTP, "https://api."+datadogSiteDefault(cfg.Datadog.Site)+"/api/v1/monitor/search?"+values.Encode(), func(req *http.Request) {
		req.Header.Set("DD-API-KEY", apiKey)
		req.Header.Set("DD-APPLICATION-KEY", appKey)
	}, nil); err != nil {
		return fmt.Errorf("Datadog authentication or monitor access failed — check the site and keys: %w", err)
	}
	req.UI.Print("✓ Datadog authenticated\n")
	return nil
}

func datadogSiteDefault(site string) string {
	site = config.EnvOr("RADAR_DATADOG_SITE", site)
	if site == "" {
		return "datadoghq.eu"
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
	enabled, err := req.UI.Confirm("Connect Radar to Datadog?", !req.Draft.Existing || req.Config.Datadog.Enabled == nil || *req.Config.Datadog.Enabled)
	if err != nil {
		return setup.Plan{}, err
	}
	req.Config.Datadog.Enabled = &enabled
	if enabled {
		err = configureSetup(ctx, req)
	}
	return setup.Plan{}, err
}
