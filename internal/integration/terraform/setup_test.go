package terraform

import (
	"context"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"radar/internal/config"
	"radar/internal/integration"
	"radar/internal/setup"
	"radar/internal/setup/setuptest"
)

type terraformSetupSystem struct {
	setuptest.System
	api         *fixtureAPI
	profiles    string
	profilesErr error
	createErr   error
	onCreate    func()
	onLogin     func()
}

func (s *terraformSetupSystem) Output(ctx context.Context, c setup.Command) (string, error) {
	s.Calls = append(s.Calls, c.Name+" "+strings.Join(c.Args, " "))
	if strings.Join(c.Args, " ") == "profile profiles list --json --no-color" {
		if s.profiles != "" || s.profilesErr != nil {
			return s.profiles, s.profilesErr
		}
		return `[{"Name":"default","Hostname":"app.terraform.io","Active":true}]`, nil
	}
	return s.api.output(ctx, c)
}

func (s *terraformSetupSystem) Run(ctx context.Context, c setup.Command) error {
	err := s.System.Run(ctx, c)
	if err == nil && c.Name == "tfctl" && len(c.Args) > 2 && strings.Join(c.Args[:3], " ") == "profile profiles create" {
		if s.createErr != nil {
			return s.createErr
		}
		if s.onCreate != nil {
			s.onCreate()
		}
	}
	if err == nil && c.Name == "tfctl" && len(c.Args) > 1 && c.Args[0] == "auth" && c.Args[1] == "login" && s.onLogin != nil {
		s.onLogin()
	}
	return err
}

func TestSetupReusesLoginAndVerifiesScope(t *testing.T) {
	_, api, original := fixtureSource(t)
	api.workspaces = func(*url.URL) (string, error) {
		return encode(map[string]any{
			"data":  []resource{workspaceFixture("ws-two", "staging-network"), workspaceFixture("ws-one", "production-network")},
			"links": map[string]any{"next": nil},
		}), nil
	}
	cfg := config.Default()
	cfg.Terraform = original
	ui := &setuptest.UI{Inputs: []string{"default", "acme"}, Selections: [][]string{{"production-network"}}, Decisions: []bool{true}}
	sys := &terraformSetupSystem{api: api}
	_, err := (Source{}).Setup(context.Background(), integration.SetupRequest{Config: &cfg, UI: ui, System: sys})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Terraform.Enabled == nil || !*cfg.Terraform.Enabled || cfg.Terraform.Organization != "acme" || !reflect.DeepEqual(cfg.Terraform.Workspaces, []string{"production-network"}) {
		t.Fatal(cfg.Terraform)
	}
	for _, call := range sys.Calls {
		if strings.Contains(call, "auth login") || strings.Contains(call, "profile profiles") {
			t.Fatal("working login should not need profile preparation or authentication")
		}
	}
	if len(ui.MultiQuestions) != 1 || !reflect.DeepEqual(ui.MultiQuestions[0].Initial, original.Workspaces) || !reflect.DeepEqual(ui.MultiQuestions[0].Choices, []setup.Choice{
		{Value: "production-network", Label: "production-network"}, {Value: "staging-network", Label: "staging-network"},
	}) {
		t.Fatal("discovered choices or initial selection missing", ui.MultiQuestions)
	}
	if !reflect.DeepEqual(api.calls, []string{
		"/account/details",
		"/organizations/acme/workspaces?page%5Bnumber%5D=1&page%5Bsize%5D=100",
		"/account/details",
		"/organizations/acme/workspaces/production-network",
	}) {
		t.Fatal("identity, discovery and selected read must run in order without reading unselected workspaces", api.calls)
	}
	if !strings.Contains(ui.Transcript.String(), "verified") {
		t.Fatal("scope not verified")
	}
}

func TestSetupDeferPreservesScopeAndDisablesConnection(t *testing.T) {
	_, api, cfgTF := fixtureSource(t)
	api.service = true
	cfg := config.Default()
	cfg.Terraform = cfgTF
	ui := &setuptest.UI{Inputs: []string{"default", "acme"}, Decisions: []bool{true, false}}
	sys := &terraformSetupSystem{api: api}
	_, err := (Source{}).Setup(context.Background(), integration.SetupRequest{Config: &cfg, UI: ui, System: sys})
	if err != nil || cfg.Terraform.Enabled == nil || *cfg.Terraform.Enabled || cfg.Terraform.Organization != cfgTF.Organization {
		t.Fatal(cfg.Terraform, err)
	}
}

