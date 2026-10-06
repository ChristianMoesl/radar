package onboarding

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"radar/internal/config"
	datadogsettings "radar/internal/integration/datadog/settings"
)

func (w wizard) jira(ctx context.Context, cfg *config.Config, secrets config.Secrets) error {
	site, err := w.ui.input(question{title: "Jira site URL", hint: "For example https://example.atlassian.net", validate: validateSiteURL})
	if err != nil {
		return err
	}
	cfg.Jira.BaseURL = strings.TrimRight(site, "/")
	email, err := w.ui.input(question{title: "Jira email", validate: func(value string) error {
		addr, err := mail.ParseAddress(value)
		if err != nil || addr.Address != value {
			return fmt.Errorf("enter an email address")
		}
		return nil
	}})
	if err != nil {
		return err
	}
	cfg.Jira.Email = email
	token, err := w.ui.input(question{title: "Jira API token", hint: "Create a token in your Atlassian account. Input is hidden; it will be saved in secrets.json.", secret: true, validate: required})
	if err != nil {
		return err
	}
	w.ui.print("Discovering the Jira Cloud ID…\n")
	var tenant struct {
		CloudID string `json:"cloudId"`
	}
	if err := w.get(ctx, cfg.Jira.BaseURL+"/_edge/tenant_info", nil, &tenant); err != nil {
		return fmt.Errorf("could not discover Jira Cloud ID — check the site URL and connection, then retry setup: %w", err)
	}
	if !regexp.MustCompile(`^[A-Za-z0-9-]+$`).MatchString(tenant.CloudID) {
		return fmt.Errorf("Jira site returned no valid Cloud ID — check the site URL")
	}
	cfg.Jira.CloudID = tenant.CloudID
	w.ui.print("Checking Jira authentication…\n")
	endpoint := "https://api.atlassian.com/ex/jira/" + tenant.CloudID + "/rest/api/3/myself"
	if err := w.get(ctx, endpoint, func(req *http.Request) { req.SetBasicAuth(email, token) }, nil); err != nil {
		return fmt.Errorf("Jira authentication failed — check your email, API token and Jira access: %w", err)
	}
	prefixes, err := w.ui.input(question{title: "Jira ticket prefixes", hint: "One or more project prefixes, separated by commas or spaces (for example ABC, XYZ).", validate: func(value string) error { _, err := parsePrefixes(value); return err }})
	if err != nil {
		return err
	}
	cfg.LinkingMarkPrefixes, err = parsePrefixes(prefixes)
	if err != nil {
		return err
	}
	secrets["jira"] = map[string]string{"api_token": token}
	w.ui.print("✓ Jira authenticated\n")
	return nil
}

func (w wizard) datadog(ctx context.Context, cfg *config.Config, secrets config.Secrets) error {
	site, err := w.ui.input(question{title: "Datadog site or API endpoint", hint: "For example datadoghq.eu or https://api.us3.datadoghq.com", initial: "datadoghq.eu", validate: func(value string) error { _, err := datadogsettings.NormalizeSite(value); return err }})
	if err != nil {
		return err
	}
	cfg.Datadog.Site, err = datadogsettings.NormalizeSite(site)
	if err != nil {
		return err
	}
	apiKey, err := w.ui.input(question{title: "Datadog API key", secret: true, validate: required})
	if err != nil {
		return err
	}
	appKey, err := w.ui.input(question{title: "Datadog application key", hint: "The application key needs monitors_read access. Input is hidden.", secret: true, validate: required})
	if err != nil {
		return err
	}
	query, err := w.ui.input(question{title: "Datadog monitor query", hint: "Scope the monitors you want to watch, for example tag:team:platform. Do not add a status filter.", validate: required})
	if err != nil {
		return err
	}
	cfg.Datadog.MonitorQuery = query
	w.ui.print("Checking Datadog credentials and monitor access…\n")
	values := url.Values{"query": {query}, "per_page": {"1"}}
	if err := w.get(ctx, "https://api."+cfg.Datadog.Site+"/api/v1/monitor/search?"+values.Encode(), func(req *http.Request) {
		req.Header.Set("DD-API-KEY", apiKey)
		req.Header.Set("DD-APPLICATION-KEY", appKey)
	}, nil); err != nil {
		return fmt.Errorf("Datadog authentication or monitor access failed — check the site and keys: %w", err)
	}
	secrets["datadog"] = map[string]string{"api_key": apiKey, "app_key": appKey}
	w.ui.print("✓ Datadog authenticated\n")
	return nil
}

// Never include a response body or the transport's arbitrary error text in a
// credential check failure. Servers/proxies can echo authentication headers.
func (w wizard) get(ctx context.Context, endpoint string, auth func(*http.Request), result any) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("invalid service endpoint")
	}
	req.Header.Set("Accept", "application/json")
	if auth != nil {
		auth(req)
	}
	res, err := w.http.Do(req)
	if err != nil {
		return fmt.Errorf("request failed (check network connectivity)")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("service returned HTTP %d", res.StatusCode)
	}
	if result != nil {
		if json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(result) != nil {
			return fmt.Errorf("service returned an invalid response")
		}
	}
	return nil
}

func required(value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("a value is required")
	}
	return nil
}

func validateSiteURL(value string) error {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("enter an HTTPS site URL without credentials, a path or query")
	}
	return nil
}

func parsePrefixes(value string) ([]string, error) {
	parts := strings.FieldsFunc(strings.ToUpper(value), func(r rune) bool { return r == ',' || r == ' ' || r == '\t' })
	result := []string{}
	seen := map[string]bool{}
	for _, part := range parts {
		if !regexp.MustCompile(`^[A-Z][A-Z0-9]*$`).MatchString(part) {
			return nil, fmt.Errorf("prefixes must start with a letter and contain only letters and numbers")
		}
		if !seen[part] {
			result = append(result, part)
			seen[part] = true
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("enter at least one ticket prefix")
	}
	return result, nil
}

func expandPath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "~" || strings.HasPrefix(value, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		value = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(value, "~"), "/"))
	}
	if !filepath.IsAbs(value) {
		return "", fmt.Errorf("use an absolute path or ~/path")
	}
	return filepath.Clean(value), nil
}
