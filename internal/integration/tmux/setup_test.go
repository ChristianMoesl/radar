package tmux

import (
	"os"
	"path/filepath"
	"radar/internal/integration"
	"strings"
	"testing"
)

func setupHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config with spaces"))
	return home
}

func TestTmuxConfigPlansPreserveUserSettingsAndAreIdempotent(t *testing.T) {
	for _, kind := range []string{"fresh", "existing", "symlink", "xdg"} {
		t.Run(kind, func(t *testing.T) {
			home := setupHome(t)
			path := filepath.Join(home, ".tmux.conf")
			original := "set -g prefix C-a\nbind-key r display-message 'reload'"
			target := path
			if kind == "xdg" {
				target = filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "tmux", "tmux.conf")
			}
			if kind == "symlink" {
				target = filepath.Join(home, "dotfiles", "tmux.conf")
			}
			if kind != "fresh" {
				if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(target, []byte(original), 0644); err != nil {
					t.Fatal(err)
				}
				if kind == "symlink" {
					if err := os.Symlink(target, path); err != nil {
						t.Fatal(err)
					}
				}
			}
			plan, err := PlanConfig(kind == "fresh")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(plan.Path); !os.IsNotExist(err) {
				t.Fatal("preview wrote config")
			}
			if !strings.Contains(plan.Content, "bind-key r display-popup") {
				t.Fatal("missing popup binding")
			}
			if strings.Contains(plan.Content, "mouse on") != (kind == "fresh") {
				t.Fatal("existing tmux should receive only essentials")
			}
			if err := plan.Apply(); err != nil {
				t.Fatal(err)
			}
			user, err := os.ReadFile(target)
			if err != nil {
				t.Fatal(err)
			}
			if kind != "fresh" && !strings.HasPrefix(string(user), original) {
				t.Fatal("existing config lost")
			}
			if strings.Count(string(user), plan.Include) != 1 {
				t.Fatal("missing or duplicate include")
			}
			if kind == "symlink" {
				info, _ := os.Lstat(path)
				if info.Mode()&os.ModeSymlink == 0 {
					t.Fatal("dotfile symlink replaced")
				}
			}
			again, err := PlanConfig(false)
			if err != nil {
				t.Fatal(err)
			}
			if err := again.Apply(); err != nil {
				t.Fatal(err)
			}
			after, _ := os.ReadFile(target)
			if string(after) != string(user) {
				t.Fatal("repeated apply changed user config")
			}
		})
	}
}

func TestTmuxConfigPlanRejectsChangesMadeDuringReview(t *testing.T) {
	home := setupHome(t)
	path := filepath.Join(home, ".tmux.conf")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanConfig(false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("user edited while wizard was open"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := plan.Apply(); err == nil {
		t.Fatal("concurrent edit was overwritten")
	}
	got, _ := os.ReadFile(path)
	if string(got) != "user edited while wizard was open" {
		t.Fatal("lost user edits")
	}
	if _, err := os.Stat(plan.Path); !os.IsNotExist(err) {
		t.Fatal("failed preflight left config")
	}
}

func TestAttachArgsAndSupportedVersions(t *testing.T) {
	args, err := (Source{}).AttachCommand(integration.SessionTarget{Name: "workspace with spaces", ID: "$12"})
	if err != nil || strings.Join(args.Args, "|") != "tmux|attach-session|-t|$12" {
		t.Fatalf("attach: %v %v", args, err)
	}
	if _, err := (Source{}).AttachCommand(integration.SessionTarget{}); err == nil {
		t.Fatal("empty target accepted")
	}
	for version, want := range map[string]bool{"tmux 3.1c": false, "tmux 3.2": true, "tmux 3.4a": true, "tmux next-3.6": true, "tmux 4.0": true, "unknown": false} {
		if SupportsPopup(version) != want {
			t.Errorf("%s: got %t", version, !want)
		}
	}
}
