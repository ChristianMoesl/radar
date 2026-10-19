package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"radar/internal/integration"
	"radar/internal/setup"
)

func (Source) SetupStage() integration.SetupStage {
	return integration.SetupStage{Phase: integration.SetupDirectories, Order: 1}
}
func (Source) Setup(_ context.Context, req integration.SetupRequest) (setup.Plan, error) {
	value, err := req.UI.Input(setup.Question{Title: "Where should Radar put its workspaces and worktrees?", Hint: "A separate directory for Radar-managed work; it will be created after confirmation.", Initial: req.Config.Workspace.RootDir, Validate: func(value string) error {
		root, err := setup.DirectoryPath(value)
		if err != nil {
			return err
		}
		for _, repository := range req.Config.RepositoryDirs {
			expanded, err := setup.ExpandPath(repository)
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(root, expanded)
			if err != nil {
				return err
			}
			if relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				return fmt.Errorf("workspace root must not contain your repository directory — Radar excludes its own workspaces from discovery")
			}
		}
		return nil
	}})
	if err != nil {
		return setup.Plan{}, err
	}
	dir, err := setup.DirectoryPath(value)
	if err != nil {
		return setup.Plan{}, err
	}
	req.Config.Workspace.RootDir = dir
	return setup.Plan{Directories: []string{dir}, Apply: func() error { return os.MkdirAll(dir, 0755) }}, nil
}
