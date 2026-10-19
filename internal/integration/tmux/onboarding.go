package tmux

import (
	"context"

	"radar/internal/integration"
	"radar/internal/setup"
)

func setupConfig(ui setup.UI) (*ConfigPlan, error) {
	plan, err := PlanConfig(true)
	if err != nil {
		return nil, err
	}
	if plan.Starter {
		ui.Print("\nNo tmux configuration found. Review will include a starter config with extended keys,\nmouse support and the Radar popup: Ctrl+B, release, then R.\n")
	} else {
		ui.Print("\nRecommended tmux additions: extended keys and a prefix + r Radar popup.\nYour prefix and other settings are preserved; this replaces any existing prefix + r binding.\n")
		yes, err := ui.Confirm("Add Radar's prefix + r popup binding?", false)
		if err != nil {
			return nil, err
		}
		if !yes {
			return nil, nil
		}
	}
	return &plan, nil
}

func (Source) SetupStage() integration.SetupStage {
	return integration.SetupStage{Phase: integration.SetupEnvironment, Order: 1}
}
func (Source) Setup(ctx context.Context, req integration.SetupRequest) (setup.Plan, error) {
	p, err := setupConfig(req.UI)
	if err != nil || p == nil {
		return setup.Plan{}, err
	}
	return p.SetupPlan(req.UI, req.System)
}

// SetupPlan keeps preview bytes and concurrency checks owned by tmux.
func (p ConfigPlan) SetupPlan(ui setup.UI, sys setup.System) (setup.Plan, error) {
	managed, err := setup.ReviewFileChange(p.Path, "Popup shortcut and extended keys", []byte(p.Content))
	if err != nil {
		return setup.Plan{}, err
	}
	user, err := setup.ReviewFileChange(p.UserPath, "Load Radar's tmux settings", p.UserConfigPreview())
	if err != nil {
		return setup.Plan{}, err
	}
	if user.Action == "UPDATE" {
		user.Action = "APPEND"
	}
	return setup.Plan{Files: []setup.File{managed, user}, CheckUnchanged: p.CheckUnchanged, Apply: p.Apply, AfterSave: func(ctx context.Context) {
		if _, err := sys.Output(ctx, setup.Command{Name: "tmux", Args: []string{"has-session"}}); err == nil {
			if err := sys.Run(ctx, setup.Command{Name: "tmux", Args: []string{"source-file", p.Path}}); err != nil {
				ui.Print("Tmux settings were saved, but could not be loaded into the running server. Run `tmux source-file %s` or restart tmux.\n", p.Path)
			}
		}
		if p.Starter {
			ui.Print("Open from tmux: Ctrl+B, release, then R.\n")
		} else {
			ui.Print("Open from tmux: your prefix, then R.\n")
		}
	}}, nil
}
