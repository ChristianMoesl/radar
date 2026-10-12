package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestPrerequisiteInstallerConsentAndRechecks(t *testing.T) {
	for _, tool := range []string{"git", "tmux", "fd", "node", "npm", "pi", "gh"} {
		for _, mode := range []string{"approve default", "decline", "failed", "still old", "headless"} {
			t.Run(tool+"/"+mode, func(t *testing.T) {
				bin := readyTools(t)
				home := t.TempDir()
				ready, err := os.ReadFile(filepath.Join(bin, tool))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(home, "ready"), ready, 0755); err != nil {
					t.Fatal(err)
				}
				write := func(name, text string) {
					t.Helper()
					if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+text+"\n"), 0755); err != nil {
						t.Fatal(err)
					}
				}
				write(tool, "exit 1")
				// Never use the host package manager, even on macOS CI.
				write("uname", "echo Linux")
				manager := `if [ "$1" = --version ]; then echo 11.0; exit; fi
 echo install >> "$HOME/calls"
 if [ "$MODE" = failed ]; then exit 7; fi
 if [ "$MODE" != 'still old' ]; then /bin/cp "$HOME/ready" "$TOOLS/$TOOL"; fi`
				write("brew", manager)
				if tool == "pi" {
					write("npm", manager)
				}
				cmd := exec.Command("bash", "-c", "set -euo pipefail; source ./install-prerequisites.sh; radar_install_prerequisites")
				cmd.Env = append(filteredEnvironment("BASH_ENV", "ENV", "HOME", "PATH", "NODE_OPTIONS", "NODE_PATH"), "HOME="+home, "PATH="+bin+":"+os.Getenv("PATH"), "TOOLS="+bin, "TOOL="+tool, "MODE="+mode)
				var output []byte
				if mode == "headless" {
					cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
					cmd.Stdin = strings.NewReader("y\n")
					output, err = cmd.CombinedOutput()
				} else {
					answer := "\n"
					if mode == "decline" {
						answer = "n\n"
					}
					output, err = runBootstrapPTY(t, cmd, answer)
				}
				if (err == nil) != (mode == "approve default") {
					t.Fatalf("%v: %s", err, output)
				}
				calls, _ := os.ReadFile(filepath.Join(home, "calls"))
				expected := mode != "headless" && mode != "decline"
				if strings.Contains(string(calls), "install") != expected {
					t.Fatalf("consent crossed: %s", calls)
				}
				if mode == "approve default" && strings.Count(string(calls), "install") != 1 {
					t.Fatalf("duplicate installation: %s", calls)
				}
			})
		}
	}
}

