package onboarding

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"radar/internal/config"
	"radar/internal/hostsetup"
	"radar/internal/integration"
	"radar/internal/pi"
	"radar/internal/setup"
)

type wizard struct {
	ui        prompter
	system    setup.System
	http      *http.Client
	registry  integration.Registry
	completed []string
	plans     []setup.Plan
}

func newWizard(ui prompter, sys setup.System, registry integration.Registry) wizard {
	return wizard{ui: ui, system: sys, registry: registry, http: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// Needed distinguishes absence from an unreadable or user-authored config.
func Needed() (bool, error) {
	path, err := config.Path()
	if err != nil {
		return false, err
	}
	_, err = os.Lstat(path)
	if os.IsNotExist(err) {
		return true, nil
	}
	return false, err
}
func MissingTools() []string                    { return hostsetup.MissingTools() }
func (w *wizard) recordCompleted(effect string) { w.completed = append(w.completed, effect) }
func (w *wizard) host() hostsetup.Service {
	return hostsetup.Service{UI: w.ui, System: w.system, Completed: w.recordCompleted}
}
func (w *wizard) run() error {
	w.plans = nil
	w.completed = nil
	draft, err := config.LoadSetupDraft()
	if err != nil {
		return err
	}
	cfg := draft.Config
	path, err := config.Path()
	if err != nil {
		return err
	}
	w.ui.Print("\n%s\n\n", w.ui.Style().Bold(true).Render("Radar setup"))
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	host := w.host()
	if err := host.Dependencies(ctx); err != nil {
		return err
	}
	updates := config.Secrets{}
	req := integration.SetupRequest{UI: w.ui, System: w.system, HTTP: w.http, Draft: draft, Config: &cfg, Path: path, Completed: w.recordCompleted,
		Secret: func(title, hint, environment, namespace, key string) (string, error) {
			return w.secret(title, hint, environment, namespace, key, updates)
		},
		EnsureExtension: func(ctx context.Context, name, reason string) error {
			return host.Extensions(ctx, hostsetup.Dependency{Name: name, Reason: reason})
		}}
	w.ui.Print("\nConfig, secrets and integration files are saved only after review.\n")
	for _, phase := range []integration.SetupPhase{integration.SetupEnvironment, integration.SetupDirectories, integration.SetupConnections} {
		if phase == integration.SetupDirectories {
			w.ui.Print("\nDirectories\n")
		}
		if phase == integration.SetupConnections {
			if draft.Existing {
				w.ui.Print("Existing settings and unchanged secrets are retained. Directory changes do not move existing notes or workspaces.\n")
			}
			w.ui.Print("Disabling an integration retains its saved credentials.\n\nIntegrations\n")
		}
		for _, provider := range w.registry.SetupProviders(phase) {
			plan, err := provider.Setup(ctx, req)
			if err != nil {
				return err
			}
			w.plans = append(w.plans, plan)
		}
	}
	data, err := draft.Preview(cfg)
	if err != nil {
		return err
	}
	if err := w.review(path, data, updates); err != nil {
		return err
	}
	err = config.WithSetupLock(func() error {
		if err := draft.CheckUnchanged(); err != nil {
			return err
		}
		for _, plan := range w.plans {
			if plan.CheckUnchanged != nil {
				if err := plan.CheckUnchanged(); err != nil {
					return err
				}
			}
		}
		for _, plan := range w.plans {
			if plan.Apply != nil {
				if err := plan.Apply(); err != nil {
					return err
				}
			}
		}
		if err := config.SaveSecrets(updates); err != nil {
			return err
		}
		if err := pi.InstallInstructions(filepath.Join(filepath.Dir(path), "AGENTS.md")); err != nil {
			return fmt.Errorf("write agent instructions: %w", err)
		}
		if err := draft.Save(cfg); err != nil {
			return fmt.Errorf("write config.yaml: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	w.ui.Print("\n✓ Radar configured\n")
	for _, plan := range w.plans {
		if plan.Summary != "" {
			w.ui.Print("%s\n", plan.Summary)
		}
	}
	for _, plan := range w.plans {
		if plan.AfterSave != nil {
			plan.AfterSave(ctx)
		}
	}
	w.ui.Print("Run radar to open the dashboard. Use /login in Pi if needed.\n")
	return nil
}
