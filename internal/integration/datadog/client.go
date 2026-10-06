package datadog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"radar/internal/config"
	"radar/internal/integration/datadog/settings"
)

const (
	defaultSite     = "datadoghq.eu"
	monitorPageSize = 1000
)

type credentials struct {
	APIKey     string
	AppKey     string
	Site       string
	APIBaseURL string
	AppBaseURL string
}

type monitor struct {
	ID                   int64    `json:"id"`
	Name                 string   `json:"name"`
	Status               string   `json:"status"`
	Priority             *int     `json:"priority"`
	Tags                 []string `json:"tags"`
	Scopes               []string `json:"scopes"`
	LastTriggeredUnix    int64    `json:"last_triggered_ts"`
	OverallStateModified int64    `json:"overall_state_modified"`
}

type monitorSearchResponse struct {
	Metadata struct {
		TotalCount int `json:"total_count"`
		PageCount  int `json:"page_count"`
	} `json:"metadata"`
	Monitors []monitor `json:"monitors"`
}

type monitorSearcher interface {
	Search(context.Context, credentials, string, []string) (monitorSearchResponse, error)
}

type apiClient struct {
	httpClient *http.Client
}

func (c apiClient) Search(ctx context.Context, cfg credentials, userQuery string, statuses []string) (monitorSearchResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	values := url.Values{}
	values.Set("query", combinedMonitorQuery(userQuery, statuses))
	values.Set("page", "0")
	values.Set("per_page", fmt.Sprint(monitorPageSize))
	endpoint := cfg.APIBaseURL + "/api/v1/monitor/search?" + values.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return monitorSearchResponse{}, fmt.Errorf("create Datadog monitor search request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("DD-API-KEY", cfg.APIKey)
	req.Header.Set("DD-APPLICATION-KEY", cfg.AppKey)

	client := c.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return monitorSearchResponse{}, fmt.Errorf("Datadog monitor search request failed: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode < http.StatusOK || res.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
		return monitorSearchResponse{}, fmt.Errorf("Datadog monitor search failed: %s", res.Status)
	}

	var response monitorSearchResponse
	decoder := json.NewDecoder(io.LimitReader(res.Body, 20<<20))
	if err := decoder.Decode(&response); err != nil {
		return monitorSearchResponse{}, fmt.Errorf("decode Datadog monitor search response: %w", err)
	}
	return response, nil
}

func loadCredentials() (credentials, []string, error) {
	user, err := config.Load()
	if err != nil {
		return credentials{}, nil, err
	}
	apiKey, err := config.Secret("RADAR_DATADOG_API_KEY", "datadog", "api_key")
	if err != nil {
		return credentials{}, nil, err
	}
	appKey, err := config.Secret("RADAR_DATADOG_APP_KEY", "datadog", "app_key")
	if err != nil {
		return credentials{}, nil, err
	}
	cfg := credentials{APIKey: apiKey, AppKey: appKey, Site: config.EnvOr("RADAR_DATADOG_SITE", user.Datadog.Site)}

	missing := make([]string, 0, 2)
	if cfg.APIKey == "" {
		missing = append(missing, "datadog.api_key in secrets.json or RADAR_DATADOG_API_KEY")
	}
	if cfg.AppKey == "" {
		missing = append(missing, "datadog.app_key in secrets.json or RADAR_DATADOG_APP_KEY")
	}
	if cfg.Site == "" {
		cfg.Site = defaultSite
	}

	site, err := settings.NormalizeSite(cfg.Site)
	if err != nil {
		return cfg, missing, err
	}
	cfg.Site = site
	cfg.APIBaseURL = "https://api." + site
	cfg.AppBaseURL = datadogAppBaseURL(site)
	return cfg, missing, nil
}

func datadogAppBaseURL(site string) string {
	switch site {
	case "datadoghq.com", "datadoghq.eu", "ddog-gov.com":
		return "https://app." + site
	default:
		return "https://" + site
	}
}

func combinedMonitorQuery(userQuery string, statuses []string) string {
	queryStatuses := make([]string, 0, len(statuses))
	for _, status := range statuses {
		status = strings.TrimSpace(status)
		if strings.Contains(status, " ") {
			status = fmt.Sprintf("%q", status)
		}
		queryStatuses = append(queryStatuses, status)
	}
	return fmt.Sprintf("(%s) status:(%s)", strings.TrimSpace(userQuery), strings.Join(queryStatuses, " OR "))
}