func TestPrerequisiteInstallerAcceptsLinuxFdfind(t *testing.T) {
	bin := readyTools(t)
	if err := os.Rename(filepath.Join(bin, "fd"), filepath.Join(bin, "fdfind")); err != nil {
		t.Fatal(err)
	}
	// Isolate fd lookup from a real host fd while retaining the shell utilities.
	script := `source ./install-prerequisites.sh
 command() { if [[ "$*" = '-v fd' ]]; then return 1; else builtin command "$@"; fi; }
 uname() { echo Linux; }
 radar_tool_ready fd`
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = append(filteredEnvironment("BASH_ENV", "ENV", "PATH"), "PATH="+bin+":"+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
}

func TestPrerequisiteInstallerLinuxPackageManagers(t *testing.T) {
	for _, manager := range []string{"apt-get", "dnf", "pacman"} {
		t.Run(manager, func(t *testing.T) {
			for _, mode := range []string{"approve default", "decline", "distribution Node too old"} {
				t.Run(mode, func(t *testing.T) {
					bin, home := readyTools(t), t.TempDir()
					tool := "gh"
					ready := "#!/bin/sh\necho gh\n"
					if mode == "distribution Node too old" {
						tool, ready = "node", "#!/bin/sh\necho v22.0.0\n"
					}
					for path, content := range map[string]string{
						filepath.Join(home, "ready"): ready,
						filepath.Join(bin, tool):     "#!/bin/sh\nexit 1\n",
						filepath.Join(bin, "id"):     "#!/bin/sh\necho 501\n",
						filepath.Join(bin, "sudo"):   "#!/bin/sh\nexec \"$@\"\n",
						filepath.Join(bin, manager): `#!/bin/sh
printf '%s\n' "$*" >> "$HOME/calls"
/bin/cp "$HOME/ready" "$TOOLS/$TOOL"
`,
					} {
						if err := os.WriteFile(path, []byte(content), 0755); err != nil {
							t.Fatal(err)
						}
					}
					script := `set -euo pipefail
source ./install-prerequisites.sh
command() { if [[ $1 = -v && ( $2 = brew || $2 = apt-get || $2 = dnf || $2 = pacman ) && $2 != $MANAGER ]]; then return 1; else builtin command "$@"; fi; }
uname() { echo Linux; }
radar_install_prerequisites`
					cmd := exec.Command("bash", "-c", script)
					cmd.Env = append(filteredEnvironment("BASH_ENV", "ENV", "HOME", "PATH"), "HOME="+home, "PATH="+bin+":"+os.Getenv("PATH"), "TOOLS="+bin, "TOOL="+tool, "MANAGER="+manager)
					answer := "\n"
					if mode == "decline" {
						answer = "n\n"
					}
					output, err := runBootstrapPTY(t, cmd, answer)
					if (err == nil) != (mode == "approve default") {
						t.Fatalf("%v: %s", err, output)
					}
					calls, _ := os.ReadFile(filepath.Join(home, "calls"))
					want := "install -y gh\n"
					if mode == "decline" {
						want = ""
					}
					if mode == "distribution Node too old" {
						want = "install -y nodejs npm\n"
					}
					if manager == "pacman" {
						want = strings.Replace(want, "install -y", "-S --needed --noconfirm", 1)
					}
					if string(calls) != want {
						t.Fatalf("package-manager calls = %q, want %q", calls, want)
					}
				})
			}
		})
	}
}

func TestPrerequisiteInstallerPiUsesUserDirectory(t *testing.T) {
	bin, home := readyTools(t), t.TempDir()
	for name, script := range map[string]string{
		"pi":    "exit 1",
		"uname": "echo Linux",
		"sudo":  "exit 99",
		"npm": `if [ "$1" = --version ]; then echo 11.0; exit; fi
printf '%s\n' "$*" > "$HOME/calls"
mkdir -p "$HOME/.local/bin"
printf '#!/bin/sh\necho 0.85.1\n' > "$HOME/.local/bin/pi"
chmod +x "$HOME/.local/bin/pi"`,
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+script+"\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("bash", "-c", "set -euo pipefail; source ./install-prerequisites.sh; radar_install_prerequisites")
	cmd.Env = append(filteredEnvironment("BASH_ENV", "ENV", "HOME", "PATH"), "HOME="+home, "PATH="+bin+":"+os.Getenv("PATH"))
	output, err := runBootstrapPTY(t, cmd, "\n")
	if err != nil {
		t.Fatalf("%v: %s", err, output)
	}
	calls, _ := os.ReadFile(filepath.Join(home, "calls"))
	if string(calls) != "install --global --prefix "+filepath.Join(home, ".local")+" --ignore-scripts @earendil-works/pi-coding-agent\n" {
		t.Fatalf("Pi was not installed without root into its user directory: %s", calls)
	}
}

func TestInstallersRejectIntelMacBeforeAnyChanges(t *testing.T) {
	for _, script := range []string{"prerequisites", "notifier", "notifier build"} {
		t.Run(script, func(t *testing.T) {
			bin := t.TempDir()
			if err := os.WriteFile(filepath.Join(bin, "uname"), []byte("#!/bin/sh\nif [ \"$1\" = -s ]; then echo Darwin; else echo x86_64; fi\n"), 0755); err != nil {
				t.Fatal(err)
			}
			home := t.TempDir()
			command := "source ./install-prerequisites.sh; radar_install_prerequisites"
			if script == "notifier" {
				command = "bash ./install-notifier.sh missing.app \"$HOME/output.app\""
			} else if script == "notifier build" {
				command = "bash ./build-notifier-app.sh \"$HOME/output.app\" amd64"
			}
			cmd := exec.Command("bash", "-c", command)
			cmd.Env = append(filteredEnvironment("BASH_ENV", "ENV", "HOME", "PATH"), "HOME="+home, "PATH="+bin+":"+os.Getenv("PATH"))
			output, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("accepted Intel macOS: %s", output)
			}
			if _, err := os.Stat(filepath.Join(home, "output.app")); !os.IsNotExist(err) {
				t.Fatal("created an unsupported installation")
			}
		})
	}
}

func TestReleaseTargetsPreserveLinuxAndRejectIntelMac(t *testing.T) {
	for _, target := range []string{"linux/amd64", "linux/arm64", "darwin/arm64", "darwin/amd64"} {
		cmd := exec.Command("make", "-n", "dist", "DIST_TARGETS="+target)
		cmd.Dir = ".."
		output, err := cmd.CombinedOutput()
		if (err == nil) != (target != "darwin/amd64") {
			t.Fatalf("target %s: %v: %s", target, err, output)
		}
	}
	cmd := exec.Command("make", "build-radar")
	cmd.Dir = ".."
	cmd.Env = append(filteredEnvironment("GOOS", "GOARCH"), "GOOS=darwin", "GOARCH=amd64")
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "requires Apple Silicon") {
		t.Fatalf("Intel source build was not rejected: %v: %s", err, output)
	}
}
