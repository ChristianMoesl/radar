package sbx

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"radar/internal/config"
	"radar/internal/setup"
	"radar/internal/setup/setuptest"
)

type installingSystem struct {
	*setuptest.System
	afterRun func(context.Context, setup.Command)
}

func (s installingSystem) Run(ctx context.Context, cmd setup.Command) error {
	err := s.System.Run(ctx, cmd)
	if err == nil && s.afterRun != nil {
		s.afterRun(ctx, cmd)
	}
	return err
}

func TestSandboxAssistedInstallRequiresConsentAndRechecksPath(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		for _, outcome := range []string{"success", "declined", "failed", "missing PATH"} {
			t.Run(platform+"/"+outcome, func(t *testing.T) {
				w, ui, sys, _ := providerFixture(t)
				sys.Goos, sys.Missing = platform, map[string]bool{"sbx": true}
				ui.Decisions = []bool{outcome != "declined"}
				if outcome == "failed" {
					sys.InstallErr = errors.New("PRIVATE_INSTALLER_DIAGNOSTIC")
				}
				w.system = installingSystem{sys, func(ctx context.Context, cmd setup.Command) {
					if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 5*time.Minute {
						t.Fatal("installer has no bounded deadline")
					}
					if platform == "darwin" && (cmd.Name != "brew" || strings.Join(cmd.Args, " ") != "install docker/tap/sbx") {
						t.Fatal("not Docker's official Homebrew cask", cmd)
					}
					if platform == "linux" && (cmd.Name != "bash" || cmd.Args[len(cmd.Args)-1] != "/usr/local") {
						t.Fatal("unexpected native Linux installation target", cmd)
					}
					if outcome == "success" {
						delete(sys.Missing, "sbx")
					}
				}}
				err := w.installSandboxCLI(context.Background())
				if (err == nil) != (outcome == "success") {
					t.Fatalf("outcome=%s err=%v", outcome, err)
				}
				if outcome == "declined" && len(sys.Calls) != 0 {
					t.Fatal("declining installation ran a command", sys.Calls)
				}
				if outcome == "missing PATH" && !strings.Contains(err.Error(), "PATH") {
					t.Fatal("successful installer was mistaken for executable readiness", err)
				}
				if err != nil && strings.Contains(err.Error(), "PRIVATE_INSTALLER_DIAGNOSTIC") {
					t.Fatal("private installer error entered setup status")
				}
			})
		}
	}
}

func TestSandboxInstalledVersionIsVerifiedBeforeRuntimePreparation(t *testing.T) {
	w, ui, sys, _ := providerFixture(t)
	sys.Missing = map[string]bool{"sbx": true}
	ui.Decisions = []bool{true}
	w.system = installingSystem{sys, func(_ context.Context, _ setup.Command) { delete(sys.Missing, "sbx") }}
	sys.SandboxFn = func(_ context.Context, args ...string) (string, error) {
		if args[0] != "version" {
			t.Fatalf("unqualified installation reached runtime preparation: %v", args)
		}
		return `{"client":{"version":"v0.47.9"}}`, nil
	}
	if err := w.prepareSandbox(context.Background(), config.SBXConfig{}, nil); err == nil {
		t.Fatal("an installed but incompatible CLI was accepted")
	}
	if len(ui.Completed) != 1 || ui.Completed[0] != "SBX installation" {
		t.Fatal("installation side effect was not recorded separately from readiness", ui.Completed)
	}
}

func TestSandboxPostLoginReadinessRetriesOnceWithBackoff(t *testing.T) {
	for _, final := range []string{"ready", "unavailable"} {
		t.Run(final, func(t *testing.T) {
			w, ui, sys, _ := providerFixture(t)
			ui.Decisions = []bool{true, false} // authenticate; decline actual VM creation
			calls := 0
			var waiting time.Time
			sys.SandboxFn = func(_ context.Context, args ...string) (string, error) {
				switch args[0] {
				case "version":
					return `{"client":{"version":"v0.50.1"}}`, nil
				case "ls":
					calls++
					switch calls {
					case 1:
						return "run sbx login", errors.New("authentication required")
					case 2:
						waiting = time.Now()
						return "PRIVATE_RUNTIME_DIAGNOSTIC", errors.New("runtime is still starting")
					case 3:
						if time.Since(waiting) < time.Second {
							t.Fatal("post-login retry ran before backoff")
						}
						if final == "ready" {
							return `[]`, nil
						}
						return "PRIVATE_RUNTIME_DIAGNOSTIC", errors.New("still unavailable")
					default:
						t.Fatal("unbounded runtime retry", calls)
					}
				case "policy":
					if final != "ready" {
						t.Fatal("runtime failure reached policy preparation")
					}
					return "", nil
				default:
					t.Fatalf("unexpected SBX command: %v", args)
				}
				return "", nil
			}
			err := w.prepareSandbox(context.Background(), config.SBXConfig{}, nil)
			if err == nil || calls != 3 {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
			if final == "ready" && !strings.Contains(err.Error(), "declined") {
				t.Fatal("ready retry did not continue through normal preparation", err)
			}
			if final == "unavailable" && !strings.Contains(err.Error(), "authentication/runtime check failed") {
				t.Fatal("failed retry did not report unverified readiness", err)
			}
			if strings.Contains(err.Error()+ui.Transcript.String(), "PRIVATE_RUNTIME_DIAGNOSTIC") {
				t.Fatal("runtime diagnostic leaked")
			}
		})
	}
}

func TestSandboxPostLoginBackoffRespectsCancellation(t *testing.T) {
	w, ui, sys, _ := providerFixture(t)
	ui.Decisions = []bool{true}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	sys.SandboxFn = func(_ context.Context, args ...string) (string, error) {
		if args[0] == "version" {
			return `{"client":{"version":"v0.50.1"}}`, nil
		}
		calls++
		if calls == 1 {
			return "run sbx login", errors.New("not authenticated")
		}
		cancel()
		return "", errors.New("starting")
	}
	if err := w.prepareSandbox(ctx, config.SBXConfig{}, nil); !errors.Is(err, context.Canceled) || calls != 2 {
		t.Fatalf("err=%v checks=%d", err, calls)
	}
}

func TestSandboxLinuxInstallerVerifiesBeforeExtractionOrExecution(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	// No network, privilege changes or real archive installation in this test.
	for name, body := range map[string]string{
		"uname":     "printf x86_64",
		"curl":      "exit 0",
		"sha256sum": "exit 1",
		"tar":       "touch \"$SBX_UNVERIFIED_EXTRACTION\"",
		"sudo":      "touch \"$SBX_UNVERIFIED_EXTRACTION\"",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	marker := filepath.Join(dir, "unverified-execution")
	cmd := exec.Command("bash", "-c", linuxSBXInstall, "radar-install-sbx", filepath.Join(dir, "prefix"))
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "SBX_UNVERIFIED_EXTRACTION="+marker)
	if output, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("failed checksum accepted: %s", output)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("unverified download was extracted or executed")
	}
}
