package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"

	"radar/internal/protocol"
	"radar/internal/version"
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
	for _, scenario := range []struct{ mode, wantError string }{
		{"dashboard", "interactive terminal"}, {"setup", "interactive terminal"},
		{"init", "usage:"}, {"version", ""}, {"config-path", ""},
	} {
		t.Run(scenario.mode, func(t *testing.T) {
			home := t.TempDir()
			executable, _ := os.Executable()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestStartupEntrypoints$")
			cmd.Env = []string{"HOME=" + home, "PATH=" + home, "RADAR_STARTUP_TEST=" + scenario.mode}
			output, err := cmd.CombinedOutput()
			if scenario.wantError != "" {
				if err == nil || !strings.Contains(string(output), scenario.wantError) {
					t.Fatalf("error=%v output=%s", err, output)
				}
			} else if err != nil {
				t.Fatalf("error=%v output=%s", err, output)
			}
			if _, err := os.Stat(filepath.Join(home, ".config", "radar", "config.yaml")); !os.IsNotExist(err) {
				t.Fatal("informational/noninteractive command wrote config")
			}
		})
	}
}

// Run the actual dashboard in a PTY against a fixture daemon. Starting it must
// not attach/start tmux. Only Enter hands off the terminal; detach returns to
// the same dashboard, whereas switching an existing client closes its popup.
func TestDashboardOpensDirectlyAndEntersWorkspaceOnDemand(t *testing.T) {
	for _, scenario := range []struct {
		name         string
		inside, fail bool
	}{{"outside", false, false}, {"inside", true, false}, {"attachment failure", false, true}} {
		t.Run(scenario.name, func(t *testing.T) {
			inside := scenario.inside
			home := t.TempDir()
			configDir := filepath.Join(home, ".config", "radar")
			if err := os.MkdirAll(configDir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(`sbx:
  enabled: false`), 0600); err != nil {
				t.Fatal(err)
			}
			bin := filepath.Join(home, "bin")
			if err := os.Mkdir(bin, 0700); err != nil {
				t.Fatal(err)
			}
			writeStartupTools(t, bin, "")
			script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$HOME/calls\"\nexit 0\n"
			if scenario.fail {
				script = strings.Replace(script, "exit 0", "exit 23", 1)
			}
			if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			short, err := os.MkdirTemp("/tmp", "radar-start-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(short)
			socket := filepath.Join(short, "s")
			listener, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			stopped := make(chan struct{})
			defer close(stopped)
			go func() {
				for {
					conn, err := listener.Accept()
					if err != nil {
						return
					}
					go func() {
						defer conn.Close()
						var req protocol.Request
						if json.NewDecoder(conn).Decode(&req) != nil {
							return
						}
						if strings.HasPrefix(req.Method, "watch:") {
							<-stopped
							return
						}
						response := protocol.Response{OK: true, Version: version.Current(), Revision: 1, Tasks: []protocol.Task{{ID: 1, Kind: "session", Title: "Fixture workspace", Attention: "in_progress", Metadata: map[string]string{"session_id": "$7"}}}}
						_ = json.NewEncoder(conn).Encode(response)
					}()
				}
			}()
			executable, _ := os.Executable()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestStartupEntrypoints$")
			cmd.Env = []string{"HOME=" + home, "PATH=" + bin, "TERM=xterm-256color", "RADAR_SOCKET=" + socket, "RADAR_STARTUP_TEST=dashboard"}
			if inside {
				cmd.Env = append(cmd.Env, "TMUX=fixture")
			}
			terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 120})
			if err != nil {
				t.Fatal(err)
			}
			defer terminal.Close()
			defer cmd.Process.Kill()
			chunks := make(chan string, 100)
			go func() {
				defer close(chunks)
				buf := make([]byte, 4096)
				for {
					n, err := terminal.Read(buf)
					if n > 0 {
						select {
						case chunks <- string(buf[:n]):
						case <-ctx.Done():
							return
						}
					}
					if err != nil {
						return
					}
				}
			}()
			var output strings.Builder
			wait := func(text string, count int) {
				t.Helper()
				for strings.Count(output.String(), text) < count {
					select {
					case chunk, ok := <-chunks:
						if !ok {
							t.Fatalf("dashboard closed before %q: %s", text, output.String())
						}
						output.WriteString(chunk)
					case <-ctx.Done():
						t.Fatalf("waiting for %q: %s", text, output.String())
					}
				}
			}
			wait("Fixture workspace", 1)
			if probes, _ := os.ReadFile(filepath.Join(home, "tool-probes")); len(probes) != 0 {
				t.Fatalf("healthy startup ran tool probes: %s", probes)
			}
			calls, _ := os.ReadFile(filepath.Join(home, "calls"))
			if strings.Contains(string(calls), "attach-session") || strings.Contains(string(calls), "switch-client") || strings.Contains(string(calls), "new-session") || strings.Contains(string(calls), "display-popup") {
				t.Fatalf("browsing invoked tmux: %s", calls)
			}
			if _, err := terminal.WriteString("\r"); err != nil {
				t.Fatal(err)
			}
			if !inside {
				if scenario.fail {
					wait("exit status 23", 1)
				} else {
					wait("Fixture workspace", 2)
				}
				if _, err := terminal.WriteString("q"); err != nil {
					t.Fatal(err)
				}
			}
			if err := cmd.Wait(); err != nil {
				t.Fatalf("dashboard exit: %v; %s", err, output.String())
			}
			calls, _ = os.ReadFile(filepath.Join(home, "calls"))
			want := "attach-session -t $7"
			if inside {
				want = "switch-client -t $7"
			}
			var actions []string
			for _, line := range strings.Split(strings.TrimSpace(string(calls)), "\n") {
				if !strings.HasPrefix(line, "display-message") {
					actions = append(actions, line)
				}
			}
			if strings.Join(actions, "\n") != want {
				t.Fatalf("calls=%s want %s", calls, want)
			}
		})
	}
}

