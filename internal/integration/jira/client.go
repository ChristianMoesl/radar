package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"radar/internal/command"
	"radar/internal/config"
	"radar/internal/setup"
)

type Config struct {
	BaseURL, CloudID, Profile, AccountID string
	output                               func(context.Context, setup.Command) (string, error)
}

func legacyEnvironmentError() error {
	for _, key := range []string{"RADAR_JIRA_EMAIL", "RADAR_JIRA_API_TOKEN", "RADAR_JIRA_ACCESS_TOKEN", "RADAR_JIRA_API_BASE_URL", "RADAR_JIRA_BASE_URL", "RADAR_JIRA_CLOUD_ID"} {
		if _, exists := os.LookupEnv(key); exists {
			return fmt.Errorf("%s is no longer supported; unset it, configure jira in config.yaml and run radar setup for TWG OAuth", key)
		}
	}
	return nil
}

func loadConnection() (Config, []string, error) {
	user, err := config.Load()
	if err != nil {
		return Config{}, nil, err
	}
	if err := legacyEnvironmentError(); err != nil {
		return Config{}, nil, err
	}
	if _, err := config.LoadSecrets(); err != nil {
		return Config{}, nil, err
	}
	cfg := Config{BaseURL: strings.TrimRight(user.Jira.BaseURL, "/"), CloudID: user.Jira.CloudID, Profile: user.Jira.Profile, AccountID: user.Jira.AccountID}
	missing := []string{}
	if _, err := exec.LookPath("twg"); err != nil {
		missing = append(missing, "twg 1.3.8+ on PATH; run radar setup")
	}
	if cfg.BaseURL == "" {
		missing = append(missing, "jira.base_url")
	}
	if cfg.CloudID == "" {
		missing = append(missing, "jira.cloud_id")
	}
	if cfg.AccountID == "" {
		missing = append(missing, "jira.account_id; run radar setup")
	}
	return cfg, missing, nil
}

// Preserve CLI-owned storage selectors and the runtime's proxy/CA trust. Strip
// all TWG credential, site, mode, output, header and endpoint overrides.
func twgEnv() []string {
	env := []string{}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "TWG_") && key != "TWG_CONFIG_DIR" && key != "TWG_AUTH_STORAGE_KEY" && key != "TWG_SECRET_STORE" {
			continue
		}
		env = append(env, entry)
	}
	return append(env, "TWG_BACKGROUND_UPDATE_CHECK=0")
}

func twgArgs(cfg Config, args ...string) []string {
	return append([]string{"--profile", cfg.Profile, "--site", cfg.CloudID, "--output", "json", "--output-summary", "none", "--api-version", "v2", "--timeout-ms", "10000"}, args...)
}

func twgOutput(ctx context.Context, cfg Config, args []string) ([]byte, error) {
	if cfg.output != nil {
		v, e := cfg.output(ctx, setup.Command{Name: "twg", Args: args, Env: twgEnv()})
		return []byte(v), e
	}
	return command.Output(ctx, "twg", args, twgEnv())
}

type twgProfile struct {
	Name        string `json:"name"`
	AuthMethod  string `json:"authMethod"`
	CloudID     string `json:"cloudId"`
	UserID      string `json:"userId"`
	User        string `json:"user"`
	Domain      string `json:"domain"`
	Environment string `json:"environment"`
}

