package tmux

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"radar/internal/config"
)

const popupBinding = "bind-key r display-popup -E -w 90% -h 90% -d '#{pane_current_path}' 'radar'\n"
const starterSettings = `# Comfortable defaults for a new tmux installation.
set -g mouse on
set -g history-limit 50000
set -g base-index 1
setw -g pane-base-index 1
set -g renumber-windows on
set -s escape-time 10
set -g focus-events on
set -g status-right 'Radar: prefix + r | %H:%M'

`

type ConfigPlan struct {
	Path, UserPath, Content, Include string
	oldUser, oldManaged              []byte
	userMode                         os.FileMode
	userExists                       bool
	managedExists                    bool
}

func PlanConfig(starter bool) (ConfigPlan, error) {
	configPath, err := config.Path()
	if err != nil {
		return ConfigPlan{}, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ConfigPlan{}, err
	}
	plan := ConfigPlan{Path: filepath.Join(filepath.Dir(configPath), "tmux.conf"), UserPath: filepath.Join(home, ".tmux.conf"), userMode: 0600}
	// Match tmux's conventional config precedence. Follow dotfile symlinks for
	// the append so an atomic write never replaces the user's symlink itself.
	xdg := os.Getenv("XDG_CONFIG_HOME")
	if xdg == "" {
		xdg = filepath.Join(home, ".config")
	}
	for _, path := range []string{plan.UserPath, filepath.Join(xdg, "tmux", "tmux.conf")} {
		if _, err := os.Lstat(path); err == nil {
			plan.UserPath = path
			break
		} else if !os.IsNotExist(err) {
			return ConfigPlan{}, err
		}
	}
	if _, err := os.Lstat(plan.UserPath); err == nil {
		plan.userExists = true
		plan.UserPath, err = filepath.EvalSymlinks(plan.UserPath)
		if err != nil {
			return ConfigPlan{}, err
		}
		info, err := os.Stat(plan.UserPath)
		if err != nil {
			return ConfigPlan{}, err
		}
		if !info.Mode().IsRegular() {
			return ConfigPlan{}, fmt.Errorf("tmux configuration is not a regular file")
		}
		plan.userMode = info.Mode().Perm()
		plan.oldUser, err = os.ReadFile(plan.UserPath)
		if err != nil {
			return ConfigPlan{}, err
		}
	}
	plan.Content = "# Radar-managed tmux settings. Prefix + r opens the dashboard.\n"
	if starter {
		plan.Content += starterSettings
	}
	plan.Content += popupBinding
	plan.Include = "source-file " + shellQuote(plan.Path)
	if info, err := os.Lstat(plan.Path); err == nil {
		if !info.Mode().IsRegular() {
			return ConfigPlan{}, fmt.Errorf("Radar tmux config must be a regular file: %s", plan.Path)
		}
		plan.managedExists = true
		plan.oldManaged, err = os.ReadFile(plan.Path)
		if err != nil {
			return ConfigPlan{}, err
		}
		plan.Content = string(plan.oldManaged)
		if !strings.Contains(plan.Content, popupBinding) {
			plan.Content = strings.TrimRight(plan.Content, "\n") + "\n\n" + popupBinding
		}
	} else if !os.IsNotExist(err) {
		return ConfigPlan{}, err
	}
	return plan, nil
}

func (p ConfigPlan) Apply() error {
	// Do not overwrite edits made while the user was answering the wizard.
	if err := unchangedConfig(p.UserPath, p.userExists, p.oldUser); err != nil {
		return err
	}
	if err := unchangedConfig(p.Path, p.managedExists, p.oldManaged); err != nil {
		return err
	}

	if !bytes.Equal([]byte(p.Content), p.oldManaged) {
		if err := writeConfig(p.Path, []byte(p.Content), 0600, !p.managedExists); err != nil {
			return err
		}
	}
	for _, line := range strings.Split(string(p.oldUser), "\n") {
		if strings.TrimSpace(line) == p.Include {
			return nil
		}
	}
	data := append([]byte(nil), p.oldUser...)
	if len(data) > 0 && data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}
	data = append(data, []byte("\n# Radar dashboard (prefix + r)\n"+p.Include+"\n")...)
	return writeConfig(p.UserPath, data, p.userMode, !p.userExists)
}

func unchangedConfig(path string, existed bool, previous []byte) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) && !existed {
		return nil
	}
	if err != nil {
		return err
	}
	if !existed || !info.Mode().IsRegular() {
		return fmt.Errorf("tmux configuration changed during setup: %s", path)
	}
	current, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, previous) {
		return fmt.Errorf("tmux configuration changed during setup — rerun setup to review %s", path)
	}
	return nil
}

func writeConfig(path string, data []byte, mode os.FileMode, exclusive bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".radar-tmux-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := file.Chmod(mode); err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if exclusive {
		return os.Link(file.Name(), path)
	}
	return os.Rename(file.Name(), path)
}
