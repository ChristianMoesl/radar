package tmux

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

// The server uses a private socket and disposable HOME. These are the actual
// production argv/config, not a fake tmux parser. Keep hermetic stub coverage as
// well so the normal Go suite remains runnable without an installed tmux.
func TestRealTmuxPopupAndPrefixBinding(t *testing.T) {
	binary, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	for _, running := range []bool{false, true} {
		t.Run(fmt.Sprint("running=", running), func(t *testing.T) {
			home := setupHome(t)
			t.Setenv("TMUX", "")
			t.Setenv("TERM", "xterm-256color")
			// Keep Unix socket paths short on macOS.
			dir, err := os.MkdirTemp("/tmp", "radar-tmux-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			socket := filepath.Join(dir, "s")
			tmux := func(args ...string) *exec.Cmd {
				return exec.Command(binary, append([]string{"-S", socket, "-f", filepath.Join(home, ".tmux.conf")}, args...)...)
			}
			t.Cleanup(func() { _ = tmux("kill-server").Run() })
			if running {
				if err := os.WriteFile(filepath.Join(home, ".tmux.conf"), []byte("set -g prefix C-a\nbind-key r display-message 'old binding'\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			plan, err := PlanConfig(!running)
			if err != nil {
				t.Fatal(err)
			}
			if err := plan.Apply(); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(home, "popup-result")
			// Name/path deliberately include shell metacharacters and spaces.
			script := filepath.Join(home, "radar's test binary")
			body := "#!/bin/sh\nprintf '%s|%s\\n' \"$TMUX\" \"$PWD\" >> " + shellQuote(marker) + "\n"
			if err := os.WriteFile(script, []byte(body), 0755); err != nil {
				t.Fatal(err)
			}
			bin := filepath.Join(home, "bin")
			if err := os.Mkdir(bin, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(script, filepath.Join(bin, "radar")); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			if running {
				if output, err := tmux("new-session", "-d", "-s", "existing", "-c", home).CombinedOutput(); err != nil {
					t.Fatalf("start server: %v %s", err, output)
				}
			}
			cmd := tmux(dashboardArgs(running, script, home)...)
			terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 140})
			if err != nil {
				t.Fatal(err)
			}
			defer terminal.Close()
			go func() { _, _ = io.Copy(io.Discard, terminal) }()
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			t.Cleanup(func() { _ = cmd.Process.Kill() })
			waitPopup := func(count int) {
				t.Helper()
				for deadline := time.Now().Add(6 * time.Second); time.Now().Before(deadline); {
					data, _ := os.ReadFile(marker)
					if strings.Count(string(data), "\n") >= count {
						if !strings.Contains(string(data), socket) || !strings.Contains(string(data), "|"+home) {
							t.Fatalf("popup lacks TMUX/current directory: %s", data)
						}
						return
					}
					time.Sleep(20 * time.Millisecond)
				}
				t.Fatalf("popup %d did not run", count)
			}
			waitPopup(1)
			if output, err := tmux("list-keys", "-T", "prefix", "r").CombinedOutput(); err != nil || !strings.Contains(string(output), "display-popup") {
				t.Fatalf("prefix binding: %v %s", err, output)
			}
			// Exercise the configured keystroke through the terminal client, not a
			// second direct display-popup command. Existing C-a is preserved; a fresh
			// installation retains tmux's standard C-b prefix.
			time.Sleep(100 * time.Millisecond)
			prefix := byte(2)
			if running {
				prefix = 1
			}
			if _, err := terminal.Write([]byte{prefix, 'r'}); err != nil {
				t.Fatal(err)
			}
			waitPopup(2)
			if output, err := tmux("list-sessions", "-F", "#{session_name}").CombinedOutput(); err != nil || running && strings.TrimSpace(string(output)) != "existing" {
				t.Fatalf("existing session was not reused: %v %s", err, output)
			}
			if _, err := terminal.Write([]byte{prefix, 'd'}); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("client did not detach")
			}
		})
	}
}
