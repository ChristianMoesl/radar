package datadog

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"radar/internal/command"
	"radar/internal/config"
	"radar/internal/integration/datadog/settings"
	"radar/internal/setup"
)

const (
	defaultSite     = "datadoghq.eu"
	monitorPageSize = 1000
)

type connection struct {
	Site       string
	Org        string
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
	Search(context.Context, connection, string, []string) (monitorSearchResponse, error)
}

type apiClient struct {
	output func(context.Context, setup.Command) (string, error)
}

func (c apiClient) Search(ctx context.Context, cfg connection, userQuery string, statuses []string) (monitorSearchResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := verifyPup(ctx, cfg, c.output); err != nil {
		return monitorSearchResponse{}, err
	}
	args := pupArgs(cfg, "api", "/api/v1/monitor/search", "-f", "query="+combinedMonitorQuery(userQuery, statuses), "-F", "page=0", "-F", fmt.Sprintf("per_page=%d", monitorPageSize))
	data, err := pupOutput(ctx, cfg, args, c.output)
	if err != nil {
		return monitorSearchResponse{}, err
	}
	var wire struct {
		Errors   []json.RawMessage `json:"errors"`
		Metadata *struct {
			TotalCount *int `json:"total_count"`
			PageCount  *int `json:"page_count"`
		} `json:"metadata"`
		Monitors *[]monitor `json:"monitors"`
	}
	if json.Unmarshal(data, &wire) != nil || len(wire.Errors) > 0 || wire.Metadata == nil || wire.Metadata.TotalCount == nil || wire.Metadata.PageCount == nil || wire.Monitors == nil {
		return monitorSearchResponse{}, fmt.Errorf("pup returned an invalid monitor search response; previous monitor facts retained")
	}
	if *wire.Metadata.TotalCount < 0 || *wire.Metadata.PageCount < 0 || *wire.Metadata.TotalCount < len(*wire.Monitors) {
		return monitorSearchResponse{}, fmt.Errorf("pup returned inconsistent monitor totals")
	}
	seen := map[int64]bool{}
	for _, m := range *wire.Monitors {
		if m.ID <= 0 || m.Status == "" || seen[m.ID] {
			return monitorSearchResponse{}, fmt.Errorf("pup returned an invalid or duplicate monitor")
		}
		seen[m.ID] = true
	}
	response := monitorSearchResponse{Monitors: *wire.Monitors}
	response.Metadata.TotalCount = *wire.Metadata.TotalCount
	response.Metadata.PageCount = *wire.Metadata.PageCount
	return response, nil
}

func loadConnection() (connection, []string, error) {
	user, err := config.Load()
	if err != nil {
		return connection{}, nil, err
	}
	if _, err := config.LoadSecrets(); err != nil {
		return connection{}, nil, err
	}
	if err := legacyEnvironmentError(); err != nil {
		return connection{}, nil, err
	}
	site := user.Datadog.Site
	if site == "" {
		site = defaultSite
	}
	site, err = settings.NormalizeSite(site)
	if err != nil {
		return connection{}, nil, err
	}
	cfg := connection{Site: site, Org: user.Datadog.Org, AppBaseURL: datadogAppBaseURL(site)}
	missing := []string{}
	if _, err := exec.LookPath("pup"); err != nil {
		missing = append(missing, "pup 2.0.0+ on PATH; run radar setup")
	}
	if cfg.Org == "" {
		return cfg, nil, fmt.Errorf("datadog.org must select a named Pup OAuth session; run radar setup")
	}
	return cfg, missing, nil
}

func legacyEnvironmentError() error {
	for _, key := range []string{"RADAR_DATADOG_API_KEY", "RADAR_DATADOG_APP_KEY", "RADAR_DATADOG_SITE"} {
		if os.Getenv(key) != "" {
			return fmt.Errorf("%s is no longer supported; unset it, configure datadog.site and run radar setup for Pup OAuth", key)
		}
	}
	return nil
}

func pupArgs(cfg connection, args ...string) []string {
	return append([]string{"--org", cfg.Org, "--no-envelope", "--output", "json"}, args...)
}

// A named session reloads OAuth from Pup storage, overriding config-file bearer
// connection. Nonempty invalid header values shadow file API keys: if OAuth
// disappears or refresh fails between status and request, reqwest rejects the
// fallback headers before sending anything. Never read or copy Pup connection.
func pupEnv(site, org string) []string {
	env := []string{}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "DD_ACCESS_TOKEN", "DD_API_KEY", "DD_APP_KEY", "DD_SITE", "DD_ORG", "DD_OUTPUT", "PUP_OUTPUT", "PUP_FILTER", "PUP_MOCK_SERVER":
			continue
		}
		env = append(env, entry)
	}
	return append(env, "DD_SITE="+site, "DD_ORG="+org, "DD_API_KEY=\n", "DD_APP_KEY=\n")
}

func pupOutput(ctx context.Context, cfg connection, args []string, output func(context.Context, setup.Command) (string, error)) ([]byte, error) {
	if output != nil {
		v, e := output(ctx, setup.Command{Name: "pup", Args: args, Env: pupEnv(cfg.Site, cfg.Org)})
		return []byte(v), e
	}
	return command.Output(ctx, "pup", args, pupEnv(cfg.Site, cfg.Org))
}

func verifyPup(ctx context.Context, cfg connection, output func(context.Context, setup.Command) (string, error)) error {
	run := func(args []string) ([]byte, error) { return pupOutput(ctx, cfg, args, output) }
	version, err := run([]string{"--version"})
	if err != nil || !setup.VersionAtLeast(string(version), "2.0.0") {
		return fmt.Errorf("pup 2.0.0+ is required; run radar setup")
	}
	data, err := run(pupArgs(cfg, "auth", "status"))
	if err != nil {
		return fmt.Errorf("Pup OAuth session is unavailable; run radar setup")
	}
	var status struct {
		Authenticated bool     `json:"authenticated"`
		AuthMethod    string   `json:"auth_method"`
		TokenType     string   `json:"token_type"`
		HasRefresh    bool     `json:"has_refresh"`
		Site          string   `json:"site"`
		Org           string   `json:"org"`
		Status        string   `json:"status"`
		Scopes        []string `json:"scopes"`
	}
	if json.Unmarshal(data, &status) != nil || !status.Authenticated || status.AuthMethod != "" || !strings.EqualFold(status.TokenType, "Bearer") || !status.HasRefresh || status.Status != "valid" || status.Site != cfg.Site || status.Org != cfg.Org {
		return fmt.Errorf("Pup requires a valid OAuth session for %s (%s); run radar setup", cfg.Site, cfg.Org)
	}
	for _, scope := range status.Scopes {
		if scope == "monitors_read" {
			return nil
		}
	}
	return fmt.Errorf("Pup OAuth session needs monitors_read; run radar setup")
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
