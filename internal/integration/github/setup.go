package github

import (
	"context"
	"encoding/json"
	"fmt"

	"radar/internal/integration"
	"radar/internal/setup"
)

func (Source) SetupStage() integration.SetupStage {
	return integration.SetupStage{Phase: integration.SetupConnections, Order: 0}
}
func (Source) Setup(ctx context.Context, req integration.SetupRequest) (setup.Plan, error) {
	enabled, err := req.UI.Confirm("Connect Radar to GitHub?", !req.Draft.Existing || req.Config.GitHub.Enabled == nil || *req.Config.GitHub.Enabled)
	if err != nil {
		return setup.Plan{}, err
	}
	req.Config.GitHub.Enabled = &enabled
	if !enabled {
		return setup.Plan{}, nil
	}
	installInstructions := "Follow the official installation instructions at https://github.com/cli/cli#installation"
	var install *setup.Command
	if req.System.LookPath("brew") {
		installInstructions = "brew install gh"
		install = &setup.Command{Name: "brew", Args: []string{"install", "gh"}}
	} else if req.System.Platform() == "linux" && req.System.LookPath("apt-get") {
		installInstructions = "sudo apt-get update && sudo apt-get install gh (official repository setup: https://github.com/cli/cli/blob/trunk/docs/install_linux.md)"
	}
	var login string
	connected, err := setup.ConnectCLI(ctx, req.UI, req.System, setup.CLIConnection{
		Name: "gh", Service: "GitHub", MinimumVersion: "2.0.0", Reason: "authenticated GitHub requests",
		InstallInstructions: installInstructions, Install: install,
		Login: setup.Command{Name: "gh", Args: []string{"auth", "login", "--hostname", "github.com"}},
		Verify: func(ctx context.Context) error {
			if _, err := req.System.Output(ctx, setup.Command{Name: "gh", Args: []string{"auth", "status", "--hostname", "github.com"}}); err != nil {
				return fmt.Errorf("GitHub login required")
			}
			data, err := req.System.Output(ctx, setup.Command{Name: "gh", Args: []string{"api", "--hostname", "github.com", "user"}})
			var user struct {
				Login string `json:"login"`
			}
			if err != nil || json.Unmarshal([]byte(data), &user) != nil || user.Login == "" {
				return fmt.Errorf("GitHub identity read failed")
			}
			login = user.Login
			return nil
		},
	}, req.Completed)
	if err == nil {
		*req.Config.GitHub.Enabled = connected
		if connected {
			req.UI.Print("✓ GitHub connected as %s at github.com (read verified)\n", login)
		}
	}
	return setup.Plan{}, err
}