func TestSetupAuthenticatesBeforeWorkspaceDiscovery(t *testing.T) {
	_, api, _ := fixtureSource(t)
	api.auth, api.authErr = `{"active":false}`, fmt.Errorf("not logged in")
	cfg := config.Default()
	ui := &setuptest.UI{Inputs: []string{"default", "acme"}, Selections: [][]string{{"production-network"}}, Decisions: []bool{true, true}}
	loggedIn := false
	sys := &terraformSetupSystem{api: api, onLogin: func() {
		loggedIn = true
		api.auth, api.authErr = "", nil
	}}
	api.workspaces = func(*url.URL) (string, error) {
		if !loggedIn {
			t.Fatal("discovery attempted before login")
		}
		return `{"data":[{"type":"workspaces","id":"ws-one","attributes":{"name":"production-network"}}],"links":{"next":null}}`, nil
	}
	_, err := (Source{}).Setup(context.Background(), integration.SetupRequest{Config: &cfg, UI: ui, System: sys})
	if err != nil || !loggedIn || cfg.Terraform.Enabled == nil || !*cfg.Terraform.Enabled {
		t.Fatal("connection failed", cfg.Terraform, loggedIn, err)
	}
}

func TestSetupWorkspaceFailuresCanBeDeferredWithoutLosingScope(t *testing.T) {
	for _, failure := range []string{"empty organization", "discovery unavailable", "selected workspace unavailable"} {
		t.Run(failure, func(t *testing.T) {
			_, api, original := fixtureSource(t)
			cfg := config.Default()
			cfg.Terraform = original
			ui := &setuptest.UI{Inputs: []string{"default", "acme"}, Decisions: []bool{true, false}}
			api.workspaces = func(*url.URL) (string, error) {
				switch failure {
				case "empty organization":
					return `{"data":[],"links":{"next":null}}`, nil
				case "discovery unavailable":
					return "private diagnostic", fmt.Errorf("private diagnostic")
				default:
					return encode(map[string]any{"data": []resource{workspaceFixture("ws-missing", "missing")}, "links": map[string]any{"next": nil}}), nil
				}
			}
			if failure == "selected workspace unavailable" {
				ui.Selections = [][]string{{"missing"}}
			}
			_, err := (Source{}).Setup(context.Background(), integration.SetupRequest{Config: &cfg, UI: ui, System: &terraformSetupSystem{api: api}})
			original.Enabled = new(false)
			if err != nil || !reflect.DeepEqual(cfg.Terraform, original) || strings.Contains(ui.Transcript.String(), "private diagnostic") {
				t.Fatal("defer did not safely preserve existing scope", cfg.Terraform, err, ui.Transcript.String())
			}
			if failure == "empty organization" && !strings.Contains(ui.Transcript.String(), "no accessible workspaces") {
				t.Fatal("empty organization not explained")
			}
		})
	}
}

func TestSetupWorkspaceDiscoveryCanBeRetried(t *testing.T) {
	_, api, _ := fixtureSource(t)
	attempts := 0
	api.workspaces = func(*url.URL) (string, error) {
		attempts++
		if attempts == 1 {
			return "", fmt.Errorf("network unavailable")
		}
		return `{"data":[{"type":"workspaces","id":"ws-one","attributes":{"name":"production-network"}}],"links":{"next":null}}`, nil
	}
	cfg := config.Default()
	ui := &setuptest.UI{Inputs: []string{"default", "acme"}, Selections: [][]string{{"production-network"}}, Decisions: []bool{true, true}}
	_, err := (Source{}).Setup(context.Background(), integration.SetupRequest{Config: &cfg, UI: ui, System: &terraformSetupSystem{api: api}})
	if err != nil || attempts != 2 || cfg.Terraform.Enabled == nil || !*cfg.Terraform.Enabled {
		t.Fatal("retry failed", cfg.Terraform, attempts, err)
	}
}

func TestSetupWorkspacePickerCancellationPreservesScope(t *testing.T) {
	_, api, original := fixtureSource(t)
	cfg := config.Default()
	cfg.Terraform = original
	ui := &setuptest.UI{Inputs: []string{"default", "acme"}, Decisions: []bool{true}, CancelAt: "Terraform workspaces"}
	_, err := (Source{}).Setup(context.Background(), integration.SetupRequest{Config: &cfg, UI: ui, System: &terraformSetupSystem{api: api}})
	if err != setup.ErrAborted || !reflect.DeepEqual(cfg.Terraform.Workspaces, original.Workspaces) {
		t.Fatal("picker cancellation changed the configured scope", cfg.Terraform, err)
	}
}

