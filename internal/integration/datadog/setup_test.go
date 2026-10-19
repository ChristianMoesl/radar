package datadog

import (
	"context"
	"errors"
	"strings"
	"testing"

	"radar/internal/config"
	"radar/internal/integration"
	"radar/internal/setup"
	"radar/internal/setup/setuptest"
)

type pupSetupSystem struct {
	setuptest.System
	authenticated bool
	loginError    bool
	wrongContext  bool
	reads         int
}

func (s *pupSetupSystem) Output(_ context.Context, c setup.Command) (string, error) {
	s.Calls = append(s.Calls, c.Name+" "+strings.Join(c.Args, " "))
	if c.Args[0] == "--version" {
		return "pup 2.0.0", nil
	}
	if strings.Contains(strings.Join(c.Args, " "), "auth status") {
		if !s.authenticated {
			return `{"authenticated":false}`, nil
		}
		status := oauthStatus
		if s.wrongContext {
			status = strings.ReplaceAll(status, "datadoghq.eu", "us3.datadoghq.com")
		}
		return status, nil
	}
	s.reads++
	return `{"metadata":{"total_count":0,"page_count":0},"monitors":[]}`, nil
}
func (s *pupSetupSystem) Run(_ context.Context, c setup.Command) error {
	s.Calls = append(s.Calls, c.Name+" "+strings.Join(c.Args, " "))
	if s.loginError {
		return errors.New("private provider diagnostic")
	}
	s.authenticated = true
	return nil
}
func setupPup(t *testing.T, sys *pupSetupSystem, decisions []bool) (config.Config, *setuptest.UI, error) {
	t.Helper()
	cfg := config.Default()
	cfg.Datadog.Site = "us3.datadoghq.com"
	cfg.Datadog.MonitorQuery = "tag:team:old"
	cfg.Datadog.Org = "old"
	ui := &setuptest.UI{Inputs: []string{"datadoghq.eu", "radar", "tag:team:platform"}, Decisions: decisions}
	_, err := (Source{}).Setup(context.Background(), integration.SetupRequest{UI: ui, System: sys, Config: &cfg, Draft: &config.SetupDraft{}, Completed: func(v string) { ui.Completed = append(ui.Completed, v) }})
	return cfg, ui, err
}
func TestPupOnboardingStartsLoginAndVerifiesReadBeforeConnecting(t *testing.T) {
	sys := &pupSetupSystem{}
	cfg, ui, err := setupPup(t, sys, []bool{true, true})
	if err != nil || cfg.Datadog.Enabled == nil || !*cfg.Datadog.Enabled {
		t.Fatalf("cfg=%+v err=%v", cfg.Datadog, err)
	}
	if sys.reads != 1 || cfg.Datadog.Site != defaultSite || cfg.Datadog.Org != "radar" || len(ui.Completed) != 1 {
		t.Fatalf("cfg=%+v calls=%v completed=%v", cfg.Datadog, sys.Calls, ui.Completed)
	}
	if !strings.Contains(strings.Join(sys.Calls, ";"), "pup auth login --site datadoghq.eu --org radar --scopes monitors_read") {
		t.Fatal(sys.Calls)
	}
	for _, q := range ui.Questions {
		if q.Secret {
			t.Fatal("onboarding requested a credential")
		}
	}
}
func TestPupOnboardingDefersFailedLoginWithoutSavingUnverifiedContext(t *testing.T) {
	sys := &pupSetupSystem{loginError: true}
	cfg, ui, err := setupPup(t, sys, []bool{true, true, false})
	if err != nil || cfg.Datadog.Enabled == nil || *cfg.Datadog.Enabled || sys.reads != 0 {
		t.Fatalf("cfg=%+v err=%v reads=%d", cfg.Datadog, err, sys.reads)
	}
	if cfg.Datadog.Site != "us3.datadoghq.com" || cfg.Datadog.Org != "old" || cfg.Datadog.MonitorQuery != "tag:team:old" {
		t.Fatal("unverified context was saved")
	}
	if strings.Contains(ui.Transcript.String(), "private provider diagnostic") {
		t.Fatal("diagnostic leaked")
	}
}
func TestPupOnboardingReusesVerifiedSessionAndRejectsWrongContext(t *testing.T) {
	for _, wrong := range []bool{false, true} {
		sys := &pupSetupSystem{authenticated: true, wrongContext: wrong}
		cfg, _, err := setupPup(t, sys, []bool{true, false})
		if err != nil || *cfg.Datadog.Enabled == wrong {
			t.Fatalf("wrong=%v cfg=%+v err=%v", wrong, cfg.Datadog, err)
		}
		for _, call := range sys.Calls {
			if strings.Contains(call, "auth login") {
				t.Fatal("reused session started login")
			}
		}
		if wrong && sys.reads > 0 {
			t.Fatal("wrong context reached provider read")
		}
	}
}
