package main

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"radar/internal/protocol"
	"radar/internal/version"
)

// Exercise the real entry points without opening a TUI or touching the user's
// daemon. Login failure or a sentinel refresh failure stops before terminal use.
func TestForegroundAuthentication(t *testing.T) {
	if mode := os.Getenv("RADAR_TEST_AUTH_MODE"); mode != "" {
		switch mode {
		case "startup":
			runTUI()
		case "create", "fork":
			runTUIWithMode(mode)
		case "cli-create":
			runCreate([]string{"--name", "ABC-123"})
		}
		os.Exit(0)
	}
	for _, tt := range []struct {
		mode          string
		loginSucceeds bool
	}{
		{"startup", false}, {"create", false}, {"fork", false},
		{"cli-create", false}, {"startup", true},
	} {
		name := tt.mode
		if tt.loginSucceeds {
			name += " refreshes after login"
		}
		t.Run(name, func(t *testing.T) {
			dir, err := os.MkdirTemp("/tmp", "radar-auth-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			logPath := filepath.Join(dir, "calls")
			script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$RADAR_TEST_AUTH_LOG\"\n" +
				"case \"$1\" in\nls) echo \"Sign-in required\" >&2; exit 1;;\nlogin) exit "
			if tt.loginSucceeds {
				script += "0"
			} else {
				script += "1"
			}
			script += ";;\n*) exit 99;;\nesac\n"
			if err := os.WriteFile(filepath.Join(dir, "sbx"), []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			socketPath := filepath.Join(dir, "s")
			listener, err := net.Listen("unix", socketPath)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			refreshed := make(chan struct{}, 1)
			go func() {
				for {
					conn, err := listener.Accept()
					if err != nil {
						return
					}
					var req protocol.Request
					_ = json.NewDecoder(conn).Decode(&req)
					response := protocol.Response{OK: true, Version: version.Current()}
					switch req.Method {
					case "tasks":
						if tt.mode == "startup" {
							response.Sources = []protocol.SourceStatus{{Name: "sbx", Status: "error", Detail: "not signed in; run sbx login"}}
						}
					case "refresh-local":
						refreshed <- struct{}{}
						response.OK, response.Error = false, "auth test refresh reached"
					}
					_ = json.NewEncoder(conn).Encode(response)
					_ = conn.Close()
				}
			}()
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestForegroundAuthentication$")
			cmd.Env = []string{
				"PATH=" + dir, "HOME=" + dir, "RADAR_SOCKET=" + socketPath,
				"RADAR_TEST_AUTH_MODE=" + tt.mode, "RADAR_TEST_AUTH_LOG=" + logPath,
			}
			output, err := cmd.CombinedOutput()
			wantError := "sbx login failed"
			if tt.loginSucceeds {
				wantError = "auth test refresh reached"
			}
			if err == nil || ctx.Err() != nil || !strings.Contains(string(output), wantError) {
				t.Fatalf("foreground result = %v, context = %v, output = %s; want %q", err, ctx.Err(), output, wantError)
			}
			data, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Fields(string(data)); !reflect.DeepEqual(got, []string{"ls", "--json", "login"}) {
				t.Fatalf("SBX calls = %q", data)
			}
			if got := len(refreshed) > 0; got != tt.loginSucceeds {
				t.Fatalf("refreshed = %t, want %t", got, tt.loginSucceeds)
			}
		})
	}
}
