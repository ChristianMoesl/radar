package onboarding

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"radar/internal/config"
	"radar/internal/integration/sbx/auth"
)

const sandboxGuide = "https://github.com/ChristianMoesl/radar/blob/main/docs/integrations/sbx.md"
const sandboxQuestion = "Enable SBX for new workspaces?"

func (w *wizard) sandboxSetup(ctx context.Context, draft *config.SetupDraft, path string, cfg *config.Config) (*sandboxSetupPlan, error) {
	w.ui.print("\nSandboxed workspaces\nAgent commands run in isolated Linux; Pi, your editor and tmux stay on the host.\nMounted workspace files remain writable. This choice affects new workspaces; repository overrides still apply.\n")
	if w.system.platform() != "darwin" {
		w.ui.print("Managed SBX workspaces currently require macOS. Linux/WSL setup leaves sandbox settings unchanged.\n")
		if cfg.SBX.Enabled != nil && *cfg.SBX.Enabled {
			return nil, fmt.Errorf("SBX is explicitly enabled on an unsupported platform; edit sbx.enabled before rerunning setup")
		}
		return nil, nil
	}
	initial := true
	if draft.Existing {
		initial = cfg.SBX.WorkspaceEnabled(w.system.platform(), func(name string) error {
			if w.system.lookPath(name) {
				return nil
			}
			return errors.New("missing")
		})
	}
	enabled, err := w.ui.confirm(sandboxQuestion, initial)
	if err != nil {
		return nil, err
	}
	cfg.SBX.Enabled = &enabled
	if !enabled {
		w.sandboxSummary = "New workspaces: host execution (SBX disabled; repository overrides still apply)."
		return nil, nil
	}
	original := cfg.SBX
	var plan *sandboxSetupPlan
	if !hasSandboxRecipe(draft) {
		plan, err = planSandboxFiles(path, &cfg.SBX)
		if err != nil {
			return nil, err
		}
		w.ui.print("Starter kit: %s\nAn empty, read-only startup.d is wired to sandbox-startup run. Existing files are never overwritten.\n", plan.root)
	} else {
		w.ui.print("Keeping your existing sandbox kit, environment file, mounts and readiness settings.\n")
	}
	w.ui.print("Credentials and customization: %s\n", sandboxGuide)
	for {
		err = w.prepareSandbox(ctx, cfg.SBX, plan)
		if err == nil {
			w.sandboxSummary = "New workspaces: SBX enabled; execution and file sharing verified. Kit: " + cfg.SBX.Kit.Name + " (repository overrides still apply)."
			return plan, nil
		}
		w.ui.print("! %s\n", err)
		var cleanupErr *sandboxCleanupError
		if errors.As(err, &cleanupErr) {
			return nil, err
		} // Never leave an unreported probe behind and retry.
		if ctx.Err() != nil || errors.Is(err, ErrAborted) {
			return nil, ErrAborted
		}
		retry, e := w.ui.confirm("Retry SBX preparation after fixing the problem?", true)
		if e != nil {
			return nil, e
		}
		if retry {
			continue
		}
		disable, e := w.ui.confirm("Continue setup with SBX disabled for new workspaces?", false)
		if e != nil {
			return nil, e
		}
		if !disable {
			return nil, ErrAborted
		}
		no := false
		original.Enabled = &no
		cfg.SBX = original
		w.sandboxSummary = "New workspaces: host execution (SBX explicitly disabled after preparation failed)."
		return nil, nil
	}
}

func (w *wizard) sbxOutput(ctx context.Context, timeout time.Duration, args ...string) (string, error) {
	bounded, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result, err := w.system.sandbox(bounded, args...)
	if bounded.Err() != nil {
		return "", bounded.Err()
	}
	return result, err
}

