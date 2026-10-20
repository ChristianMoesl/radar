package terraform

import (
	"context"
	"fmt"
	"strings"

	"radar/internal/integration"
	"radar/internal/integration/terraform/settings"
	"radar/internal/setup"
)

func (Source) SetupStage() integration.SetupStage {
	return integration.SetupStage{Phase: integration.SetupConnections, Order: 3}
}

func (Source) Setup(ctx context.Context, req integration.SetupRequest) (setup.Plan, error) {
	original := req.Config.Terraform
	enabled, err := req.UI.Confirm("Connect Radar to Terraform?", original.Enabled != nil && *original.Enabled || configured(original) == "")
	if err != nil {
		return setup.Plan{}, err
	}
	if !enabled {
		req.Config.Terraform.Enabled = &enabled
		return setup.Plan{}, nil
	}
	next := original
	for _, field := range []struct {
		title string
		value *string
	}{{"Terraform CLI profile", &next.Profile}, {"Terraform organization", &next.Organization}} {
		value, err := req.UI.Input(setup.Question{Title: field.title, Initial: *field.value, Validate: func(value string) error {
			if !settings.ValidSelector(value) {
				return fmt.Errorf("enter a profile or organization name")
			}
			return nil
		}})
		if err != nil {
			return setup.Plan{}, err
		}
		*field.value = value
	}
	value, err := req.UI.Input(setup.Question{Title: "Terraform workspaces", Initial: strings.Join(next.Workspaces, ", "), Hint: "Comma-separated exact workspace names", Validate: func(value string) error {
		cfg := next
		cfg.Workspaces = splitWorkspaces(value)
		if len(cfg.Workspaces) == 0 {
			return fmt.Errorf("select at least one workspace")
		}
		return settings.Validate(cfg)
	}})
	if err != nil {
		return setup.Plan{}, err
	}
	next.Workspaces = splitWorkspaces(value)
	installInstructions := "Install tfctl 0.4.0+ from https://releases.hashicorp.com/tfctl/"
	var install *setup.Command
	if req.System.LookPath("brew") {
		installInstructions = "brew install hashicorp/tap/tfctl"
		install = &setup.Command{Name: "brew", Args: []string{"install", "hashicorp/tap/tfctl"}}
	}
	connected, err := setup.ConnectCLI(ctx, req.UI, req.System, setup.CLIConnection{
		Name: "tfctl", Service: "Terraform", MinimumVersion: minimumVersion, Reason: "tracking your own runs and plan summaries",
		InstallInstructions: installInstructions, Install: install,
		Login: setup.Command{Name: "tfctl", Args: []string{"auth", "login", "--profile=" + next.Profile}},
		Verify: func(ctx context.Context) error {
			_, err := (client{profile: next.Profile, output: req.System.Output}).connect(ctx, next)
			return err
		},
	}, req.Completed)
	if err != nil || !connected {
		connected = false
		original.Enabled = &connected
		req.Config.Terraform = original
		return setup.Plan{}, err
	}
	next.Enabled = &connected
	req.Config.Terraform = next
	req.UI.Print("✓ Terraform personal identity and selected workspace reads verified using profile %s\n", next.Profile)
	return setup.Plan{}, nil
}

func splitWorkspaces(value string) []string {
	var result []string
	for _, name := range strings.Split(value, ",") {
		if name = strings.TrimSpace(name); name != "" {
			result = append(result, name)
		}
	}
	return result
}
