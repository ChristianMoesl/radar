package scripts_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"radar/internal/client"
)

func TestReleaseInstallAndFirstLaunch(t *testing.T) {
	// Build once, then exercise the actual release installer, installed executable,
	// and daemon with no host integrations or credentials available.
	archive := filepath.Join(t.TempDir(), "release archive")
	if err := os.MkdirAll(filepath.Join(archive, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-o", filepath.Join(archive, "bin", "radar"), "../cmd/radar")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	for _, file := range []struct{ source, destination string }{
		{"../Makefile", "Makefile"},
		{"../LICENSE", "LICENSE"},
		{"install.sh", "install.sh"},
		{"install-prerequisites.sh", "scripts/install-prerequisites.sh"},
		{"install-agent-instructions.sh", "install-agent-instructions.sh"},
		{"install-agent-instructions.sh", "scripts/install-agent-instructions.sh"},
		{"../internal/pi/default-AGENTS.md", "share/radar/AGENTS.md"},
		{filepath.Join(archive, "bin", "radar"), "radar"},
	} {
		data, err := os.ReadFile(file.source)
		if err != nil {
			t.Fatal(err)
		}
		if file.destination == "install.sh" {
			data = bundledInstaller(t)
		}
		path := filepath.Join(archive, file.destination)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0755); err != nil {
			t.Fatal(err)
		}
	}

	manuals := exec.Command("go", "run", "../internal/cmd/manpages", "--output", filepath.Join(archive, "share/man"), "--version", "v1.2.3")
	if output, err := manuals.CombinedOutput(); err != nil {
		t.Fatalf("manuals: %v %s", err, output)
	}
	for _, page := range []string{"man1/radar.1", "man5/radar-config.5"} {
		data, err := os.ReadFile(filepath.Join(archive, "share/man", page))
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(archive, "build/man", page)
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, data, 0644); err != nil {
			t.Fatal(err)
		}
	}

	for _, scenario := range []struct {
		name                                string
		xdg, prefix, bindir, source, mandir bool
	}{
		{name: "home defaults"},
		{name: "XDG and custom prefix", xdg: true, prefix: true},
		{name: "custom binary directory", xdg: true, prefix: true, bindir: true},
		{name: "custom manual directory", xdg: true, prefix: true, mandir: true},
		{name: "custom source manual directory", xdg: true, prefix: true, mandir: true, source: true},
		{name: "source install with spaces", xdg: true, prefix: true, bindir: true, source: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			home := filepath.Join(t.TempDir(), "user home")
			configHome := filepath.Join(home, ".config")
			prefix := filepath.Join(home, ".local")
			env := append(filteredEnvironment("BASH_ENV", "ENV", "HOME", "XDG_CONFIG_HOME", "PREFIX", "BINDIR", "LIBEXECDIR", "MANDIR"), "HOME="+home, "PATH="+readyTools(t)+":"+os.Getenv("PATH"))
			if scenario.xdg {
				configHome = filepath.Join(home, "settings")
				env = append(env, "XDG_CONFIG_HOME="+configHome)
			}
			if scenario.prefix {
				prefix = filepath.Join(home, "custom prefix")
				env = append(env, "PREFIX="+prefix)
			}
			bindir := filepath.Join(prefix, "bin")
			if scenario.bindir {
				bindir = filepath.Join(home, "executables")
				env = append(env, "BINDIR="+bindir)
			}
			mandir := filepath.Join(prefix, "share/man")
			if scenario.mandir {
				mandir = filepath.Join(home, "custom manual directory")
				env = append(env, "MANDIR="+mandir)
			}
			install := func() {
				t.Helper()
				command := exec.Command("bash", filepath.Join(archive, "install.sh"))
				if scenario.source {
					// The binary is already built. Notifier packaging is covered separately by
					// macOS CI; exercise the portable source-install recipe here on both OSes.
					command = exec.Command("make", "install", "-o", "build", "HOST_OS=Linux", "AGENT_INSTRUCTIONS_TEMPLATE=share/radar/AGENTS.md")
					command.Dir = archive
				}
				command.Env = env
				if output, err := command.CombinedOutput(); err != nil {
					t.Fatalf("install: %v\n%s", err, output)
				}
			}
			install()
			executable := filepath.Join(bindir, "radar")
			assertMode(t, executable, 0755)
			for _, page := range []string{"man1/radar.1", "man5/radar-config.5"} {
				installed := filepath.Join(mandir, page)
				assertMode(t, installed, 0644)
				data, err := os.ReadFile(installed)
				if err != nil || !bytes.Contains(data, []byte("Radar v1.2.3")) {
					t.Fatalf("installed manual: %v %s", err, data)
				}
			}
			licensePath := filepath.Join(prefix, "share", "radar", "LICENSE")
			assertMode(t, licensePath, 0644)
			license, err := os.ReadFile(licensePath)
			if err != nil {
				t.Fatal(err)
			}
			wantLicense, err := os.ReadFile("../LICENSE")
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(license, wantLicense) {
				t.Fatal("installer must preserve the MIT license notice")
			}
			instructions := filepath.Join(configHome, "radar", "AGENTS.md")
			assertInstalledInstructions(t, instructions)
			configPath := filepath.Join(configHome, "radar", "config.yaml")
			if _, err := os.Stat(configPath); !os.IsNotExist(err) {
				t.Fatal("installer should leave config creation to first launch")
			}
			// Use a short socket path on macOS; long HOME paths remain part of the test.
			runtimeDir, err := os.MkdirTemp("/tmp", "radar-install-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(runtimeDir)
			emptyPath := filepath.Join(home, "no-tools")
			if err := os.Mkdir(emptyPath, 0755); err != nil {
				t.Fatal(err)
			}
			socketPath := filepath.Join(runtimeDir, "radar.sock")
			// No inherited environment: in particular no credentials, state overrides,
			// test collection-disable switch, notifier, or ps discovery of host daemons.
			runtimeEnv := []string{
				"HOME=" + home, "PATH=" + emptyPath, "XDG_CONFIG_HOME=" + configHome,
				"XDG_DATA_HOME=" + filepath.Join(home, "data"), "XDG_STATE_HOME=" + filepath.Join(home, "state"),
				"RADAR_SOCKET=" + socketPath, "RADAR_PID=" + filepath.Join(runtimeDir, "radar.pid"),
			}
			version := exec.Command(executable, "version")
			version.Env = runtimeEnv
			if output, err := version.CombinedOutput(); err != nil || !strings.Contains(string(output), "radar") {
				t.Fatalf("version: %v %s", err, output)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			daemon := exec.CommandContext(ctx, executable, "daemon")
			daemon.Env = runtimeEnv
			var output bytes.Buffer
			daemon.Stdout, daemon.Stderr = &output, &output
			if err := daemon.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				cancel()
				_ = daemon.Wait()
				if t.Failed() {
					t.Log(output.String())
				}
			}()
			ready := false
			for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
				response, err := client.CallWithTimeout(socketPath, "summary", time.Second)
				if err == nil && response.OK && len(response.Sources) > 0 {
					for _, status := range response.Sources {
						if status.Status == "error" {
							t.Fatalf("first-run source failure: %+v", status)
						}
					}
					ready = true
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if !ready {
				t.Fatal("installed daemon did not become ready with usable source status")
			}
			if _, err := os.Stat(configPath); !os.IsNotExist(err) {
				t.Fatal("background daemon must not create config.yaml and bypass first-run consent")
			}

			// An upgrade must preserve both user-owned files byte for byte.
			customConfig := []byte(`{sbx: {enabled: false}, github: {enabled: false}}` + "\n")
			customInstructions := []byte("User-owned instructions\n")
			if err := os.WriteFile(configPath, customConfig, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(instructions, customInstructions, 0600); err != nil {
				t.Fatal(err)
			}
			install()
			for path, want := range map[string][]byte{configPath: customConfig, instructions: customInstructions} {
				got, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(got, want) {
					t.Fatalf("upgrade changed %s: %v %q", path, err, got)
				}
			}
		})
	}
}

func readyTools(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, version := range map[string]string{"git": "git version 2.50", "tmux": "tmux 3.6", "fd": "fd 10.0", "node": "v24.0.0", "npm": "11.0", "pi": "0.85.1", "gh": "gh version 2.70"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\necho '"+version+"'\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// Match make dist: helper functions are embedded in the existing archive member,
// not added as a new file that already-installed updaters would reject.
func bundledInstaller(t *testing.T) []byte {
	t.Helper()
	var out strings.Builder
	out.Write(mustReadScript(t, "install-prerequisites.sh"))
	for _, line := range strings.Split(string(mustReadScript(t, "install.sh")), "\n") {
		if !strings.HasPrefix(line, "source ") {
			out.WriteString(line + "\n")
		}
	}
	return []byte(out.String())
}
