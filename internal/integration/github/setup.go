package github

import (
	"context"
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
	req.UI.Print("Checking GitHub authentication…\n")
	check := setup.Command{Name: "gh", Args: []string{"auth", "status", "--hostname", "github.com"}}
	if _, err := req.System.Output(ctx, check); err != nil {
		yes, err := req.UI.Confirm("GitHub CLI is not authenticated. Run `gh auth login`?", true)
		if err != nil {
			return setup.Plan{}, err
		}
		if !yes {
			return setup.Plan{}, setup.ErrAborted
		}
		if err := req.System.Run(ctx, setup.Command{Name: "gh", Args: []string{"auth", "login", "--hostname", "github.com"}}); err != nil {
			return setup.Plan{}, fmt.Errorf("GitHub login failed — run `gh auth login` and retry setup: %w", err)
		}
		if _, err := req.System.Output(ctx, check); err != nil {
			return setup.Plan{}, fmt.Errorf("GitHub authentication is still unavailable — run `gh auth status` and retry setup")
		}
		req.Completed("GitHub login")
	}
	req.UI.Print("✓ GitHub authenticated (credentials stay with gh)\n")
	return setup.Plan{}, nil
}
