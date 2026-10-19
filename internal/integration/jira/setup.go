package jira

import (
	"context"
	"fmt"
	"net/http"
	"net/mail"
	"os"
	"regexp"
	"strings"

	"radar/internal/config"
	"radar/internal/integration"
	"radar/internal/setup"
)

func configureSetup(ctx context.Context, req integration.SetupRequest) error {
	cfg := req.Config
	site, err := req.UI.Input(setup.Question{Title: "Jira site URL", Hint: "For example https://example.atlassian.net", Initial: config.EnvOr("RADAR_JIRA_BASE_URL", cfg.Jira.BaseURL), Validate: setup.ValidateSiteURL})
	if err != nil {
		return err
	}
	oldSite := cfg.Jira.BaseURL
	cfg.Jira.BaseURL = strings.TrimRight(site, "/")
	email, err := req.UI.Input(setup.Question{Title: "Jira email", Initial: config.EnvOr("RADAR_JIRA_EMAIL", cfg.Jira.Email), Validate: func(value string) error {
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
	token, err := req.Secret("Jira API token", "Create a token in your Atlassian account.", "RADAR_JIRA_API_TOKEN", "jira", "api_token")
	if err != nil {
		return err
	}
	if cfg.Jira.APIBaseURL != "" {
		value, err := req.UI.Input(setup.Question{Title: "Jira API base URL", Hint: "Existing API override; retain it or enter the API base for this connection.", Initial: cfg.Jira.APIBaseURL, Validate: setup.ValidateAPIURL})
		if err != nil {
			return err
		}
		cfg.Jira.APIBaseURL = strings.TrimRight(value, "/")
	}
	apiBase := config.EnvOr("RADAR_JIRA_API_BASE_URL", cfg.Jira.APIBaseURL)
	cloudID := config.EnvOr("RADAR_JIRA_CLOUD_ID", cfg.Jira.CloudID)
	if apiBase == "" {
		if cloudID == "" || (oldSite != cfg.Jira.BaseURL && os.Getenv("RADAR_JIRA_CLOUD_ID") == "") {
			req.UI.Print("Discovering the Jira Cloud ID…\n")
			var tenant struct {
				CloudID string `json:"cloudId"`
			}
			if err := setup.Get(ctx, req.HTTP, config.EnvOr("RADAR_JIRA_BASE_URL", cfg.Jira.BaseURL)+"/_edge/tenant_info", nil, &tenant); err != nil {
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
	if err := setup.ValidateAPIURL(apiBase); err != nil {
		return err
	}
	req.UI.Print("Checking Jira authentication…\n")
	endpoint := strings.TrimRight(apiBase, "/") + "/myself"
	if err := setup.Get(ctx, req.HTTP, endpoint, func(req *http.Request) { req.SetBasicAuth(config.EnvOr("RADAR_JIRA_EMAIL", email), token) }, nil); err != nil {
		return fmt.Errorf("Jira authentication failed — check your email, API token and Jira access: %w", err)
	}
	prefixes, err := req.UI.Input(setup.Question{Title: "Jira ticket prefixes", Initial: strings.Join(cfg.LinkingMarkPrefixes, ", "), Hint: "One or more project prefixes, separated by commas or spaces (for example ABC, XYZ).", Validate: func(value string) error { _, err := parsePrefixes(value); return err }})
	if err != nil {
		return err
	}
	cfg.LinkingMarkPrefixes, err = parsePrefixes(prefixes)
	if err != nil {
		return err
	}
	req.UI.Print("✓ Jira authenticated\n")
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

func (Source) SetupStage() integration.SetupStage {
	return integration.SetupStage{Phase: integration.SetupConnections, Order: 1}
}
func (Source) Setup(ctx context.Context, req integration.SetupRequest) (setup.Plan, error) {
	enabled, err := req.UI.Confirm("Connect Radar to Jira?", !req.Draft.Existing || req.Config.Jira.Enabled == nil || *req.Config.Jira.Enabled)
	if err != nil {
		return setup.Plan{}, err
	}
	req.Config.Jira.Enabled = &enabled
	if enabled {
		err = configureSetup(ctx, req)
	}
	return setup.Plan{}, err
}
