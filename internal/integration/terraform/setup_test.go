package terraform

import (
	"context"
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
	api *fixtureAPI
}

func (s *terraformSetupSystem) Output(ctx context.Context, c setup.Command) (string, error) {
	s.Calls = append(s.Calls, c.Name+" "+strings.Join(c.Args, " "))
	return s.api.output(ctx, c)
}

func TestSetupReusesLoginAndVerifiesScope(t *testing.T) {
	_, api, _ := fixtureSource(t)
	cfg := config.Default()
	ui := &setuptest.UI{Inputs: []string{"default", "acme", "production-network"}, Decisions: []bool{true}}
	sys := &terraformSetupSystem{api: api}
	_, err := (Source{}).Setup(context.Background(), integration.SetupRequest{Config: &cfg, UI: ui, System: sys})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Terraform.Enabled == nil || !*cfg.Terraform.Enabled || cfg.Terraform.Organization != "acme" || !reflect.DeepEqual(cfg.Terraform.Workspaces, []string{"production-network"}) {
		t.Fatal(cfg.Terraform)
	}
	for _, call := range sys.Calls {
		if strings.Contains(call, "auth login") {
			t.Fatal("redundant login")
		}
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
	ui := &setuptest.UI{Inputs: []string{"default", "acme", "production-network"}, Decisions: []bool{true, false}}
	sys := &terraformSetupSystem{api: api}
	_, err := (Source{}).Setup(context.Background(), integration.SetupRequest{Config: &cfg, UI: ui, System: sys})
	if err != nil || cfg.Terraform.Enabled == nil || *cfg.Terraform.Enabled || cfg.Terraform.Organization != cfgTF.Organization {
		t.Fatal(cfg.Terraform, err)
	}
}
