package onboarding

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"radar/internal/config"
	"radar/internal/integration/tmux"
	"radar/internal/pi"
)

type wizard struct {
	ui     prompter
	system system
	http   *http.Client
}

func newWizard(ui prompter, system system) wizard {
	return wizard{ui: ui, system: system, http: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// Needed deliberately distinguishes absence from an unreadable, broken, empty or
// user-authored config. Setup is not a migration or an implicit reset command.
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

func (w wizard) run() error {
	draft, err := config.LoadSetupDraft()
	if err != nil {
		return err
	}
	cfg := draft.Config
	path, err := config.Path()
	if err != nil {
		return err
	}
	w.ui.print("\nWelcome to Radar\nLet's prepare your tools, directories and integrations.\nNothing is installed without permission. Config and secrets are saved only after review.\n\n")
	ctx := context.Background()
	if err := w.dependencies(ctx, cfg.SBX); err != nil {
		return err
	}
	tmuxPlan, err := w.tmuxConfig()
	if err != nil {
		return err
	}
	w.ui.print("\nDirectories\n")
	repoInitial := "~/workspace"
	if draft.Existing && len(cfg.RepositoryDirs) > 0 {
		repoInitial = cfg.RepositoryDirs[0]
	}
	repo, err := w.directory("Where do you check out your repositories?", "Your primary repository directory. Additional configured repository directories are retained.", repoInitial)
	if err != nil {
		return err
	}
	if draft.Existing && len(cfg.RepositoryDirs) > 0 {
		cfg.RepositoryDirs = append([]string{repo}, cfg.RepositoryDirs[1:]...)
	} else {
		cfg.RepositoryDirs = []string{repo}
	}
	rootInput, err := w.ui.input(question{title: "Where should Radar put its workspaces and worktrees?", hint: "A separate directory for Radar-managed work; it will be created after confirmation.", initial: cfg.Workspace.RootDir, validate: func(value string) error {
		root, err := directoryPath(value)
		if err != nil {
			return err
		}
		for _, repository := range cfg.RepositoryDirs {
			expanded, err := expandPath(repository)
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
		return err
	}
	root, err := directoryPath(rootInput)
	if err != nil {
		return err
	}
	cfg.Workspace.RootDir = root
	notesInitial := cfg.Obsidian.VaultPath
	if notesInitial == "" {
		notesInitial = "~/Documents/Radar"
	}
	notes, err := w.directory("Where should Radar store your task notes?", "Radar creates a Tasks/ folder here. An Obsidian vault works well, but any directory is fine.", notesInitial)
	if err != nil {
		return err
	}
	cfg.Obsidian.VaultPath = notes
	if draft.Existing {
		w.ui.print("Existing settings and unchanged secrets are retained. Directory changes do not move existing notes or workspaces.\n")
	}
	w.ui.print("RADAR_JIRA_* and RADAR_DATADOG_* environment values still override saved settings at runtime. Disabling an integration retains its saved secrets.\n")

	updates := config.Secrets{}
	w.ui.print("\nIntegrations\n")
	github, err := w.ui.confirm("Connect Radar to GitHub?", draft.Existing && (cfg.GitHub.Enabled == nil || *cfg.GitHub.Enabled))
	if err != nil {
		return err
	}
	cfg.GitHub.Enabled = &github
	if github {
		w.ui.print("Checking GitHub authentication…\n")
		if _, err := w.system.output(ctx, "gh", "auth", "status", "--hostname", "github.com"); err != nil {
			if err := w.allow("GitHub CLI is not authenticated. Run `gh auth login`?"); err != nil {
				return err
			}
			if err := w.system.run(ctx, "gh", "auth", "login", "--hostname", "github.com"); err != nil {
				return fmt.Errorf("GitHub login failed — run `gh auth login` and retry setup: %w", err)
			}
			if _, err := w.system.output(ctx, "gh", "auth", "status", "--hostname", "github.com"); err != nil {
				return fmt.Errorf("GitHub authentication is still unavailable — run `gh auth status` and retry setup")
			}
		}
		w.ui.print("✓ GitHub authenticated (credentials stay with gh)\n")
	}
	jira, err := w.ui.confirm("Connect Radar to Jira?", draft.Existing && (cfg.Jira.Enabled == nil || *cfg.Jira.Enabled))
	if err != nil {
		return err
	}
	cfg.Jira.Enabled = &jira
	if jira {
		if err := w.jira(ctx, &cfg, updates); err != nil {
			return err
		}
	}
	datadog, err := w.ui.confirm("Connect Radar to Datadog?", draft.Existing && (cfg.Datadog.Enabled == nil || *cfg.Datadog.Enabled))
	if err != nil {
		return err
	}
	cfg.Datadog.Enabled = &datadog
	if datadog {
		if err := w.datadog(ctx, &cfg, updates); err != nil {
			return err
		}
	}

	data, err := draft.Preview(cfg)
	if err != nil {
		return err
	}
	if err := w.review(path, data, cfg, updates, tmuxPlan); err != nil {
		return err
	}
	if err := w.allow("Save this configuration?"); err != nil {
		return err
	}
	err = config.WithSetupLock(func() error {
		if err := draft.CheckUnchanged(); err != nil {
			return err
		}
		for _, dir := range []string{repo, root, notes} {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("create directory: %w", err)
			}
		}
		if _, err := cfg.Obsidian.ValidateAndPrepare(); err != nil {
			return err
		}
		if tmuxPlan != nil {
			if err := tmuxPlan.Apply(); err != nil {
				return err
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

	if tmuxPlan != nil {
		if _, err := w.system.output(ctx, "tmux", "has-session"); err == nil {
			if err := w.system.run(ctx, "tmux", "source-file", tmuxPlan.Path); err != nil {
				w.ui.print("Tmux settings were saved, but could not be loaded into the running server. Run `tmux source-file %s` or restart tmux.\n", tmuxPlan.Path)
			}
		}
	}
	w.ui.print("\n✓ Wrote %s\n", path)
	if len(updates) > 0 {
		secretsPath, _ := config.SecretsPath()
		w.ui.print("✓ Saved credentials to %s\n", secretsPath)
	}
	if tmuxPlan != nil {
		w.ui.print("✓ Tmux settings: %s (included by %s)\n", tmuxPlan.Path, tmuxPlan.UserPath)
		if tmuxPlan.Starter {
			w.ui.print("Open Radar from a workspace: Ctrl+B, release, then R.\n")
		} else {
			w.ui.print("Open Radar from a workspace: your tmux prefix, then R.\n")
		}
	}
	w.ui.print("Ready — run `radar` to open your dashboard.\nPi model/provider authentication is separate: use `/login` in Pi if needed.\n")
	return nil
}

func (w wizard) allowInstall(title string) error { return w.confirmRequired(title, true) }
func (w wizard) allow(title string) error        { return w.confirmRequired(title, false) }
func (w wizard) confirmRequired(title string, initial bool) error {
	yes, err := w.ui.confirm(title, initial)
	if err != nil {
		return err
	}
	if !yes {
		return ErrAborted
	}
	return nil
}

func (w wizard) directory(title, hint, initial string) (string, error) {
	value, err := w.ui.input(question{title: title, hint: hint, initial: initial, validate: func(value string) error {
		_, err := directoryPath(value)
		return err
	}})
	if err != nil {
		return "", err
	}
	return directoryPath(value)
}

func directoryPath(value string) (string, error) {
	path, err := expandPath(value)
	if err != nil {
		return "", err
	}
	current := path
	for {
		info, err := os.Stat(current)
		if err == nil {
			if !info.IsDir() {
				return "", fmt.Errorf("path must be a directory")
			}
			return path, nil
		}
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("cannot access directory: %w", err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("directory has no accessible parent")
		}
		current = parent
	}
}

func (w wizard) tmuxConfig() (*tmux.ConfigPlan, error) {
	plan, err := tmux.PlanConfig(true)
	if err != nil {
		return nil, err
	}
	if plan.Starter {
		w.ui.print("\nNo tmux configuration found. Review will include a starter config with extended keys,\nmouse support and the Radar popup: Ctrl+B, release, then R.\n")
	} else {
		w.ui.print("\nRecommended tmux additions: extended keys and a prefix + r Radar popup.\nYour prefix and other settings are preserved; this replaces any existing prefix + r binding.\n")
		yes, err := w.ui.confirm("Add Radar's prefix + r popup binding?", false)
		if err != nil {
			return nil, err
		}
		if !yes {
			return nil, nil
		}
	}
	return &plan, nil
}