func verifyTWG(ctx context.Context, cfg Config) (twgProfile, error) {
	version, err := twgOutput(ctx, cfg, []string{"--version"})
	if err != nil || !setup.VersionAtLeast(string(version), "1.3.8") {
		return twgProfile{}, fmt.Errorf("twg 1.3.8+ is required; run radar setup")
	}
	data, err := twgOutput(ctx, cfg, twgArgs(cfg, "auth", "profile", "list"))
	var result struct {
		APIVersion string `json:"apiVersion"`
		Data       struct {
			Profiles []twgProfile `json:"profiles"`
		} `json:"data"`
	}
	if err != nil || json.Unmarshal(data, &result) != nil || result.APIVersion != "v2" {
		return twgProfile{}, fmt.Errorf("TWG OAuth profiles are unavailable; run radar setup")
	}
	var selected *twgProfile
	for _, p := range result.Data.Profiles {
		if p.Name == cfg.Profile {
			if selected != nil {
				return twgProfile{}, fmt.Errorf("TWG profile selection is ambiguous")
			}
			copy := p
			selected = &copy
		}
	}
	if selected == nil || selected.AuthMethod != "oauth" || selected.Environment != "prod" || selected.CloudID != cfg.CloudID || selected.UserID == "" || (cfg.AccountID != "" && selected.UserID != cfg.AccountID) {
		return twgProfile{}, fmt.Errorf("TWG requires the selected OAuth profile, site and account; run radar setup")
	}
	if cfg.BaseURL != "" {
		site, err := url.Parse(cfg.BaseURL)
		if err != nil || !strings.EqualFold(selected.Domain, site.Hostname()) {
			return twgProfile{}, fmt.Errorf("TWG profile does not match jira.base_url; run radar setup")
		}
	}
	data, err = twgOutput(ctx, cfg, twgArgs(cfg, "doctor"))
	var doctor struct {
		Data struct {
			Auth struct {
				Resolved struct {
					AuthMethod string `json:"authMethod"`
				} `json:"resolved"`
			} `json:"auth"`
			Connectivity struct {
				Attempted bool   `json:"attempted"`
				OK        bool   `json:"ok"`
				UserID    string `json:"userId"`
			} `json:"connectivity"`
		} `json:"data"`
	}
	if err != nil || json.Unmarshal(data, &doctor) != nil || doctor.Data.Auth.Resolved.AuthMethod != "oauth" || !doctor.Data.Connectivity.Attempted || !doctor.Data.Connectivity.OK || !strings.HasSuffix(doctor.Data.Connectivity.UserID, "/"+selected.UserID) {
		return twgProfile{}, fmt.Errorf("TWG OAuth identity or connectivity could not be verified; run radar setup")
	}
	// The user accepts that another process can change this profile between
	// verification and the following CLI invocation. This is not an atomic guard.
	return *selected, nil
}

func twgRead(ctx context.Context, cfg Config, args ...string) ([]byte, error) {
	if _, err := verifyTWG(ctx, cfg); err != nil {
		return nil, err
	}
	return twgOutput(ctx, cfg, twgArgs(cfg, args...))
}

type twgEnvelope struct {
	APIVersion string            `json:"apiVersion"`
	Command    string            `json:"command"`
	Data       json.RawMessage   `json:"data"`
	Error      json.RawMessage   `json:"error"`
	Warnings   []json.RawMessage `json:"warnings"`
	PageInfo   *struct {
		HasNextPage *bool  `json:"hasNextPage"`
		NextCursor  string `json:"nextCursor"`
	} `json:"pageInfo"`
	Meta struct {
		Partial     bool                                                                      `json:"partial"`
		Acquisition *struct{ Requested, Returned, Succeeded, Failed, Omitted, Truncated int } `json:"acquisition"`
		Evidence    struct {
			Coverage struct {
				Status string `json:"status"`
			} `json:"coverage"`
			Truncation struct {
				Truncated bool `json:"truncated"`
			} `json:"truncation"`
		} `json:"evidence"`
	} `json:"meta"`
}

func decodeTWG(data []byte, expected string) (twgEnvelope, error) {
	var e twgEnvelope
	if json.Unmarshal(data, &e) != nil || e.APIVersion != "v2" || e.Command != expected || len(e.Data) == 0 || string(e.Data) == "null" || (len(e.Error) > 0 && string(e.Error) != "null") || len(e.Warnings) > 0 || e.Meta.Partial || e.Meta.Evidence.Truncation.Truncated || (e.Meta.Evidence.Coverage.Status != "" && e.Meta.Evidence.Coverage.Status != "complete") {
		return e, fmt.Errorf("TWG returned an invalid or incomplete Jira response; previous facts retained")
	}
	if a := e.Meta.Acquisition; a != nil && (a.Failed != 0 || a.Omitted != 0 || a.Truncated != 0 || a.Requested != a.Succeeded || a.Returned != a.Succeeded) {
		return e, fmt.Errorf("TWG returned incomplete Jira acquisition")
	}
	return e, nil
}

// TWG flattens Jira fields. Decode only that qualified wire shape, then map to
// Radar's existing domain model; there is no Jira REST response fallback.
func decodeIssue(data json.RawMessage) (issue, error) {
	var value issue
	var identity struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	if json.Unmarshal(data, &identity) != nil || json.Unmarshal(data, &value.Fields) != nil {
		return value, fmt.Errorf("invalid TWG issue")
	}
	value.ID, value.Key = identity.ID, identity.Key
	if normalizeIssueKey(value.Key) != value.Key || value.Key == "" || strings.TrimSpace(value.Fields.Summary) == "" || value.Fields.Status == nil || strings.TrimSpace(value.Fields.Status.Name) == "" || value.Fields.Status.StatusCategory == nil || value.Fields.Status.StatusCategory.Key == "" || value.Fields.IssueType == nil || value.Fields.IssueType.Name == "" {
		return issue{}, fmt.Errorf("TWG issue has invalid identity or missing state")
	}
	return value, nil
}