func (w *wizard) prepareSandbox(ctx context.Context, cfg config.SBXConfig, plan *sandboxSetupPlan) error {
	if !w.system.lookPath("sbx") {
		return fmt.Errorf("SBX is missing. Install Docker Sandboxes for macOS from https://docs.docker.com/ai/sandboxes/ in another terminal, then retry (SBX 0.48.0+ required)")
	}
	version, err := w.sbxOutput(ctx, 15*time.Second, "version")
	if err != nil || !versionAtLeast(version, [3]int{0, 48, 0}) {
		return fmt.Errorf("SBX 0.48.0+ is required; check `sbx version`, update Docker Sandboxes, then retry")
	}
	w.ui.print("✓ SBX version supported\nChecking SBX authentication and runtime access…\n")
	output, err := w.sbxOutput(ctx, 15*time.Second, "ls", "--json")
	if err != nil {
		if !auth.IsRequired(output) {
			return fmt.Errorf("SBX runtime is unavailable; check `sbx ls --json` on the host and retry")
		}
		yes, e := w.ui.confirm("SBX is not signed in. Run `sbx login`?", true)
		if e != nil {
			return e
		}
		if !yes {
			return fmt.Errorf("SBX login is required; run `sbx login` on the host, then retry")
		}
		loginCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		err = w.system.run(loginCtx, "sbx", "login")
		cancel()
		if err != nil {
			return fmt.Errorf("SBX login failed or timed out; run `sbx login` on the host, then retry")
		}
		w.completed = append(w.completed, "SBX login")
		output, err = w.sbxOutput(ctx, 15*time.Second, "ls", "--json")
	}
	var listed sandboxSetupList
	if err != nil || json.Unmarshal([]byte(output), &listed) != nil || listed == nil {
		return fmt.Errorf("SBX authentication/runtime check failed; check `sbx ls --json` on the host and retry")
	}
	w.ui.print("✓ SBX authenticated and runtime accessible\n")
	if err := w.extensions(ctx, dependency{"pi-sbx", "pi-sbx 0.6.4+ supports SBX 0.48.0 and gates early tools on startup readiness"}); err != nil {
		// Declining an optional extension is a preparation failure, not a whole-wizard abort.
		if errors.Is(err, errExtensionDeclined) {
			return fmt.Errorf("pi-sbx installation was not completed; install/enable pi-sbx 0.6.4+ or continue without SBX")
		}
		return err
	}
	w.ui.print("Preparation creates and removes one temporary sandbox, may download images, and runs the configured kit/startup commands.\nExisting configured mounts and credentials apply; no network policy is changed. Downloads and startup-script side effects are not undone by cancelling setup.\n")
	yes, err := w.ui.confirm("Prepare and test the sandbox environment now?", true)
	if err != nil {
		return err
	}
	if !yes {
		return fmt.Errorf("sandbox preparation was declined; readiness has not been verified")
	}
	return w.probeSandbox(ctx, cfg, plan)
}

type sandboxSetupList []struct {
	Name string `json:"name"`
}

type sandboxCleanupError struct{ name, temp string }

func (e *sandboxCleanupError) Error() string {
	return fmt.Sprintf("temporary sandbox cleanup could not be confirmed; run `sbx rm --force %s` on the host, then remove temporary directory %s and rerun setup; no Radar configuration was saved", e.name, e.temp)
}

