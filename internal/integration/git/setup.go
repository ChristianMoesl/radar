package git

import (
	"context"
	"os"

	"radar/internal/integration"
	"radar/internal/setup"
)

func (Source) SetupStage() integration.SetupStage {
	return integration.SetupStage{Phase: integration.SetupDirectories, Order: 0}
}
func (Source) Setup(_ context.Context, req integration.SetupRequest) (setup.Plan, error) {
	initial := "~/workspace"
	if req.Draft.Existing && len(req.Config.RepositoryDirs) > 0 {
		initial = req.Config.RepositoryDirs[0]
	}
	value, err := req.UI.Input(setup.Question{Title: "Where do you check out your repositories?", Hint: "Your primary repository directory. Additional configured repository directories are retained.", Initial: initial, Validate: func(value string) error { _, err := setup.DirectoryPath(value); return err }})
	if err != nil {
		return setup.Plan{}, err
	}
	dir, err := setup.DirectoryPath(value)
	if err != nil {
		return setup.Plan{}, err
	}
	if req.Draft.Existing && len(req.Config.RepositoryDirs) > 0 {
		req.Config.RepositoryDirs = append([]string{dir}, req.Config.RepositoryDirs[1:]...)
	} else {
		req.Config.RepositoryDirs = []string{dir}
	}
	return setup.Plan{Directories: []string{dir}, Apply: func() error { return os.MkdirAll(dir, 0755) }}, nil
}