// Healthy startup must not execute prerequisite version probes. Other existing
// dashboard operations (e.g. Git discovery) may still use the tools normally.
// Version output lets the real setup flow validate the remaining tools
// when a missing dependency redirects the user into onboarding.
func writeStartupTools(t *testing.T, bin, missing string) {
	t.Helper()
	for name, version := range map[string]string{
		"git": "git version 2.50.0", "tmux": "tmux 3.6", "fd": "fd 10.0.0",
		"node": "v24.8.0", "npm": "11.0.0", "pi": "1.0.0", "gh": "gh version 2.80.0",
	} {
		if name == missing {
			continue
		}
		script := "#!/bin/sh\nif [ \"$1\" = --version ] || [ \"$1\" = -V ]; then\n  printf '%s\\n' '" + name + " probe' >> \"$HOME/tool-probes\"\nfi\nprintf '%s\\n' '" + version + "'\n"
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0755); err != nil {
			t.Fatal(err)
		}
	}
}

func TestConfiguredDashboardStartsSetupForMissingTools(t *testing.T) {
	for _, missing := range []string{"pi", "node", "git", "tmux", "npm", "fd", "gh"} {
		t.Run(missing, func(t *testing.T) {
			home := t.TempDir()
			configDir := filepath.Join(home, ".config", "radar")
			bin := filepath.Join(home, "bin")
			for _, dir := range []string{configDir, bin} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			original := "# existing user configuration\nsbx:\n  enabled: false\n"
			configPath := filepath.Join(configDir, "config.yaml")
			if err := os.WriteFile(configPath, []byte(original), 0600); err != nil {
				t.Fatal(err)
			}
			writeStartupTools(t, bin, missing)
			executable, _ := os.Executable()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestStartupEntrypoints$")
			cmd.Env = []string{"HOME=" + home, "PATH=" + bin, "TERM=xterm-256color", "RADAR_STARTUP_TEST=dashboard"}
			terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 120})
			if err != nil {
				t.Fatal(err)
			}
			defer terminal.Close()
			defer cmd.Process.Kill()
			output, _ := io.ReadAll(terminal)
			err = cmd.Wait()
			if ctx.Err() != nil {
				t.Fatalf("setup hung: %s", output)
			}
			if err == nil {
				t.Fatal("missing prerequisite should stop before saving")
			}
			for _, want := range []string{"Required tools missing from PATH: " + missing, "Starting Radar setup", "Welcome to Radar", "required tools are missing or too old", "Setup does not install CLI tools"} {
				if !strings.Contains(string(output), want) {
					t.Fatalf("missing %q: %s", want, output)
				}
			}
			saved, err := os.ReadFile(configPath)
			if err != nil || string(saved) != original {
				t.Fatalf("setup changed config: %s, %v", saved, err)
			}
			for _, name := range []string{"AGENTS.md", "secrets.yaml", "tmux.conf"} {
				if _, err := os.Lstat(filepath.Join(configDir, name)); !os.IsNotExist(err) {
					t.Fatalf("setup wrote %s", name)
				}
			}
		})
	}
}
