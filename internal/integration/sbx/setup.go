package sbx

import (
	"context"
	"os"

	"radar/internal/integration"
	sbxclient "radar/internal/integration/sbx/client"
	"radar/internal/setup"
)

type setupWizard struct {
	ui             setup.UI
	system         setup.System
	completed      func(string)
	extensions     func(context.Context, string, string) error
	sandboxSummary string
}

func (Source) SetupStage() integration.SetupStage {
	return integration.SetupStage{Phase: integration.SetupEnvironment, Order: 0}
}
func (Source) Setup(ctx context.Context, req integration.SetupRequest) (setup.Plan, error) {
	w := setupWizard{ui: req.UI, system: req.System, completed: req.Completed, extensions: req.EnsureExtension}
	p, err := w.sandboxSetup(ctx, req.Draft, req.Path, req.Config)
	if err != nil {
		return setup.Plan{}, err
	}
	return setup.Plan{Files: p.reviewFiles(), Summary: w.sandboxSummary, CheckUnchanged: p.checkUnchanged, Apply: p.apply}, nil
}

// Sandbox calls use per-stage deadlines and keep diagnostics captured. Preflight
// belongs to SBX and runs immediately before provisioning, including on WSL.
func (w *setupWizard) sandboxOutput(ctx context.Context, args ...string) (string, error) {
	if len(args) > 0 && args[0] == "create" {
		if err := sbxclient.NewForPlatform(setupRunner{w.system}, w.system.Platform(), sbxclient.IsWSL()).Preflight(ctx); err != nil {
			return "", err
		}
	}
	return w.system.Output(ctx, setup.Command{Name: "sbx", Args: args, Env: sbxclient.Environment("sbx", os.Environ())})
}

type setupRunner struct{ setup.System }

func (r setupRunner) LookPath(name string) error {
	if r.System.LookPath(name) {
		return nil
	}
	return os.ErrNotExist
}
func (r setupRunner) Run(ctx context.Context, cwd, name string, args ...string) (string, error) {
	return r.System.Output(ctx, setup.Command{Name: name, Args: args, Dir: cwd, Env: sbxclient.Environment(name, os.Environ())})
}
