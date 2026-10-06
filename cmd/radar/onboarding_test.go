package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStartupEntrypoints(t *testing.T) {
	if mode := os.Getenv("RADAR_STARTUP_TEST"); mode != "" {
		os.Args = []string{"radar"}
		if mode != "dashboard" {
			os.Args = append(os.Args, mode)
		}
		main()
		os.Exit(0)
	}
	for _, scenario := range []struct {
		name, mode          string
		configured, running bool
		wantError           string
	}{
		{name: "first start needs terminal", mode: "dashboard", wantError: "interactive terminal"},
		{name: "explicit init needs terminal", mode: "init", wantError: "interactive terminal"},
		{name: "version without onboarding", mode: "version"},
		{name: "config path without onboarding", mode: "config-path"},
		{name: "start tmux", mode: "dashboard", configured: true},
		{name: "attach tmux", mode: "dashboard", configured: true, running: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			home := t.TempDir()
			configDir := filepath.Join(home, ".config", "radar")
			if scenario.configured {
				if err := os.MkdirAll(configDir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			bin := filepath.Join(home, "bin")
			if err := os.Mkdir(bin, 0700); err != nil {
				t.Fatal(err)
			}
			exit := "1"
			if scenario.running {
				exit = "0"
			}
			script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$HOME/calls\"\ncase \"$1\" in\n-V) echo 'tmux 3.6';;\nhas-session) exit " + exit + ";;\n*) exit 0;;\nesac\n"
			if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestStartupEntrypoints$")
			cmd.Env = []string{"HOME=" + home, "PATH=" + bin, "RADAR_STARTUP_TEST=" + scenario.mode}
			output, err := cmd.CombinedOutput()
			if scenario.wantError != "" {
				if err == nil || !strings.Contains(string(output), scenario.wantError) {
					t.Fatalf("error=%v output=%s", err, output)
				}
			} else if err != nil {
				t.Fatalf("error=%v output=%s", err, output)
			}
			calls, _ := os.ReadFile(filepath.Join(home, "calls"))
			if scenario.configured {
				want := "new-session"
				if scenario.running {
					want = "attach-session"
				}
				if !strings.Contains(string(calls), want) || !strings.Contains(string(calls), "display-popup") {
					t.Fatalf("calls: %s", calls)
				}
			} else {
				if len(calls) > 0 {
					t.Fatalf("unexpected commands: %s", calls)
				}
				if _, err := os.Stat(filepath.Join(configDir, "config.json")); !os.IsNotExist(err) {
					t.Fatal("informational/noninteractive command wrote config")
				}
			}
		})
	}
}
