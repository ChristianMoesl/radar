package datadog

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"radar/internal/setup"
)

const oauthStatus = `{"authenticated":true,"has_refresh":true,"token_type":"Bearer","site":"datadoghq.eu","org":"radar","status":"valid","scopes":["monitors_read"]}`

func pupFixture(t *testing.T, body string) (apiClient, *[]setup.Command) {
	t.Helper()
	calls := []setup.Command{}
	return apiClient{output: func(ctx context.Context, c setup.Command) (string, error) {
		calls = append(calls, c)
		if len(c.Args) > 0 && c.Args[0] == "--version" {
			return "pup 2.0.0", nil
		}
		if strings.Contains(strings.Join(c.Args, " "), "auth status") {
			return oauthStatus, nil
		}
		return body, nil
	}}, &calls
}
func TestPupSearchUsesOneScopedMonitorRequest(t *testing.T) {
	c, calls := pupFixture(t, `{"metadata":{"total_count":1,"page_count":1},"monitors":[{"id":42,"name":"API errors","status":"Alert"}]}`)
	r, err := c.Search(context.Background(), connection{Site: defaultSite, Org: "radar"}, "tag:team:platform", []string{"Alert", "Warn", "No Data"})
	if err != nil || len(r.Monitors) != 1 || r.Monitors[0].ID != 42 {
		t.Fatalf("response=%+v err=%v", r, err)
	}
	if len(*calls) != 3 {
		t.Fatal(*calls)
	}
	want := []string{"--org", "radar", "--no-envelope", "--output", "json", "api", "/api/v1/monitor/search", "-f", `query=(tag:team:platform) status:(Alert OR Warn OR "No Data")`, "-F", "page=0", "-F", "per_page=1000"}
	if !reflect.DeepEqual((*calls)[2].Args, want) {
		t.Fatalf("args=%q", (*calls)[2].Args)
	}
}
func TestPupSearchRejectsMissingOrMalformedCompletenessEvidence(t *testing.T) {
	for _, body := range []string{`null`, `{}`, `{"errors":["partial"],"metadata":{"total_count":0,"page_count":0},"monitors":[]}`, `{"data":{"monitors":[]}}`, `{"metadata":{},"monitors":[]}`, `{"metadata":{"total_count":0,"page_count":1},"monitors":[{"id":1,"status":"Alert"}]}`, `{"metadata":{"total_count":1,"page_count":1},"monitors":[{"status":"Alert"}]}`, `{"metadata":{"total_count":2,"page_count":1},"monitors":[{"id":1,"status":"Alert"},{"id":1,"status":"Alert"}]}`} {
		c, _ := pupFixture(t, body)
		if _, err := c.Search(context.Background(), connection{Site: defaultSite, Org: "radar"}, "tag:team:platform", []string{"Alert"}); err == nil {
			t.Errorf("accepted %s", body)
		}
	}
}
func TestPupRejectsStaticExpiredWrongContextAndMissingScope(t *testing.T) {
	var valid map[string]any
	if err := json.Unmarshal([]byte(oauthStatus), &valid); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"authenticated", "auth_method", "token_type", "has_refresh", "site", "org", "status", "scopes"} {
		t.Run(field, func(t *testing.T) {
			payload := map[string]any{}
			for k, v := range valid {
				payload[k] = v
			}
			switch field {
			case "authenticated", "has_refresh":
				payload[field] = false
			case "scopes":
				payload[field] = []string{"other_read"}
			default:
				payload[field] = "wrong"
			}
			body, _ := json.Marshal(payload)
			c := apiClient{output: func(_ context.Context, cmd setup.Command) (string, error) {
				if cmd.Args[0] == "--version" {
					return "pup 2.0.0", nil
				}
				if strings.Contains(strings.Join(cmd.Args, " "), "auth status") {
					return string(body), nil
				}
				t.Fatal("API request ran without verified OAuth")
				return "", nil
			}}
			if _, err := c.Search(context.Background(), connection{Site: defaultSite, Org: "radar"}, "tag:team:platform", []string{"Alert"}); err == nil {
				t.Fatal("invalid auth accepted")
			}
		})
	}
}
func TestPupEnvironmentCannotUseStaticCredentialsOrRedirectRequests(t *testing.T) {
	for _, key := range []string{"DD_ACCESS_TOKEN", "DD_API_KEY", "DD_APP_KEY", "DD_SITE", "DD_ORG", "PUP_FILTER", "PUP_MOCK_SERVER"} {
		t.Setenv(key, "must-not-be-used")
	}
	env := map[string]string{}
	for _, entry := range pupEnv(defaultSite, "radar") {
		k, v, _ := strings.Cut(entry, "=")
		env[k] = v
	}
	for _, key := range []string{"DD_ACCESS_TOKEN", "PUP_FILTER", "PUP_MOCK_SERVER"} {
		if _, ok := env[key]; ok {
			t.Errorf("inherited %s", key)
		}
	}
	if env["DD_SITE"] != defaultSite || env["DD_ORG"] != "radar" || env["DD_API_KEY"] != "\n" || env["DD_APP_KEY"] != "\n" {
		t.Fatal("OAuth fallback guard missing")
	}
}
func TestConnectionUsesOnlyConfiguredContextAndCLI(t *testing.T) {
	configureDatadog(t, "tag:team:platform")
	configureDatadogCLI(t)
	cfg, missing, err := loadConnection()
	if err != nil || len(missing) != 0 || cfg.Site != defaultSite || cfg.Org != "radar" {
		t.Fatalf("cfg=%+v missing=%v err=%v", cfg, missing, err)
	}
	t.Setenv("PATH", t.TempDir())
	if _, missing, err := loadConnection(); err != nil || len(missing) != 1 {
		t.Fatalf("missing=%v err=%v", missing, err)
	}
}

func TestConnectionRejectsRetiredEnvironmentWithoutUsingIt(t *testing.T) {
	for _, key := range []string{"RADAR_DATADOG_SITE", "RADAR_DATADOG_API_KEY", "RADAR_DATADOG_APP_KEY"} {
		t.Run(key, func(t *testing.T) {
			configureDatadog(t, "tag:team:platform")
			t.Setenv(key, "private-fixture")
			_, _, err := loadConnection()
			if err == nil || !strings.Contains(err.Error(), "no longer supported") || strings.Contains(err.Error(), "private-fixture") {
				t.Fatal(err)
			}
		})
	}
}
func TestPupFailureDoesNotExposeDiagnostics(t *testing.T) {
	c := apiClient{output: func(context.Context, setup.Command) (string, error) {
		return "private-secret", errors.New("private-secret")
	}}
	_, err := c.Search(context.Background(), connection{Site: defaultSite, Org: "radar"}, "tag:team:platform", []string{"Alert"})
	if err == nil || strings.Contains(err.Error(), "private-secret") {
		t.Fatal(err)
	}
}
func TestConnectionRejectsUnsupportedSite(t *testing.T) {
	configureDatadog(t, "tag:team:platform")
	path := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "radar", "config.yaml")
	if err := os.WriteFile(path, []byte("datadog:\n  site: invalid.example.test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadConnection(); err == nil {
		t.Fatal("unsupported site accepted")
	}
}
func TestRegionalDatadogAppURL(t *testing.T) {
	if got := datadogAppBaseURL("us3.datadoghq.com"); got != "https://us3.datadoghq.com" {
		t.Fatal(got)
	}
}