func (w *wizard) probeSandbox(ctx context.Context, cfg config.SBXConfig, plan *sandboxSetupPlan) (result error) {
	temp, err := os.MkdirTemp("", "radar-sbx-setup-")
	if err != nil {
		return err
	}
	keepTemp := false
	defer func() {
		if !keepTemp {
			_ = os.RemoveAll(temp)
		}
	}()
	cfg, err = plan.stage(temp, cfg)
	if err != nil {
		return err
	}
	anchor := filepath.Join(temp, "workspace")
	if err := os.Mkdir(anchor, 0700); err != nil {
		return err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	token := hex.EncodeToString(random[:])
	name := "radar-setup-" + token
	if err := os.WriteFile(filepath.Join(anchor, "host-check"), []byte(token), 0600); err != nil {
		return err
	}
	args := []string{"create", "--name", name}
	if cfg.Kit.Path != "" {
		path, e := expandPath(cfg.Kit.Path)
		if e != nil {
			return e
		}
		args = append(args, "--kit", path)
	}
	if cfg.EnvFile != "" {
		path, e := expandPath(cfg.EnvFile)
		if e != nil {
			return e
		}
		info, e := os.Stat(path)
		if e != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("configured sbx.env_file must be a readable regular file; contents are not displayed")
		}
		args = append(args, "--env-file", path)
	}
	kit := cfg.Kit.Name
	if strings.HasPrefix(kit, "~/") {
		kit, err = expandPath(kit)
		if err != nil {
			return err
		}
	}
	if filepath.IsAbs(kit) {
		w.ui.print("Validating local sandbox kit…\n")
		if _, err := w.sbxOutput(ctx, 15*time.Second, "kit", "validate", kit); err != nil {
			return fmt.Errorf("local sandbox kit validation failed; inspect it with `sbx kit validate` on the host (diagnostics withheld)")
		}
	}
	args = append(args, kit, anchor)
	for _, mount := range cfg.AdditionalMounts {
		suffix := ""
		if strings.HasSuffix(mount, ":ro") {
			mount = strings.TrimSuffix(mount, ":ro")
			suffix = ":ro"
		}
		path, e := expandPath(mount)
		if e != nil {
			return e
		}
		info, e := os.Stat(path)
		if e != nil || !info.IsDir() {
			return fmt.Errorf("configured sandbox mount must be an existing directory for preparation: %s", path)
		}
		args = append(args, path+suffix)
	}
	// A create can fail after provisioning. Always clean this uniquely named probe,
	// using a fresh bounded context even if the user cancelled the foreground work.
	defer func() {
		w.ui.print("Removing temporary sandbox…\n")
		_, cleanupErr := w.sbxOutput(context.Background(), 30*time.Second, "rm", "--force", name)
		if cleanupErr != nil {
			output, listErr := w.sbxOutput(context.Background(), 15*time.Second, "ls", "--json")
			var entries sandboxSetupList
			absent := listErr == nil && json.Unmarshal([]byte(output), &entries) == nil && entries != nil
			for _, entry := range entries {
				if entry.Name == name {
					absent = false
				}
			}
			if !absent {
				keepTemp = true
				result = &sandboxCleanupError{name: name, temp: temp}
			}
		}
	}()
	w.ui.print("Preparing sandbox image — first download/start may take several minutes (up to 5 minutes)…\n")
	if _, err := w.sbxOutput(ctx, 5*time.Minute, args...); err != nil {
		return fmt.Errorf("sandbox creation failed or timed out; check the kit/image, env-file, SBX runtime and network policy; provider diagnostics withheld to protect credentials")
	}
	if len(cfg.ReadyCommand) > 0 {
		w.ui.print("Running sandbox readiness command (up to 60 seconds)…\n")
		ready := append([]string{"exec", "--workdir", anchor, name}, cfg.ReadyCommand...)
		if _, err := w.sbxOutput(ctx, time.Minute, ready...); err != nil {
			return fmt.Errorf("sandbox startup failed or exceeded 60 seconds; inspect your startup scripts/readiness command; hook output is not displayed")
		}
	}
	w.ui.print("Checking sandbox tools and shared file read/write…\n")
	script := `set -eu; for tool in bash node rg fd file; do command -v "$tool" >/dev/null; done; bash -c true; node -e ""; rg --version >/dev/null; fd --version >/dev/null; file --version >/dev/null; test "$(cat "$1/host-check")" = "$2"; printf %s "$2" > "$1/sandbox-check"`
	if _, err := w.sbxOutput(ctx, 30*time.Second, "exec", "--workdir", anchor, name, "sh", "-c", script, "radar-check", anchor, token); err != nil {
		return fmt.Errorf("sandbox execution/file-sharing check failed; custom images must provide Bash, Node.js, rg, fd and file")
	}
	data, err := os.ReadFile(filepath.Join(anchor, "sandbox-check"))
	if err != nil || string(data) != token {
		return fmt.Errorf("sandbox file-sharing check failed: host could not read the sandbox's response")
	}
	w.completed = append(w.completed, "sandbox preparation (downloaded images remain cached; startup side effects are not rolled back)")
	w.ui.print("✓ Sandbox execution and file sharing verified\n")
	return nil
}
