package jira

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"radar/internal/config"
	"radar/internal/integration"
	"radar/internal/setup"
	"radar/internal/setup/setuptest"
)

type twgSetupSystem struct {
	setuptest.System
	authenticated, loginError, wrongSite bool
	reads, logins, installs              int
}

func (s *twgSetupSystem) Output(ctx context.Context, c setup.Command) (string, error) {
	if c.Name != "twg" {
		return "", errors.New("unexpected tool")
	}
	args := strings.Join(c.Args, " ")
	s.Calls = append(s.Calls, args)
	if len(c.Args) == 1 && c.Args[0] == "--version" {
		return "1.3.8", nil
	}
	cfg := testTWGClientForSetup(s.authenticated, s.wrongSite)
	if strings.Contains(args, "auth profile list") {
		return cfg, nil
	}
	if strings.Contains(args, "doctor") {
		return `{"data":{"auth":{"resolved":{"authMethod":"oauth"}},"connectivity":{"attempted":true,"ok":true,"userId":"ari:cloud:identity::user/fixture-account"}}}`, nil
	}
	s.reads++
	return `{"apiVersion":"v2","command":"jira.workitem.query","data":{"issues":[]}}`, nil
}
func testTWGClientForSetup(authenticated, wrong bool) string {
	if !authenticated {
		return `{"apiVersion":"v2","data":{"profiles":[]}}`
	}
	site := "jira.example.test"
	if wrong {
		site = "wrong.example.test"
	}
	return `{"apiVersion":"v2","data":{"profiles":[{"name":"radar","authMethod":"oauth","environment":"prod","cloudId":"cloud-fixture","userId":"fixture-account","user":"fixture@example.invalid","domain":"` + site + `"}]}}`
}
func (s *twgSetupSystem) Run(_ context.Context, c setup.Command) error {
	s.Calls = append(s.Calls, c.Name+" "+strings.Join(c.Args, " "))
	if c.Name == "bash" {
		s.installs++
		delete(s.Missing, "twg")
		return nil
	}
	s.logins++
	if s.loginError {
		return errors.New("private provider diagnostic")
	}
	s.authenticated = true
	return nil
}

type jiraDiscovery func(*http.Request) (*http.Response, error)

func (f jiraDiscovery) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func setupTWG(t *testing.T, sys *twgSetupSystem, decisions []bool) (config.Config, *setuptest.UI, error) {
	t.Helper()
	cfg := config.Default()
	cfg.Jira.BaseURL = "https://old.example.test"
	cfg.Jira.CloudID = "old-cloud"
	cfg.Jira.AccountID = "old-account"
	ui := &setuptest.UI{Inputs: []string{"https://jira.example.test", "radar", "ABC"}, Decisions: decisions}
	client := &http.Client{Transport: jiraDiscovery(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "" || r.URL.Path != "/_edge/tenant_info" {
			t.Fatal("unexpected authenticated HTTP request")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"cloudId":"cloud-fixture"}`))}, nil
	})}
	_, err := NewSource().Setup(context.Background(), integration.SetupRequest{Config: &cfg, Draft: &config.SetupDraft{}, UI: ui, System: sys, HTTP: client, Completed: func(v string) { ui.Completed = append(ui.Completed, v) }})
	return cfg, ui, err
}
func TestTWGOnboardingInstallsRechecksLogsInAndVerifiesIdentityAndRead(t *testing.T) {
	sys := &twgSetupSystem{System: setuptest.System{Missing: map[string]bool{"twg": true}}}
	cfg, ui, err := setupTWG(t, sys, []bool{true, true, true, true, true})
	if err != nil || cfg.Jira.Enabled == nil || !*cfg.Jira.Enabled || cfg.Jira.AccountID != "fixture-account" || sys.installs != 1 || sys.logins != 1 || sys.reads != 1 {
		t.Fatalf("setup failed: %+v installs=%d logins=%d reads=%d err=%v", cfg.Jira, sys.installs, sys.logins, sys.reads, err)
	}
	if !strings.Contains(strings.Join(sys.Calls, " "), "--new-profile radar --site cloud-fixture") {
		t.Fatal("login did not select a dedicated profile/site")
	}
	for _, q := range ui.Questions {
		if q.Secret {
			t.Fatal("setup asked for a credential")
		}
	}
}
func TestTWGOnboardingDefersFailedLoginAndWrongContextWithoutSaving(t *testing.T) {
	for _, wrong := range []bool{false, true} {
		t.Run(map[bool]string{false: "login failure", true: "wrong site"}[wrong], func(t *testing.T) {
			sys := &twgSetupSystem{authenticated: wrong, wrongSite: wrong, loginError: !wrong}
			decisions := []bool{true, true, false}
			if wrong {
				decisions = []bool{true, false}
			}
			cfg, ui, err := setupTWG(t, sys, decisions)
			if err != nil || cfg.Jira.Enabled == nil || *cfg.Jira.Enabled || cfg.Jira.BaseURL != "https://old.example.test" || cfg.Jira.AccountID != "old-account" || sys.reads != 0 {
				t.Fatalf("unverified connection saved: %+v reads=%d err=%v", cfg.Jira, sys.reads, err)
			}
			if strings.Contains(ui.Transcript.String(), "private provider diagnostic") {
				t.Fatal("diagnostic leaked")
			}
		})
	}
}
