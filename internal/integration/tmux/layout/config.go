package layout

import (
	"fmt"
	"strings"
)

const PiArgsPlaceholder = "$RADAR_PI_ARGS"

type Config struct {
	Windows []Window `json:"windows"`
}

type Window struct {
	Name   string `json:"name"`
	Layout string `json:"layout,omitempty"`
	Panes  []Pane `json:"panes"`
}

type Pane struct {
	Command string `json:"command"`
}

func Default() Config {
	return Config{Windows: []Window{
		{Name: "pi", Panes: []Pane{{Command: "pi " + PiArgsPlaceholder}}},
		{Name: "nvim", Panes: []Pane{{Command: "nvim ."}}},
	}}
}

func WithDefaults(cfg Config) Config {
	if len(cfg.Windows) == 0 {
		return Default()
	}
	return cfg
}

func Validate(cfg Config) error {
	cfg = WithDefaults(cfg)
	windowNames := make(map[string]bool, len(cfg.Windows))
	piArgsPlaceholders := 0
	for windowIndex, window := range cfg.Windows {
		name := strings.TrimSpace(window.Name)
		if name == "" {
			return fmt.Errorf("tmux.windows[%d].name is required", windowIndex)
		}
		if windowNames[name] {
			return fmt.Errorf("tmux window name %q is duplicated", name)
		}
		windowNames[name] = true
		if _, err := NativeLayout(window.Layout); err != nil {
			return fmt.Errorf("tmux window %q: %w", name, err)
		}
		if len(window.Panes) == 0 {
			return fmt.Errorf("tmux window %q requires at least one pane", name)
		}
		for paneIndex, pane := range window.Panes {
			if strings.TrimSpace(pane.Command) == "" {
				return fmt.Errorf("tmux window %q pane %d command is required", name, paneIndex)
			}
			piArgsPlaceholders += strings.Count(pane.Command, PiArgsPlaceholder)
		}
	}
	if piArgsPlaceholders != 1 {
		return fmt.Errorf("tmux configuration must contain %s exactly once", PiArgsPlaceholder)
	}
	return nil
}

func NativeLayout(layout string) (string, error) {
	switch strings.TrimSpace(layout) {
	case "":
		return "", nil
	case "horizontal":
		return "even-horizontal", nil
	case "vertical":
		return "even-vertical", nil
	case "main-horizontal", "main-vertical", "tiled":
		return strings.TrimSpace(layout), nil
	default:
		return "", fmt.Errorf("unsupported layout %q", layout)
	}
}
