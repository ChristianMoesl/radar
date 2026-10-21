package jira

import (
	"context"
	"encoding/json"
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
	needsLogin                           bool
	profiles                             []twgProfile
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
		if s.profiles != nil {
			data, err := json.Marshal(map[string]any{"apiVersion": "v2", "data": map[string]any{"profiles": s.profiles}})
			return string(data), err
		}
		return cfg, nil
	}
	if strings.Contains(args, "doctor") {
		if s.needsLogin {
			return "", errors.New("login required")
		}
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
	s.needsLogin = false
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
	ui := &setuptest.UI{Inputs: []string{"https://jira.example.test", "<keep>", "ABC"}, Decisions: decisions}
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

func TestTWGOnboardingReusesExistingSiteProfile(t *testing.T) {
	for _, needsLogin := range []bool{false, true} {
		t.Run(map[bool]string{false: "already authenticated", true: "reauthentication"}[needsLogin], func(t *testing.T) {
			profile := twgProfile{Name: "example-oauth", AuthMethod: "oauth", Environment: "prod", CloudID: "cloud-fixture", Domain: "jira.example.test", UserID: "fixture-account", User: "fixture@example.invalid"}
			sys := &twgSetupSystem{profiles: []twgProfile{profile}, needsLogin: needsLogin}
			decisions := []bool{true, true}
			if needsLogin {
				decisions = []bool{true, true, true}
			}
			cfg, ui, err := setupTWG(t, sys, decisions)
			if err != nil || cfg.Jira.Enabled == nil || !*cfg.Jira.Enabled || cfg.Jira.Profile != profile.Name || cfg.Jira.AccountID != profile.UserID || sys.reads != 1 {
				t.Fatalf("existing profile was not reused: %+v reads=%d err=%v", cfg.Jira, sys.reads, err)
			}
			calls := strings.Join(sys.Calls, "\n")
			if strings.Contains(calls, "--new-profile") || !strings.Contains(calls, "--profile example-oauth --site cloud-fixture") {
				t.Fatalf("setup did not pin the existing profile: %s", calls)
			}
			if needsLogin {
				if sys.logins != 1 || !strings.Contains(calls, "twg --profile example-oauth login --force --site cloud-fixture") {
					t.Fatalf("reauthentication did not target the existing profile: %s", calls)
				}
			} else if sys.logins != 0 {
				t.Fatal("authenticated profile triggered an unnecessary login")
			}
			if !strings.Contains(ui.Transcript.String(), "example-oauth") {
				t.Fatal("selected profile was not displayed")
			}
		})
	}
}

func TestTWGOnboardingProfileDefaultMatchesOAuthSiteAndKeepsConfiguredProfile(t *testing.T) {
	matching := twgProfile{Name: "example-oauth", AuthMethod: "oauth", Environment: "prod", CloudID: "cloud-fixture", Domain: "jira.example.test", UserID: "fixture-account", User: "fixture@example.invalid"}
	for _, change := range []struct {
		name   string
		mutate func(*twgProfile)
	}{
		{"another cloud", func(p *twgProfile) { p.CloudID = "another-cloud" }},
		{"another domain", func(p *twgProfile) { p.Domain = "another.example.test" }},
		{"another environment", func(p *twgProfile) { p.Environment = "stg" }},
		{"PAT", func(p *twgProfile) { p.AuthMethod = "pat" }},
		{"configured profile", func(p *twgProfile) { p.Name = "radar" }},
	} {
		t.Run(change.name, func(t *testing.T) {
			profile := matching
			change.mutate(&profile)
			sys := &twgSetupSystem{profiles: []twgProfile{matching, profile}}
			// Inspect the proposed default without completing or saving setup.
			cfg := config.Default()
			ui := &setuptest.UI{Inputs: []string{"https://jira.example.test"}, CancelAt: "TWG OAuth profile name"}
			client := &http.Client{Transport: jiraDiscovery(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"cloudId":"cloud-fixture"}`))}, nil
			})}
			_, err := configureSetup(context.Background(), integration.SetupRequest{Config: &cfg, UI: ui, System: sys, HTTP: client})
			if !errors.Is(err, setup.ErrAborted) {
				t.Fatalf("expected cancellation, got %v", err)
			}
			want := matching.Name
			if change.name == "configured profile" {
				want = "radar"
			}
			if got := ui.Questions[len(ui.Questions)-1].Initial; got != want {
				t.Fatalf("profile default = %q, want %q", got, want)
			}
			if sys.logins != 0 || sys.reads != 0 || cfg.Jira.CloudID != "" || cfg.Jira.AccountID != "" {
				t.Fatal("cancelled profile selection changed the connection")
			}
		})
	}
}