func TestSetupCreatesMissingProfileBeforeLogin(t *testing.T) {
	_, api, original := fixtureSource(t)
	api.profile = "radar"
	api.auth, api.authErr = `{}`, fmt.Errorf("profile configuration file doesn't exist")
	cfg := config.Default()
	cfg.Terraform = original
	ui := &setuptest.UI{Inputs: []string{"radar", "acme"}, Selections: [][]string{{"production-network"}}, Decisions: []bool{true, true, true}}
	created := false
	sys := &terraformSetupSystem{api: api, onCreate: func() { created = true }, onLogin: func() {
		if !created {
			t.Fatal("login attempted before creating the missing profile")
		}
		api.auth, api.authErr = "", nil
	}}
	var completed []string
	_, err := (Source{}).Setup(context.Background(), integration.SetupRequest{Config: &cfg, UI: ui, System: sys, Completed: func(v string) { completed = append(completed, v) }})
	if err != nil || !created || cfg.Terraform.Enabled == nil || !*cfg.Terraform.Enabled || cfg.Terraform.Profile != "radar" {
		t.Fatal("missing profile not connected", cfg.Terraform, created, err)
	}
	if !strings.Contains(strings.Join(sys.Calls, ";"), "tfctl profile profiles list --json --no-color;tfctl profile profiles create radar --hostname=app.terraform.io --no-activate;tfctl auth login --profile=radar") {
		t.Fatal("profile creation must precede login without changing the active profile", sys.Calls)
	}
	if !reflect.DeepEqual(completed, []string{"Terraform CLI profile radar creation", "Terraform login"}) {
		t.Fatal("creation and login must be reported as immediate side effects", completed)
	}
}

func TestSetupMissingProfileFailuresPreserveScope(t *testing.T) {
	for _, failure := range []string{"declined", "cancelled", "list failed", "invalid list", "null list", "incomplete list", "creation failed"} {
		t.Run(failure, func(t *testing.T) {
			_, api, original := fixtureSource(t)
			api.profile = "radar"
			api.auth, api.authErr = `{}`, fmt.Errorf("missing profile")
			cfg := config.Default()
			cfg.Terraform = original
			ui := &setuptest.UI{Inputs: []string{"radar", "acme"}, Decisions: []bool{true, true}}
			sys := &terraformSetupSystem{api: api}
			switch failure {
			case "declined":
				ui.Decisions = append(ui.Decisions, false)
			case "cancelled":
				ui.CancelAt = "Terraform CLI profile radar does not exist. Create it for app.terraform.io?"
			case "list failed":
				sys.profilesErr = fmt.Errorf("private diagnostic")
				ui.Decisions = append(ui.Decisions, false)
			case "invalid list":
				sys.profiles = "private diagnostic"
				ui.Decisions = append(ui.Decisions, false)
			case "null list":
				sys.profiles = "null"
				ui.Decisions = append(ui.Decisions, false)
			case "incomplete list":
				sys.profiles = `[{}]`
				ui.Decisions = append(ui.Decisions, false)
			case "creation failed":
				sys.createErr = fmt.Errorf("private diagnostic")
				ui.Decisions = append(ui.Decisions, true, false)
			}
			_, err := (Source{}).Setup(context.Background(), integration.SetupRequest{Config: &cfg, UI: ui, System: sys})
			if failure == "cancelled" {
				if err != setup.ErrAborted {
					t.Fatal("cancellation not propagated", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			original.Enabled = new(false)
			if !reflect.DeepEqual(cfg.Terraform, original) || strings.Contains(ui.Transcript.String(), "private diagnostic") {
				t.Fatal("failure lost scope or leaked diagnostics", cfg.Terraform, ui.Transcript.String())
			}
			for _, call := range sys.Calls {
				if strings.Contains(call, "auth login") || strings.Contains(call, "api ") || failure != "creation failed" && strings.Contains(call, "profiles create") {
					t.Fatal("unsafe command after failed preparation", sys.Calls)
				}
			}
		})
	}
}

func TestSetupRetriesLoginWithoutRecreatingProfile(t *testing.T) {
	_, api, _ := fixtureSource(t)
	api.profile = "radar"
	api.auth, api.authErr = `{}`, fmt.Errorf("missing profile")
	cfg := config.Default()
	ui := &setuptest.UI{Inputs: []string{"radar", "acme"}, Selections: [][]string{{"production-network"}}, Decisions: []bool{true, true, true, true, true}}
	sys := &terraformSetupSystem{api: api}
	sys.onCreate = func() { sys.profiles = `[{"Name":"default"},{"Name":"radar"}]` }
	attempts := 0
	sys.onLogin = func() {
		attempts++
		if attempts == 2 {
			api.auth, api.authErr = "", nil
		}
	}
	_, err := (Source{}).Setup(context.Background(), integration.SetupRequest{Config: &cfg, UI: ui, System: sys})
	if err != nil || cfg.Terraform.Enabled == nil || !*cfg.Terraform.Enabled || attempts != 2 {
		t.Fatal("retry failed", cfg.Terraform, attempts, err)
	}
	if strings.Count(strings.Join(sys.Calls, ";"), "profiles create") != 1 {
		t.Fatal("retry recreated an existing profile", sys.Calls)
	}
}
