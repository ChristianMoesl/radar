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
	site, err := w.ui.input(question{title: "Jira site URL", hint: "For example https://example.atlassian.net", initial: config.EnvOr("RADAR_JIRA_BASE_URL", cfg.Jira.BaseURL), validate: validateSiteURL})
	if err != nil {
		return err
	}
	oldSite := cfg.Jira.BaseURL
	cfg.Jira.BaseURL = strings.TrimRight(site, "/")
	email, err := w.ui.input(question{title: "Jira email", initial: config.EnvOr("RADAR_JIRA_EMAIL", cfg.Jira.Email), validate: func(value string) error {
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
	token, err := w.secret("Jira API token", "Create a token in your Atlassian account.", "RADAR_JIRA_API_TOKEN", "jira", "api_token", secrets)
	if err != nil {
		return err
	}
	if cfg.Jira.APIBaseURL != "" {
		value, err := w.ui.input(question{title: "Jira API base URL", hint: "Existing API override; retain it or enter the API base for this connection.", initial: cfg.Jira.APIBaseURL, validate: validateAPIURL})
		if err != nil {
			return err
		}
		cfg.Jira.APIBaseURL = strings.TrimRight(value, "/")
	}
	apiBase := config.EnvOr("RADAR_JIRA_API_BASE_URL", cfg.Jira.APIBaseURL)
	cloudID := config.EnvOr("RADAR_JIRA_CLOUD_ID", cfg.Jira.CloudID)
	if apiBase == "" {
		if cloudID == "" || (oldSite != cfg.Jira.BaseURL && os.Getenv("RADAR_JIRA_CLOUD_ID") == "") {
			w.ui.print("Discovering the Jira Cloud ID…\n")
			var tenant struct {
				CloudID string `json:"cloudId"`
			}
			if err := w.get(ctx, config.EnvOr("RADAR_JIRA_BASE_URL", cfg.Jira.BaseURL)+"/_edge/tenant_info", nil, &tenant); err != nil {
				return fmt.Errorf("could not discover Jira Cloud ID — check the site URL and connection, then retry setup: %w", err)
			}
			cloudID = tenant.CloudID
			cfg.Jira.CloudID = cloudID
		}
		if !regexp.MustCompile(`^[A-Za-z0-9-]+$`).MatchString(cloudID) {
			return fmt.Errorf("Jira site returned no valid Cloud ID — check the site URL")
		}
		apiBase = "https://api.atlassian.com/ex/jira/" + cloudID + "/rest/api/3"
	}
	if err := validateAPIURL(apiBase); err != nil {
		return err
	}
	w.ui.print("Checking Jira authentication…\n")
	endpoint := strings.TrimRight(apiBase, "/") + "/myself"
	if err := w.get(ctx, endpoint, func(req *http.Request) { req.SetBasicAuth(config.EnvOr("RADAR_JIRA_EMAIL", email), token) }, nil); err != nil {
		return fmt.Errorf("Jira authentication failed — check your email, API token and Jira access: %w", err)
	}
	prefixes, err := w.ui.input(question{title: "Jira ticket prefixes", initial: strings.Join(cfg.LinkingMarkPrefixes, ", "), hint: "One or more project prefixes, separated by commas or spaces (for example ABC, XYZ).", validate: func(value string) error { _, err := parsePrefixes(value); return err }})
	if err != nil {
		return err
	}
	cfg.LinkingMarkPrefixes, err = parsePrefixes(prefixes)
	if err != nil {
		return err
	}
	w.ui.print("✓ Jira authenticated\n")
	return nil
}

func (w wizard) datadog(ctx context.Context, cfg *config.Config, secrets config.Secrets) error {
	site, err := w.ui.input(question{title: "Datadog site or API endpoint", hint: "For example datadoghq.eu or https://api.us3.datadoghq.com", initial: datadogSiteDefault(cfg.Datadog.Site), validate: func(value string) error { _, err := datadogsettings.NormalizeSite(value); return err }})
	if err != nil {
		return err
	}
	cfg.Datadog.Site, err = datadogsettings.NormalizeSite(site)
	if err != nil {
		return err
	}
	apiKey, err := w.secret("Datadog API key", "", "RADAR_DATADOG_API_KEY", "datadog", "api_key", secrets)
	if err != nil {
		return err
	}
	appKey, err := w.secret("Datadog application key", "The application key needs monitors_read access.", "RADAR_DATADOG_APP_KEY", "datadog", "app_key", secrets)
	if err != nil {
		return err
	}
	query, err := w.ui.input(question{title: "Datadog monitor query", initial: cfg.Datadog.MonitorQuery, hint: "Scope the monitors you want to watch, for example tag:team:platform. Do not add a status filter.", validate: required})
	if err != nil {
		return err
	}
	cfg.Datadog.MonitorQuery = query
	w.ui.print("Checking Datadog credentials and monitor access…\n")
	values := url.Values{"query": {query}, "per_page": {"1"}}
	if err := w.get(ctx, "https://api."+datadogSiteDefault(cfg.Datadog.Site)+"/api/v1/monitor/search?"+values.Encode(), func(req *http.Request) {
		req.Header.Set("DD-API-KEY", apiKey)
		req.Header.Set("DD-APPLICATION-KEY", appKey)
	}, nil); err != nil {
		return fmt.Errorf("Datadog authentication or monitor access failed — check the site and keys: %w", err)
	}
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

func validateAPIURL(value string) error {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("enter an HTTPS API base URL without credentials or a query")
	}
	return nil
}

// Never prefill a text input with a secret, even a masked one. Empty input keeps
// an existing credential; only explicit replacements become disk updates.
func (w wizard) secret(title, hint, environment, namespace, key string, updates config.Secrets) (string, error) {
	if value := strings.TrimSpace(os.Getenv(environment)); value != "" {
		w.ui.print("%s: using %s (not copied to disk). Unset it to replace the stored credential through setup.\n", title, environment)
		return value, nil
	}
	saved, err := config.LoadSecrets()
	if err != nil {
		return "", err
	}
	existing := saved[namespace][key]
	hint += " Input is hidden."
	check := required
	if existing != "" {
		hint += " Leave blank to keep the stored credential."
		check = nil
	}
	value, err := w.ui.input(question{title: title, hint: hint, secret: true, validate: check})
	if err != nil {
		return "", err
	}
	if value == "" {
		return existing, nil
	}
	if updates[namespace] == nil {
		updates[namespace] = map[string]string{}
	}
	updates[namespace][key] = value
	return value, nil
}
