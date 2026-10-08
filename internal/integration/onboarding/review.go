package onboarding

import (
	"bytes"
	"os"
	"strings"

	"radar/internal/config"
	"radar/internal/integration/tmux"
)

func fileAction(path string, data []byte) (string, error) {
	before, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "CREATE", nil
	}
	if err != nil {
		return "", err
	}
	if bytes.Equal(before, data) {
		return "UNCHANGED", nil
	}
	return "UPDATE", nil
}

func (w wizard) reviewBlock(title, path, action, body string) {
	w.ui.print("\n%s\n%s\n%s\n", heading.Render("── "+title+" · "+action+" ──"), path, body)
}

func (w wizard) review(path string, data []byte, cfg config.Config, updates config.Secrets, plan *tmux.ConfigPlan) error {
	w.ui.print("\nReview changes — nothing below is written until you save.\n")
	action, err := fileAction(path, data)
	if err != nil {
		return err
	}
	w.reviewBlock("Radar settings", path, action, strings.TrimSpace(string(data)))
	secretsPath, err := config.SecretsPath()
	if err != nil {
		return err
	}
	action = "NOT NEEDED"
	if _, err := os.Stat(secretsPath); err == nil {
		action = "UNCHANGED"
	} else if !os.IsNotExist(err) {
		return err
	}
	if len(updates) > 0 {
		action = "CREATE"
		if _, err := os.Stat(secretsPath); err == nil {
			action = "UPDATE"
		}
	}
	body := "Owner-only permissions (0600); plaintext, not encrypted. Existing secrets are retained."
	if len(updates["jira"]) > 0 {
		body += "\nJira API token: [hidden]"
	}
	if len(updates["datadog"]) > 0 {
		body += "\nDatadog API key and application key: [hidden]"
	}
	w.reviewBlock("Credentials", secretsPath, action, body)
	if plan != nil {
		action, err = fileAction(plan.Path, []byte(plan.Content))
		if err != nil {
			return err
		}
		w.reviewBlock("Radar tmux settings", plan.Path, action, strings.TrimSpace(plan.Content))
		before, err := os.ReadFile(plan.UserPath)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		action = "APPEND INCLUDE"
		for _, line := range strings.Split(string(before), "\n") {
			if strings.TrimSpace(line) == plan.Include {
				action = "UNCHANGED"
			}
		}
		if os.IsNotExist(err) {
			action = "CREATE"
		}
		w.reviewBlock("User tmux configuration", plan.UserPath, action, "Existing contents are preserved.\n"+plan.Include)
	}
	w.ui.print("\n%s\n", heading.Render("── Directories ──"))
	seen := map[string]bool{}
	for _, dir := range []string{cfg.RepositoryDirs[0], cfg.Workspace.RootDir, cfg.Obsidian.VaultPath, config.ObsidianTaskRoot(cfg.Obsidian.VaultPath)} {
		if seen[dir] {
			continue
		}
		seen[dir] = true
		action = "EXISTS"
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			action = "CREATE"
		} else if err != nil {
			return err
		}
		w.ui.print("[%s] %s\n", action, dir)
	}
	w.ui.print("\n%s\nEarlier approved installations, logins and installer PATH changes are already applied.\nCancelling this review does not undo them.\n", heading.Render("── Already completed ──"))
	return nil
}
