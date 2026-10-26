package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"

	"radar/internal/integration"
	"radar/internal/integration/obsidian"
	"radar/internal/integration/tmux"
	"radar/internal/integration/workspace"
	"radar/internal/linking"
	"radar/internal/protocol"
	"radar/internal/version"
)

// Run the real dashboard and workspace opener against a private tmux server.
// The fixture daemon deliberately retains the observation collected before the
// session was killed, reproducing Enter before the next successful collection.
func TestDashboardRecreatesClosedWorkspaceSessionE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("starts an isolated tmux server and dashboard")
	}
	tmuxBinary, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	for _, inside := range []bool{true, false} {
		t.Run(fmt.Sprintf("inside_tmux=%v", inside), func(t *testing.T) {
			// Both daemon and tmux Unix socket paths must stay short on macOS.
			home, err := os.MkdirTemp("/tmp", "radar-reopen-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(home) })
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			bin, root := filepath.Join(home, "bin"), filepath.Join(home, "workspaces")
			configHome, vault := filepath.Join(home, "config"), filepath.Join(home, "vault")
			for _, dir := range []string{bin, filepath.Join(configHome, "radar"), filepath.Join(vault, ".obsidian")} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", configHome)
			t.Setenv("RADAR_STATE", filepath.Join(home, "state.db"))
			t.Setenv("RADAR_SOCKET", filepath.Join(home, "daemon.sock"))
			t.Setenv("TMUX", "")
			t.Setenv("TMUX_PANE", "")
			t.Setenv("TERM", "xterm-256color")
			if err := os.WriteFile(filepath.Join(configHome, "radar", "config.yaml"), []byte(fmt.Sprintf("workspace:\n  root_dir: %q\nsbx:\n  enabled: false\n", root)), 0600); err != nil {
				t.Fatal(err)
			}
			writeStartupTools(t, bin, "")
			// Only redirect the socket; every tmux operation is real.
			wrapper := "#!/bin/sh\nexec " + quoteReopenE2E(tmuxBinary) + " -S " + quoteReopenE2E(filepath.Join(home, "tmux.sock")) + " -f /dev/null \"$@\"\n"
			if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(wrapper), 0755); err != nil {
				t.Fatal(err)
			}
			pi := "#!/bin/sh\nprintf 'workspace ready\\n'\nIFS= read -r line\nprintf '%s' \"$line\" > \"$HOME/workspace-input\"\nexec /bin/sleep 60\n"
			if err := os.WriteFile(filepath.Join(bin, "pi"), []byte(pi), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			runTmux := func(args ...string) string {
				t.Helper()
				output, err := exec.CommandContext(ctx, "tmux", args...).CombinedOutput()
				if err != nil {
					t.Fatalf("tmux %v: %v\n%s", args, err, output)
				}
				return strings.TrimSpace(string(output))
			}
			t.Cleanup(func() { _ = exec.Command("tmux", "kill-server").Run() })
			// Keep the server alive while the workspace session is absent.
			runTmux("new-session", "-d", "-s", "dashboard", "sleep 60")
			runTmux("set-option", "-g", "default-shell", "/bin/sh")
			author := obsidian.NewSourceAt(vault)
			created, err := workspace.Create(ctx, workspace.ExecRunner{}, workspace.CreateOptions{
				Name: "ABC-123 Recovery", WorkspaceRoot: root, NoteAuthor: author,
			})
			if err != nil {
				t.Fatal(err)
			}
			oldID := runTmux("display-message", "-p", "-t", created.SessionName, "#{session_id}")
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			sessions, status := tmux.FetchSessions(ctx, logger, linking.MarkMatcher{})
			if status.Status != "ok" {
				t.Fatalf("collect tmux: %+v", status)
			}
			anchors := workspace.NewSource(author).Collect(ctx, integration.CollectRequest{Logger: logger})
			if !anchors.Complete || len(anchors.Observations) != 1 {
				t.Fatalf("collect workspace: %+v", anchors)
			}
			refs := []protocol.SourceRef{anchors.Observations[0].Ref}
			for _, ref := range sessions {
				if ref.Metadata["session_id"] == oldID {
					refs = append(refs, ref)
				}
			}
			if len(refs) != 2 {
				t.Fatal("missing cached workspace session observation")
			}
			runTmux("kill-session", "-t", created.SessionName)
			if err := exec.CommandContext(ctx, "tmux", "has-session", "-t", "="+created.SessionName).Run(); err == nil {
				t.Fatal("workspace session was not closed")
			}

			listener, err := net.Listen("unix", os.Getenv("RADAR_SOCKET"))
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			go func() {
				for {
					conn, err := listener.Accept()
					if err != nil {
						return
					}
					go func() {
						defer conn.Close()
						var request protocol.Request
						if json.NewDecoder(conn).Decode(&request) != nil {
							return
						}
						if strings.HasPrefix(request.Method, "watch:") {
							<-ctx.Done()
							return
						}
						_ = json.NewEncoder(conn).Encode(protocol.Response{OK: true, Version: version.Current(), Revision: 1,
							Tasks: []protocol.Task{{ID: 1, Title: "ABC-123 Recovery", Attention: "in_progress", SourceRefs: refs}}})
					}()
				}
			}()
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			// The existing subprocess entrypoint calls main(), including startup,
			// the actual Bubble Tea loop, workspace recovery and attach/switch.
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestStartupEntrypoints$")
			cmd.Env = append(os.Environ(), "RADAR_STARTUP_TEST=dashboard")
			if inside {
				runTmux("respawn-pane", "-k", "-t", "dashboard", "env RADAR_STARTUP_TEST=dashboard "+quoteReopenE2E(executable)+" -test.run=^TestStartupEntrypoints$")
				cmd = exec.CommandContext(ctx, "tmux", "attach-session", "-t", "dashboard")
			}
			terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 120})
			if err != nil {
				t.Fatal(err)
			}
			defer terminal.Close()
			defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
			chunks := make(chan string, 100)
			go func() {
				defer close(chunks)
				buffer := make([]byte, 4096)
				for {
					n, err := terminal.Read(buffer)
					if n > 0 {
						select {
						case chunks <- string(buffer[:n]):
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
			for !strings.Contains(output.String(), "ABC-123 Recovery") {
				select {
				case chunk, ok := <-chunks:
					if !ok {
						t.Fatalf("dashboard exited: %s", output.String())
					}
					output.WriteString(chunk)
				case <-ctx.Done():
					t.Fatalf("dashboard did not load: %s", output.String())
				}
			}
			if _, err := terminal.WriteString("\r"); err != nil {
				t.Fatal(err)
			}
			wait := func(description string, condition func() bool) {
				t.Helper()
				for deadline := time.Now().Add(6 * time.Second); time.Now().Before(deadline); {
					if condition() {
						return
					}
					time.Sleep(20 * time.Millisecond)
				}
				capture, _ := exec.CommandContext(ctx, "tmux", "capture-pane", "-p", "-t", "dashboard").CombinedOutput()
				t.Fatalf("timed out waiting for %s\n%s", description, capture)
			}
			wait("the client to enter the recreated workspace", func() bool {
				return runTmux("list-clients", "-F", "#{session_name}") == created.SessionName
			})
			if newID := runTmux("display-message", "-p", "-t", created.SessionName, "#{session_id}"); newID == oldID {
				t.Fatal("reopened the stale session instead of creating a new one")
			}
			wait("the workspace pane to be ready", func() bool {
				return strings.Contains(runTmux("capture-pane", "-p", "-t", created.SessionName), "workspace ready")
			})
			if _, err := terminal.WriteString("recovered-input\r"); err != nil {
				t.Fatal(err)
			}
			wait("input to reach the reopened workspace", func() bool {
				data, _ := os.ReadFile(filepath.Join(home, "workspace-input"))
				return string(data) == "recovered-input"
			})
			if count := strings.Count(runTmux("list-sessions", "-F", "#{session_name}"), created.SessionName); count != 1 {
				t.Fatalf("workspace session count = %d, want exactly one", count)
			}
		})
	}
}

func quoteReopenE2E(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
