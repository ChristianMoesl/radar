package jira

import (
	"context"
	"errors"
	"strings"
	"testing"

	"radar/internal/setup"
)

const validTWGIssue = `{"id":"1","key":"ABC-1","summary":"Ship work","issuetype":{"name":"Task"},"status":{"name":"Open","statusCategory":{"key":"new"}},"priority":null}`

func testTWGClient(t *testing.T, read func(setup.Command) (string, error)) Config {
	t.Helper()
	return Config{BaseURL: "https://jira.example.test", CloudID: "cloud-fixture", Profile: "radar", AccountID: "fixture-account", output: func(ctx context.Context, c setup.Command) (string, error) {
		if len(c.Args) == 1 && c.Args[0] == "--version" {
			return "1.3.8", nil
		}
		args := strings.Join(c.Args, " ")
		if !strings.Contains(args, "--profile radar --site cloud-fixture") {
			t.Fatal("request did not pin profile and Cloud ID")
		}
		if strings.Contains(args, "auth profile list") {
			return `{"apiVersion":"v2","data":{"profiles":[{"name":"radar","authMethod":"oauth","environment":"prod","cloudId":"cloud-fixture","userId":"fixture-account","domain":"jira.example.test"}]}}`, nil
		}
		if strings.Contains(args, "doctor") {
			return `{"data":{"auth":{"resolved":{"authMethod":"oauth"}},"connectivity":{"attempted":true,"ok":true,"userId":"ari:cloud:identity::user/fixture-account"}}}`, nil
		}
		return read(c)
	}}
}

func TestTWGQueryConsumesEveryCursorAndTerminalEmptyPage(t *testing.T) {
	calls := 0
	checks := 0
	cfg := testTWGClient(t, func(c setup.Command) (string, error) {
		calls++
		if !strings.Contains(strings.Join(c.Args, " "), "--fields summary,status,issuetype,priority --first 1") {
			t.Fatal("query fields/page size changed")
		}
		if calls == 1 {
			return `{"apiVersion":"v2","command":"jira.workitem.query","data":{"issues":[` + validTWGIssue + `]},"pageInfo":{"hasNextPage":true,"nextCursor":"next"}}`, nil
		}
		if !strings.Contains(strings.Join(c.Args, " "), "--after next") {
			t.Fatal("continuation cursor missing")
		}
		return `{"apiVersion":"v2","command":"jira.workitem.query","data":{"issues":[]}}`, nil
	})
	original := cfg.output
	cfg.output = func(ctx context.Context, c setup.Command) (string, error) {
		if strings.Contains(strings.Join(c.Args, " "), "auth profile list") {
			checks++
		}
		return original(ctx, c)
	}
	rows, err := searchIssues(context.Background(), cfg, searchRequest{JQL: "project = ABC", MaxResults: 1, Fields: issueSearchFields()})
	if err != nil || len(rows) != 1 || rows[0].Key != "ABC-1" || calls != 2 || checks != 2 {
		t.Fatalf("rows=%d calls=%d checks=%d error=%v", len(rows), calls, checks, err)
	}
}

