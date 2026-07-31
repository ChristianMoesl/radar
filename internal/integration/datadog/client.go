package datadog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	defaultSite      = "datadoghq.eu"
	monitorPageSize  = 1000
	activeStateQuery = `status:(Alert OR Warn OR "No Data")`
)

var supportedSites = map[string]bool{
	"datadoghq.com":     true,
	"us3.datadoghq.com": true,
	"us5.datadoghq.com": true,
	"datadoghq.eu":      true,
	"ap1.datadoghq.com": true,
	"ap2.datadoghq.com": true,
	"ddog-gov.com":      true,
}

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
	Search(context.Context, credentials, string) (monitorSearchResponse, error)
}

type apiClient struct {
	httpClient *http.Client
}

func (c apiClient) Search(ctx context.Context, cfg credentials, userQuery string) (monitorSearchResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	values := url.Values{}
	values.Set("query", combinedMonitorQuery(userQuery))
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

func credentialsFromEnv() (credentials, []string, error) {
	cfg := credentials{
		APIKey: strings.TrimSpace(os.Getenv("RADAR_DATADOG_API_KEY")),
		AppKey: strings.TrimSpace(os.Getenv("RADAR_DATADOG_APP_KEY")),
		Site:   strings.TrimSpace(os.Getenv("RADAR_DATADOG_SITE")),
	}
	missing := make([]string, 0, 2)
	if cfg.APIKey == "" {
		missing = append(missing, "RADAR_DATADOG_API_KEY")
	}
	if cfg.AppKey == "" {
		missing = append(missing, "RADAR_DATADOG_APP_KEY")
	}
	if cfg.Site == "" {
		cfg.Site = defaultSite
	}

	site, err := normalizeSite(cfg.Site)
	if err != nil {
		return cfg, missing, err
	}
	cfg.Site = site
	cfg.APIBaseURL = "https://api." + site
	cfg.AppBaseURL = datadogAppBaseURL(site)
	return cfg, missing, nil
}

func normalizeSite(site string) (string, error) {
	site = strings.TrimSpace(strings.ToLower(site))
	site = strings.TrimPrefix(site, "https://")
	site = strings.TrimPrefix(site, "http://")
	site = strings.TrimPrefix(site, "api.")
	if strings.ContainsAny(site, "/?#") || !supportedSites[site] {
		return "", fmt.Errorf("RADAR_DATADOG_SITE must be a supported Datadog site hostname")
	}
	return site, nil
}

func datadogAppBaseURL(site string) string {
	switch site {
	case "datadoghq.com", "datadoghq.eu", "ddog-gov.com":
		return "https://app." + site
	default:
		return "https://" + site
	}
}

func combinedMonitorQuery(userQuery string) string {
	return fmt.Sprintf("(%s) %s", strings.TrimSpace(userQuery), activeStateQuery)
}