func fetchIssue(ctx context.Context, cfg Config, key string) (issue, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if key == "" || normalizeIssueKey(key) != key {
		return issue{}, fmt.Errorf("invalid Jira issue key")
	}
	data, err := twgRead(ctx, cfg, "jira", "workitem", "get", key, "--fields", strings.Join(issueSearchFields(), ","), "--strict")
	if err != nil {
		return issue{}, err
	}
	e, err := decodeTWG(data, "jira.workitem.get")
	if err != nil {
		return issue{}, err
	}
	var rows []json.RawMessage
	if json.Unmarshal(e.Data, &rows) != nil || len(rows) != 1 || e.Meta.Acquisition == nil || e.Meta.Acquisition.Requested != 1 || e.Meta.Acquisition.Returned != 1 {
		return issue{}, fmt.Errorf("TWG did not return the requested Jira issue")
	}
	if e.PageInfo != nil && (e.PageInfo.HasNextPage == nil || *e.PageInfo.HasNextPage || e.PageInfo.NextCursor != "") {
		return issue{}, fmt.Errorf("TWG returned an incomplete single Jira issue")
	}
	i, err := decodeIssue(rows[0])
	if err != nil {
		return issue{}, err
	}
	if i.Key != key {
		return issue{}, fmt.Errorf("TWG returned a different Jira issue")
	}
	return i, nil
}

func issueSearchFields() []string { return []string{"summary", "status", "issuetype", "priority"} }
func searchAssignedIssues(ctx context.Context, cfg Config, types []string) ([]issue, error) {
	return searchIssues(ctx, cfg, searchRequest{JQL: assignedIssuesJQL(types), MaxResults: 100, Fields: issueSearchFields()})
}
func searchIssuesByKeys(ctx context.Context, cfg Config, keys []string) ([]issue, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	quoted := []string{}
	for _, key := range keys {
		if normalizeIssueKey(key) != key || key == "" {
			return nil, fmt.Errorf("invalid discovered Jira key")
		}
		quoted = append(quoted, `"`+key+`"`)
	}
	return searchIssues(ctx, cfg, searchRequest{JQL: "key IN (" + strings.Join(quoted, ", ") + ")", MaxResults: len(keys), Fields: issueSearchFields()})
}
func searchIssues(ctx context.Context, cfg Config, request searchRequest) ([]issue, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	size := request.MaxResults
	if size <= 0 || size > 100 {
		size = 100
	}
	var issues []issue
	seen := map[string]bool{}
	cursors := map[string]bool{}
	cursor := ""
	bytes := 0
	for page := 0; page < 100; page++ {
		args := []string{"jira", "workitem", "query", "--jql", request.JQL, "--fields", strings.Join(request.Fields, ","), "--first", fmt.Sprint(size)}
		if cursor != "" {
			args = append(args, "--after", cursor)
		}
		data, err := twgRead(ctx, cfg, args...)
		if err != nil {
			return nil, err
		}
		bytes += len(data)
		if bytes > 20<<20 {
			return nil, fmt.Errorf("TWG Jira search exceeded 20 MiB; previous facts retained")
		}
		e, err := decodeTWG(data, "jira.workitem.query")
		if err != nil {
			return nil, err
		}
		var result struct {
			Issues *[]json.RawMessage `json:"issues"`
		}
		if json.Unmarshal(e.Data, &result) != nil || result.Issues == nil || len(*result.Issues) > size {
			return nil, fmt.Errorf("TWG returned an invalid Jira query page")
		}
		for _, row := range *result.Issues {
			i, err := decodeIssue(row)
			if err != nil {
				return nil, err
			}
			if seen[i.Key] {
				return nil, fmt.Errorf("TWG repeated a Jira issue across pages")
			}
			seen[i.Key] = true
			issues = append(issues, i)
		}
		if e.PageInfo == nil {
			return issues, nil
		}
		if e.PageInfo.HasNextPage == nil {
			return nil, fmt.Errorf("TWG omitted Jira pagination completeness")
		}
		if !*e.PageInfo.HasNextPage {
			if e.PageInfo.NextCursor != "" {
				return nil, fmt.Errorf("TWG returned contradictory Jira pagination")
			}
			return issues, nil
		}
		cursor = e.PageInfo.NextCursor
		if len(*result.Issues) == 0 || cursor == "" || cursors[cursor] {
			return nil, fmt.Errorf("TWG Jira pagination stalled or is incomplete")
		}
		cursors[cursor] = true
	}
	return nil, fmt.Errorf("TWG Jira pagination exceeded 100 pages; previous facts retained")
}
