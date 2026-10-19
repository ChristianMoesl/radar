package obsidian

import (
	"context"
	"os"

	"radar/internal/config"
	"radar/internal/integration"
	"radar/internal/setup"
)

func (Source) SetupStage() integration.SetupStage {
	return integration.SetupStage{Phase: integration.SetupDirectories, Order: 2}
}
func (Source) Setup(_ context.Context, req integration.SetupRequest) (setup.Plan, error) {
	initial := req.Config.Obsidian.VaultPath
	if initial == "" {
		initial = "~/Documents/Radar"
	}
	value, err := req.UI.Input(setup.Question{Title: "Where should Radar store your task notes?", Hint: "Radar creates a Tasks/ folder here. An Obsidian vault works well, but any directory is fine.", Initial: initial, Validate: func(value string) error { _, err := setup.DirectoryPath(value); return err }})
	if err != nil {
		return setup.Plan{}, err
	}
	dir, err := setup.DirectoryPath(value)
	if err != nil {
		return setup.Plan{}, err
	}
	req.Config.Obsidian.VaultPath = dir
	cfg := req.Config.Obsidian
	return setup.Plan{Directories: []string{dir, config.ObsidianTaskRoot(dir)}, Apply: func() error {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
		_, err := cfg.ValidateAndPrepare()
		return err
	}}, nil
}
