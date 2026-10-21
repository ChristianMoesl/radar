package terraform

import (
	"context"
	"fmt"

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
	installInstructions := "Install tfctl 0.4.0+ from https://releases.hashicorp.com/tfctl/"
	var install *setup.Command
	if req.System.LookPath("brew") {
		installInstructions = "brew install hashicorp/tap/tfctl"
		install = &setup.Command{Name: "brew", Args: []string{"install", "hashicorp/tap/tfctl"}}
	}
	c := client{profile: next.Profile, output: req.System.Output}
	identity := next
	identity.Workspaces = nil
	var authenticated session
	connected, err := setup.ConnectCLI(ctx, req.UI, req.System, setup.CLIConnection{
		Name: "tfctl", Service: "Terraform", MinimumVersion: minimumVersion, Reason: "tracking your own runs and plan summaries",
		InstallInstructions: installInstructions, Install: install,
		Login: setup.Command{Name: "tfctl", Args: []string{"auth", "login", "--profile=" + next.Profile}},
		Verify: func(ctx context.Context) error {
			var err error
			authenticated, err = c.connect(ctx, identity)
			return err
		},
	}, req.Completed)
	if err == nil && connected {
		next.Workspaces, connected, err = selectWorkspaces(ctx, req.UI, authenticated, next)
	}
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

func selectWorkspaces(ctx context.Context, ui setup.UI, authenticated session, cfg settings.Config) ([]string, bool, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		ui.Print("Fetching accessible Terraform workspaces in %s…\n", cfg.Organization)
		names, err := authenticated.workspaceNames(ctx, cfg.Organization)
		if err == nil && len(names) == 0 {
			err = fmt.Errorf("no accessible workspaces found in %s", cfg.Organization)
		}
		if err == nil {
			choices := make([]setup.Choice, 0, len(names))
			available := map[string]bool{}
			for _, name := range names {
				choices = append(choices, setup.Choice{Value: name, Label: name})
				available[name] = true
			}
			selected, inputErr := ui.MultiSelect(setup.MultiSelectQuestion{
				Title: "Terraform workspaces", Hint: "Select the workspaces whose personal runs Radar should track",
				Initial: cfg.Workspaces, Choices: choices,
				Validate: func(values []string) error {
					if len(values) == 0 {
						return fmt.Errorf("select at least one workspace")
					}
					for _, name := range values {
						if !available[name] {
							return fmt.Errorf("select an accessible workspace")
						}
					}
					next := cfg
					next.Workspaces = values
					return settings.Validate(next)
				},
			})
			if inputErr != nil {
				return nil, false, inputErr
			}
			cfg.Workspaces = selected
			if _, err = authenticated.client.connect(ctx, cfg); err == nil {
				return selected, true, nil
			}
		}
		if ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
		ui.Print("Terraform workspace discovery or access verification failed: %s. Check the organization, permissions and network.\n", err)
		retry, err := ui.Confirm("Retry Terraform workspace selection? (No defers Terraform)", false)
		if err != nil {
			return nil, false, err
		}
		if !retry {
			ui.Print("Terraform deferred. Rerun radar setup to connect it.\n")
			return nil, false, nil
		}
	}
}
