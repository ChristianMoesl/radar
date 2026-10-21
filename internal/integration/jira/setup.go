package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"radar/internal/integration"
	jirasettings "radar/internal/integration/jira/settings"
	"radar/internal/setup"
)

func configureSetup(ctx context.Context, req integration.SetupRequest) (bool, error) {
	if err := legacyEnvironmentError(); err != nil {
		return false, err
	}
	cfg := req.Config
	site, err := req.UI.Input(setup.Question{Title: "Jira site URL", Initial: cfg.Jira.BaseURL, Hint: "For example https://example.atlassian.net", Validate: setup.ValidateSiteURL})
	if err != nil {
		return false, err
	}
	site = strings.TrimRight(site, "/")
	var tenant struct {
		CloudID string `json:"cloudId"`
	}
	req.UI.Print("Discovering the Jira Cloud ID…\n")
	for {
		discoveryErr := setup.Get(ctx, req.HTTP, site+"/_edge/tenant_info", nil, &tenant)
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if discoveryErr == nil && regexp.MustCompile(`^[A-Za-z0-9-]+$`).MatchString(tenant.CloudID) {
			break
		}
		req.UI.Print("The Jira site could not be verified. Check the site URL and network.\n")
		retry, err := req.UI.Confirm("Retry Jira site discovery? (No defers Jira)", false)
		if err != nil {
			return false, err
		}
		if !retry {
			return false, nil
		}
	}
	connection := Config{BaseURL: site, CloudID: tenant.CloudID, output: req.System.Output}
	// This is non-secret CLI metadata; never inspect the credential files.
	var listing struct {
		APIVersion string `json:"apiVersion"`
		Data       struct {
			Profiles []twgProfile `json:"profiles"`
		} `json:"data"`
	}
	if data, e := twgOutput(ctx, connection, []string{"--output", "json", "--output-summary", "none", "--api-version", "v2", "auth", "profile", "list"}); e == nil {
		if json.Unmarshal(data, &listing) != nil || listing.APIVersion != "v2" {
			listing.Data.Profiles = nil
		}
	}
	initialProfile := cfg.Jira.Profile
	u, _ := url.Parse(site)
	for _, p := range listing.Data.Profiles {
		if p.AuthMethod == "oauth" && p.Environment == "prod" && p.CloudID == tenant.CloudID && strings.EqualFold(p.Domain, u.Hostname()) {
			initialProfile = p.Name
			if p.Name == cfg.Jira.Profile {
				break
			}
		}
	}
	profile, err := req.UI.Input(setup.Question{Title: "TWG OAuth profile name", Initial: initialProfile, Hint: "Reuse an existing OAuth profile for this Jira site; use a new name only if no profile exists.", Validate: func(v string) error { c := jirasettings.Default(); c.Profile = v; return jirasettings.Validate(c) }})
	if err != nil {
		return false, err
	}
	prefixes, err := req.UI.Input(setup.Question{Title: "Jira ticket prefixes", Initial: strings.Join(cfg.LinkingMarkPrefixes, ", "), Hint: "Project prefixes separated by commas or spaces, for example ABC, XYZ.", Validate: func(v string) error { _, e := parsePrefixes(v); return e }})
	if err != nil {
		return false, err
	}
	marks, err := parsePrefixes(prefixes)
	if err != nil {
		return false, err
	}
	connection.Profile = profile
	login := []string{"login", "--new-profile", profile, "--site", tenant.CloudID}
	for _, p := range listing.Data.Profiles {
		if p.Name == profile {
			login = []string{"--profile", profile, "login", "--force", "--site", tenant.CloudID}
			break
		}
	}
	install := `installer=$(mktemp); trap 'rm -f "$installer"' EXIT; curl -fsSL https://teamwork-graph.atlassian.com/cli/install -o "$installer" && bash "$installer" --version 1.3.8 --install-dir "$HOME/.local/bin" --skip-login --skip-skills --yes`
	var selected twgProfile
	connected, err := setup.ConnectCLI(ctx, req.UI, req.System, setup.CLIConnection{
		Env: twgEnv(), Name: "twg", Service: "Jira", MinimumVersion: "1.3.8", Reason: "OAuth issue access and automatic credential refresh", InstallInstructions: install + `; export PATH="$HOME/.local/bin:$PATH"`, Install: &setup.Command{Name: "bash", Args: []string{"-o", "pipefail", "-c", install}, Env: twgEnv()},
		Login: setup.Command{Name: "twg", Args: login, Env: twgEnv()},
		Verify: func(ctx context.Context) error {
			p, e := verifyTWG(ctx, connection)
			if e != nil {
				return e
			}
			u, _ := url.Parse(site)
			if !strings.EqualFold(p.Domain, u.Hostname()) {
				return fmt.Errorf("TWG profile does not match the selected Jira site")
			}
			data, e := twgRead(ctx, connection, "jira", "workitem", "query", "--jql", "assignee = currentUser() ORDER BY updated DESC", "--fields", strings.Join(issueSearchFields(), ","), "--first", "1")
			if e == nil {
				envelope, err := decodeTWG(data, "jira.workitem.query")
				e = err
				if e == nil {
					var rows struct {
						Issues *[]json.RawMessage `json:"issues"`
					}
					if json.Unmarshal(envelope.Data, &rows) != nil || rows.Issues == nil || len(*rows.Issues) > 1 {
						e = fmt.Errorf("TWG issue read returned an invalid page")
					} else {
						for _, row := range *rows.Issues {
							_, e = decodeIssue(row)
							if e != nil {
								break
							}
						}
					}
				}
			}
			if e == nil {
				selected = p
			}
			return e
		},
	}, req.Completed)
	if err != nil || !connected {
		return false, err
	}
	use, err := req.UI.Confirm(fmt.Sprintf("Use Jira account %s (%s) at %s?", selected.User, selected.UserID, site), true)
	if err != nil || !use {
		return false, err
	}
	cfg.Jira.BaseURL, cfg.Jira.CloudID, cfg.Jira.Profile, cfg.Jira.AccountID = site, tenant.CloudID, profile, selected.UserID
	cfg.LinkingMarkPrefixes = marks
	req.UI.Print("✓ Jira connected at %s with TWG OAuth profile %s (issue read verified)\n", site, profile)
	return true, nil
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
	original := req.Config.Jira
	enabled, err := req.UI.Confirm("Connect Radar to Jira?", !req.Draft.Existing || req.Config.Jira.Enabled == nil || *req.Config.Jira.Enabled)
	if err != nil {
		return setup.Plan{}, err
	}
	req.Config.Jira.Enabled = &enabled
	if enabled {
		connected, e := configureSetup(ctx, req)
		connected = connected && e == nil
		if !connected {
			original.Enabled = &connected
			req.Config.Jira = original
		} else {
			*req.Config.Jira.Enabled = true
		}
		err = e
	}
	return setup.Plan{}, err
}
