package tmuxlayout

import (
	"strings"
	"testing"
)

func TestDefaultUsesSeparatePiAndNvimWindows(t *testing.T) {
	cfg := Default()
	if len(cfg.Windows) != 2 {
		t.Fatalf("windows = %d, want 2", len(cfg.Windows))
	}
	if cfg.Windows[0].Name != "pi" || cfg.Windows[0].Panes[0].Command != "pi "+PiArgsPlaceholder {
		t.Fatalf("first window = %#v, want Pi window", cfg.Windows[0])
	}
	if cfg.Windows[1].Name != "nvim" || cfg.Windows[1].Panes[0].Command != "nvim ." {
		t.Fatalf("second window = %#v, want nvim window", cfg.Windows[1])
	}
}

func TestValidateAcceptsHorizontalWorkspace(t *testing.T) {
	cfg := Config{Windows: []Window{{
		Name:   "workspace",
		Layout: "horizontal",
		Panes: []Pane{
			{Command: "pi " + PiArgsPlaceholder},
			{Command: "nvim ."},
		},
	}}}

	if err := Validate(cfg); err != nil {
		t.Fatal(err)
	}
	if got, err := NativeLayout("horizontal"); err != nil || got != "even-horizontal" {
		t.Fatalf("NativeLayout(horizontal) = %q, %v", got, err)
	}
}

func TestValidateRequiresWindowName(t *testing.T) {
	cfg := Config{Windows: []Window{{Panes: []Pane{{Command: "pi " + PiArgsPlaceholder}}}}}

	if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "name is required") {
		t.Fatalf("Validate() error = %v, want required name error", err)
	}
}

func TestValidateRequiresOnePiArgsPlaceholder(t *testing.T) {
	cfg := Config{Windows: []Window{{Name: "editor", Panes: []Pane{{Command: "nvim ."}}}}}

	if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), PiArgsPlaceholder+" exactly once") {
		t.Fatalf("Validate() error = %v, want placeholder error", err)
	}
}

func TestValidateRejectsUnsupportedLayout(t *testing.T) {
	cfg := Config{Windows: []Window{{
		Name:   "workspace",
		Layout: "even-horizontal",
		Panes:  []Pane{{Command: "pi " + PiArgsPlaceholder}},
	}}}

	if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "unsupported layout") {
		t.Fatalf("Validate() error = %v, want unsupported layout error", err)
	}
}
