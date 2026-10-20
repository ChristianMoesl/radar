package jira

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"radar/internal/integration"
	"radar/internal/integration/contracttest"
	"radar/internal/linking"
	"radar/internal/protocol"
)

// Explicit, read-only provider qualification through the production source and
// transport. Fixture selectors are non-secret; TWG owns credentials and refresh.
func TestLiveTWGOAuthCollection(t *testing.T) {
	path := os.Getenv("RADAR_TEST_JIRA_FIXTURES")
	if path == "" {
		t.Skip("set RADAR_TEST_JIRA_FIXTURES to approved non-secret selectors and TWG_CONFIG_DIR to the CLI store")
	}
	if os.Getenv("TWG_CONFIG_DIR") == "" {
		t.Fatal("select the existing CLI-owned store explicitly before isolating Radar config")
	}
	var fixture struct {
		BaseURL   string   `json:"base_url"`
		CloudID   string   `json:"cloud_id"`
		Profile   string   `json:"profile"`
		AccountID string   `json:"account_id"`
		Keys      []string `json:"issue_keys"`
	}
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &fixture) != nil || len(fixture.Keys) < 2 || fixture.AccountID == "" {
		t.Fatal("live qualification requires explicit approved site/profile/account and at least two issues")
	}
	var prefixes []string
	prefixSeen := map[string]bool{}
	for _, key := range fixture.Keys {
		if normalizeIssueKey(key) != key {
			t.Fatal("invalid fixture key")
		}
		prefix, _, _ := strings.Cut(key, "-")
		if !prefixSeen[prefix] {
			prefixes = append(prefixes, prefix)
			prefixSeen[prefix] = true
		}
	}
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	write := func(profile string) {
		t.Helper()
		data, _ := json.Marshal(map[string]any{"linking_mark_prefixes": prefixes, "jira": map[string]any{"enabled": true, "base_url": fixture.BaseURL, "cloud_id": fixture.CloudID, "profile": profile, "account_id": fixture.AccountID}})
		dir := filepath.Join(root, "radar")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(fixture.Profile)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	logger := jiraCollectRequest(nil).Logger
	source := NewSource()
	marks := linking.NewMarkMatcher(prefixes)
	reference := protocol.Task{ID: 1, Title: strings.Join(fixture.Keys, " ")}
	req := integration.CollectRequest{Previous: []protocol.Task{reference}, LinkingMarks: marks, Logger: logger}
	first := source.Collect(ctx, req)
	if !first.Complete || first.SourceStatus == nil || first.SourceStatus.Status != "ok" {
		t.Fatalf("live collection failed: %+v", first.SourceStatus)
	}
	seen := map[string]bool{}
	for i, o := range first.Observations {
		contracttest.AssertValidSourceRefs(t, "jira", []protocol.SourceRef{o.Ref})
		seen[o.Ref.Metadata["key"]] = true
		req.Previous = append(req.Previous, protocol.Task{ID: i + 2, Title: o.Ref.Title, SourceRefs: []protocol.SourceRef{o.Ref}})
	}
	for _, key := range fixture.Keys {
		if !seen[key] {
			t.Fatal("approved fixture absent from production collection")
		}
	}
	for _, key := range []string{"TWG_TOKEN", "TWG_USER", "TWG_AUTH_DISABLED", "TWG_OUTBOUND_AUTH", "TWG_OAUTH_ACCESS_TOKEN"} {
		t.Setenv(key, "static-fixture")
	}
	second := source.Collect(ctx, req)
	if !second.Complete || len(second.Observations) != len(first.Observations) {
		t.Fatal("inherited credential overrides defeated production OAuth collection")
	}
	connection, missing, err := loadConnection()
	if err != nil || len(missing) > 0 {
		t.Fatal("live connection could not be loaded")
	}
	quoted := []string{}
	for _, key := range fixture.Keys {
		quoted = append(quoted, `"`+key+`"`)
	}
	rows, err := searchIssues(ctx, connection, searchRequest{JQL: "key IN (" + strings.Join(quoted, ",") + ") ORDER BY key ASC", MaxResults: 1, Fields: issueSearchFields()})
	if err != nil || len(rows) != len(fixture.Keys) {
		t.Fatalf("production cursor pagination failed: rows=%d err=%v", len(rows), err)
	}
	for _, key := range fixture.Keys {
		value, err := fetchIssue(ctx, connection, key)
		if err != nil || value.Key != key {
			t.Fatalf("production get failed: %v", err)
		}
	}
	write(fixture.Profile + "-missing")
	failed := source.Collect(ctx, req)
	if failed.Complete || failed.SourceStatus == nil || failed.SourceStatus.Status != "error" || len(failed.Observations) != len(first.Observations) {
		t.Fatal("missing OAuth profile did not preserve previous Jira facts")
	}
	t.Logf("Production TWG collection, inherited-credential sanitization, cursor pages, single reads and missing-session retention passed for %d approved issue(s)", len(fixture.Keys))
}