func TestTWGQueryRejectsPartialMalformedAndStalledResults(t *testing.T) {
	for name, response := range map[string]string{
		"missing rows":          `{"apiVersion":"v2","command":"jira.workitem.query","data":{}}`,
		"rest shape":            `{"issues":[]}`,
		"nested REST fields":    `{"apiVersion":"v2","command":"jira.workitem.query","data":{"issues":[{"key":"ABC-1","fields":{"summary":"incompatible"}}]}}`,
		"missing state":         `{"apiVersion":"v2","command":"jira.workitem.query","data":{"issues":[{"key":"ABC-1","summary":"partial"}]}}`,
		"missing cursor":        `{"apiVersion":"v2","command":"jira.workitem.query","data":{"issues":[` + validTWGIssue + `]},"pageInfo":{"hasNextPage":true}}`,
		"missing completeness":  `{"apiVersion":"v2","command":"jira.workitem.query","data":{"issues":[]},"pageInfo":{}}`,
		"contradictory cursor":  `{"apiVersion":"v2","command":"jira.workitem.query","data":{"issues":[]},"pageInfo":{"hasNextPage":false,"nextCursor":"next"}}`,
		"empty continuing page": `{"apiVersion":"v2","command":"jira.workitem.query","data":{"issues":[]},"pageInfo":{"hasNextPage":true,"nextCursor":"next"}}`,
		"duplicate page":        `{"apiVersion":"v2","command":"jira.workitem.query","data":{"issues":[` + validTWGIssue + `]},"pageInfo":{"hasNextPage":true,"nextCursor":"next"}}`,
		"partial evidence":      `{"apiVersion":"v2","command":"jira.workitem.query","data":{"issues":[]},"meta":{"partial":true}}`,
		"warnings":              `{"apiVersion":"v2","command":"jira.workitem.query","data":{"issues":[]},"warnings":["private-value"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			cfg := testTWGClient(t, func(setup.Command) (string, error) { return response, nil })
			rows, err := searchAssignedIssues(context.Background(), cfg, []string{"Task"})
			if err == nil || len(rows) != 0 || strings.Contains(err.Error(), "private-value") {
				t.Fatalf("accepted incomplete page or disclosed output: %v", err)
			}
		})
	}
}

func TestTWGRejectsWrongProfileAccountSiteAndAuthenticationBeforeIssueReads(t *testing.T) {
	for _, change := range []string{"pat", "wrong account", "wrong cloud", "wrong domain", "wrong profile", "failed connectivity", "resolved PAT", "old version"} {
		t.Run(change, func(t *testing.T) {
			reads := 0
			cfg := testTWGClient(t, func(setup.Command) (string, error) { reads++; return "", nil })
			original := cfg.output
			cfg.output = func(ctx context.Context, c setup.Command) (string, error) {
				s, e := original(ctx, c)
				args := strings.Join(c.Args, " ")
				switch change {
				case "old version":
					if args == "--version" {
						s = "1.3.7"
					}
				case "pat":
					if strings.Contains(args, "auth profile list") {
						s = strings.Replace(s, `"authMethod":"oauth"`, `"authMethod":"pat"`, 1)
					}
				case "wrong account":
					s = strings.ReplaceAll(s, "fixture-account", "other-account")
				case "wrong cloud":
					s = strings.ReplaceAll(s, "cloud-fixture", "other-cloud")
				case "wrong domain":
					s = strings.ReplaceAll(s, "jira.example.test", "other.example.test")
				case "wrong profile":
					s = strings.ReplaceAll(s, `"name":"radar"`, `"name":"other"`)
				case "failed connectivity":
					s = strings.ReplaceAll(s, `"ok":true`, `"ok":false`)
				case "resolved PAT":
					if strings.Contains(args, "doctor") {
						s = strings.ReplaceAll(s, `"authMethod":"oauth"`, `"authMethod":"pat"`)
					}
				}
				return s, e
			}
			if _, err := searchAssignedIssues(context.Background(), cfg, []string{"Task"}); err == nil || reads != 0 {
				t.Fatalf("unverified connection dispatched %d reads: %v", reads, err)
			}
		})
	}
}

func TestTWGEnvironmentDiscardsCredentialAndModeOverrides(t *testing.T) {
	for _, key := range []string{"TWG_TOKEN", "TWG_USER", "TWG_PROFILE", "TWG_SITE", "TWG_ENV", "TWG_OUTBOUND_AUTH", "TWG_AUTH_DISABLED", "TWG_OAUTH_ACCESS_TOKEN", "TWG_CUSTOM_HEADERS", "TWG_ENDPOINT", "TWG_TOKEN_MODE"} {
		t.Setenv(key, "private-fixture")
	}
	t.Setenv("TWG_CONFIG_DIR", "/fixture/cli-store")
	t.Setenv("HTTPS_PROXY", "http://proxy.invalid:8080")
	env := strings.Join(twgEnv(), "\n")
	if strings.Contains(env, "private-fixture") || !strings.Contains(env, "TWG_CONFIG_DIR=/fixture/cli-store") || !strings.Contains(env, "HTTPS_PROXY=http://proxy.invalid:8080") {
		t.Fatal("credential sanitization or runtime proxy/storage selection changed")
	}
}

func TestTWGFailedGetAndQueryDoNotReturnPartialFacts(t *testing.T) {
	cfg := testTWGClient(t, func(setup.Command) (string, error) { return `private-provider-output`, errors.New("request failed") })
	if rows, err := searchAssignedIssues(context.Background(), cfg, []string{"Task"}); err == nil || len(rows) > 0 {
		t.Fatal("failed query accepted")
	}
	if _, err := fetchIssue(context.Background(), cfg, "ABC-1"); err == nil {
		t.Fatal("failed get accepted")
	}
	cfg = testTWGClient(t, func(setup.Command) (string, error) {
		return `{"apiVersion":"v2","command":"jira.workitem.get","data":[` + validTWGIssue + `],"meta":{"acquisition":{"requested":1,"returned":1,"succeeded":1}}}`, nil
	})
	if value, err := fetchIssue(context.Background(), cfg, "ABC-1"); err != nil || value.Fields.Status.StatusCategory.Key != "new" {
		t.Fatalf("single get contract failed: %v", err)
	}
	if _, err := fetchIssue(context.Background(), cfg, "ABC-2"); err == nil {
		t.Fatal("wrong identity accepted")
	}
}

func TestRetiredJiraInputsAreRejected(t *testing.T) {
	for _, key := range []string{"RADAR_JIRA_EMAIL", "RADAR_JIRA_API_TOKEN", "RADAR_JIRA_ACCESS_TOKEN", "RADAR_JIRA_API_BASE_URL", "RADAR_JIRA_BASE_URL", "RADAR_JIRA_CLOUD_ID"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, "private-fixture")
			err := legacyEnvironmentError()
			if err == nil || !strings.Contains(err.Error(), key) || strings.Contains(err.Error(), "private-fixture") {
				t.Fatal("retired input accepted or disclosed")
			}
		})
	}
}
