// Package client owns SBX executable selection and the Windows/WSL path boundary.
// All persisted workspace paths remain host-native. Commands inside a sandbox
// are never rewritten.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"radar/internal/command"
)

type Runner interface {
	LookPath(string) error
	Run(context.Context, string, string, ...string) (string, error)
}

type Client struct {
	runner     Runner
	goos       string
	wsl        bool
	executable string
	distro     string
}

func New(runner Runner) *Client {
	return &Client{runner: runner, goos: runtime.GOOS, wsl: IsWSL(), distro: os.Getenv("WSL_DISTRO_NAME")}
}

func IsWSL() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	release, err := os.ReadFile("/proc/sys/kernel/osrelease")
	return err == nil && strings.Contains(strings.ToLower(string(release)), "microsoft")
}

// ErrWindowsWorkspace is a real filesystem limitation, not a detection failure.
// SBX v0.43.0 exposes WSL symlinks through virtiofs with readlink/open failing
// EINVAL. Radar requires notes.md and Git worktree paths to work on both sides.
var ErrWindowsWorkspace = errors.New("managed workspace sandboxes are unavailable with Windows SBX: WSL-mounted symlinks (including the required notes.md link) return Invalid argument; SBX detection, shell actions and cleanup are supported")

// RequireManaged rejects the Windows backend before any workspace mutation.
func (c *Client) RequireManaged() error {
	executable, err := c.Executable()
	if err != nil {
		return err
	}
	if executable == "sbx.exe" {
		return ErrWindowsWorkspace
	}
	return nil
}

// SelectExecutable prefers native SBX, matching pi-sbx. A runtime failure must
// never cause a switch to another installation (or to host execution).
func SelectExecutable(goos string, wsl bool, lookup func(string) error) (string, error) {
	if err := lookup("sbx"); err == nil {
		return "sbx", nil
	}
	if goos == "linux" && wsl {
		if err := lookup("sbx.exe"); err == nil {
			if err := lookup("wslpath"); err != nil {
				return "", fmt.Errorf("Windows SBX requires wslpath: %w", err)
			}
			return "sbx.exe", nil
		}
		return "", fmt.Errorf("sbx or sbx.exe not found on WSL PATH")
	}
	return "", fmt.Errorf("sbx not found")
}

func (c *Client) Executable() (string, error) {
	if c.executable == "" {
		name, err := SelectExecutable(c.goos, c.wsl, c.runner.LookPath)
		if err != nil {
			return "", err
		}
		c.executable = name
	}
	return c.executable, nil
}

func (c *Client) LookPath() error { _, err := c.Executable(); return err }

func (c *Client) Run(ctx context.Context, cwd string, args ...string) (string, error) {
	executable, err := c.Executable()
	if err != nil {
		return "", err
	}
	// Windows processes hold their cwd open. Never lock a workspace against
	// removal; the sandbox's independent cwd is supplied via --workdir.
	if executable == "sbx.exe" {
		if len(args) > 0 && args[0] == "create" {
			return "", ErrWindowsWorkspace
		}
		cwd = "/"
	}
	output, err := c.runner.Run(ctx, cwd, executable, args...)
	if err != nil {
		return output, err
	}
	if executable == "sbx.exe" && len(args) == 2 && args[0] == "ls" && args[1] == "--json" {
		return c.normalizeList(ctx, output)
	}
	return output, nil
}

func windowsPath(value string) bool {
	return (len(value) >= 3 && ((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) && value[1] == ':' && (value[2] == '\\' || value[2] == '/')) || strings.HasPrefix(value, `\\`) || strings.HasPrefix(value, "//")
}

func splitReadOnly(value string) (string, string) {
	if strings.HasSuffix(value, ":ro") {
		return strings.TrimSuffix(value, ":ro"), ":ro"
	}
	return value, ""
}

func (c *Client) normalizeList(ctx context.Context, output string) (string, error) {
	var response struct {
		Sandboxes []map[string]json.RawMessage `json:"sandboxes"`
	}
	if err := json.Unmarshal([]byte(output), &response); err != nil {
		return "", fmt.Errorf("unexpected sbx ls output: %w", err)
	}
	cache := map[string]string{}
	local := func(value string) (string, error) {
		host, suffix := splitReadOnly(value)
		if !windowsPath(host) {
			return value, nil
		}
		if converted, ok := cache[host]; ok {
			if converted == "" {
				return "", nil
			}
			return converted + suffix, nil
		}
		// Resources in another distribution can be listed/removed by name,
		// but must never link to this distribution's filesystem.
		unc := strings.Split(strings.ReplaceAll(host, `\`, "/"), "/")
		if len(unc) >= 4 && (strings.EqualFold(unc[2], "wsl.localhost") || strings.EqualFold(unc[2], "wsl$")) && c.distro != "" && !strings.EqualFold(unc[3], c.distro) {
			cache[host] = ""
			return "", nil
		}
		converted, err := c.runner.Run(ctx, "", "wslpath", "-u", host)
		if err != nil {
			return "", fmt.Errorf("translate SBX workspace %q: %w", host, err)
		}
		converted = strings.TrimSpace(converted)
		if !filepath.IsAbs(converted) || strings.HasPrefix(converted, "//") {
			return "", fmt.Errorf("wslpath returned an invalid local path for %q: %q", host, converted)
		}
		cache[host] = converted
		return converted + suffix, nil
	}
	for _, sandbox := range response.Sandboxes {
		for _, field := range []string{"workspaces", "mounts"} {
			raw, ok := sandbox[field]
			if !ok {
				continue
			}
			var paths []string
			if err := json.Unmarshal(raw, &paths); err != nil {
				return "", fmt.Errorf("unexpected sbx %s: %w", field, err)
			}
			normalized := []string{}
			for _, value := range paths {
				value, err := local(value)
				if err != nil {
					return "", err
				}
				if value != "" {
					normalized = append(normalized, value)
				}
			}
			sandbox[field], _ = json.Marshal(normalized)
		}
		if raw, ok := sandbox["kit_path"]; ok {
			var value string
			if err := json.Unmarshal(raw, &value); err != nil {
				return "", err
			}
			value, err := local(value)
			if err != nil {
				return "", err
			}
			sandbox["kit_path"], _ = json.Marshal(value)
		}
	}
	data, err := json.Marshal(response)
	return string(data), err
}

// ExecRunner keeps stderr out of JSON responses, but includes it in errors.
type ExecRunner struct{}

func (ExecRunner) LookPath(name string) error { _, err := exec.LookPath(name); return err }
func (ExecRunner) Run(ctx context.Context, cwd, name string, args ...string) (string, error) {
	cmd := command.CommandContext(ctx, name, args...)
	cmd.Dir = cwd
	output, err := cmd.Output()
	if err == nil {
		return strings.TrimSpace(string(output)), nil
	}
	if ctx.Err() != nil {
		return "", fmt.Errorf("%s %s failed: %w", name, strings.Join(args, " "), ctx.Err())
	}
	detail := err.Error()
	if exit, ok := err.(*exec.ExitError); ok && strings.TrimSpace(string(exit.Stderr)) != "" {
		detail = strings.TrimSpace(string(exit.Stderr))
	}
	return "", fmt.Errorf("%s %s failed: %s", name, strings.Join(args, " "), detail)
}
