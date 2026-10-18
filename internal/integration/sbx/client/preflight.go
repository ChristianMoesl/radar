package client

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const CacheEnvironment = "DOCKER_SANDBOXES_ENABLE_VIRTIOFS_CACHE"

func NewForPlatform(runner Runner, goos string, wsl bool) *Client {
	return &Client{runner: runner, goos: goos, wsl: wsl, distro: os.Getenv("WSL_DISTRO_NAME")}
}

// Environment is subprocess-local. It also covers auto-started native daemons;
// it cannot reconfigure a daemon that was already running.
func Environment(executable string, environ []string) []string {
	if filepath.Base(executable) != "sbx" || !IsWSL() {
		return environ
	}
	result := make([]string, 0, len(environ)+1)
	for _, entry := range environ {
		if !strings.HasPrefix(entry, CacheEnvironment+"=") {
			result = append(result, entry)
		}
	}
	return append(result, CacheEnvironment+"=0")
}

func (c *Client) ShellExecutable() (string, error) {
	executable, err := c.Executable()
	if err != nil {
		return "", err
	}
	if c.goos == "linux" && c.wsl && executable == "sbx" {
		return "env " + CacheEnvironment + "=0 sbx", nil
	}
	return executable, nil
}

// Preflight never creates a VM, logs in, or restarts a daemon. Run before
// mutations, including recovery and explicit attachment to an existing anchor.
func (c *Client) Preflight(ctx context.Context) error {
	if err := c.RequireManaged(); err != nil {
		return err
	}
	if c.goos != "darwin" && c.goos != "linux" {
		return fmt.Errorf("managed SBX requires macOS or native Linux with KVM")
	}
	if c.goos == "linux" {
		if err := checkKVM(); err != nil {
			return err
		}
	}
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	version, err := c.Run(bounded, "", "version", "--json")
	if err != nil {
		return fmt.Errorf("check SBX version: %w", err)
	}
	if err := ValidateVersion(version); err != nil {
		return err
	}
	if c.goos == "linux" && c.wsl {
		status, err := c.Run(bounded, "", "daemon", "status", "--json")
		if err != nil {
			return fmt.Errorf("check native WSL daemon cache policy: %w", err)
		}
		if err := validateDaemonCache(status, os.ReadFile); err != nil {
			return err
		}
	}
	return nil
}

func validateDaemonCache(output string, read func(string) ([]byte, error)) error {
	guidance := "native WSL SBX requires " + CacheEnvironment + "=0 in the daemon environment; after inspecting running sandboxes, deliberately stop the native daemon and start it with this variable (Radar never restarts unrelated sandboxes)"
	var state struct {
		Status string `json:"status"`
		Socket string `json:"socket"`
	}
	if err := json.Unmarshal([]byte(output), &state); err != nil {
		return fmt.Errorf("unexpected sbx daemon status: %w", err)
	}
	if state.Status == "stopped" {
		return nil
	} // The next CLI auto-start inherits Environment.
	if state.Status != "running" || !filepath.IsAbs(state.Socket) {
		return fmt.Errorf("cannot establish daemon state: %s", guidance)
	}
	data, err := read(filepath.Join(filepath.Dir(state.Socket), "sandboxd.pid"))
	if err != nil {
		return fmt.Errorf("cannot inspect daemon PID: %s", guidance)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return fmt.Errorf("invalid native SBX daemon PID: %s", guidance)
	}
	environ, err := read(fmt.Sprintf("/proc/%d/environ", pid))
	if err == nil {
		for _, entry := range strings.Split(string(environ), "\x00") {
			if entry == CacheEnvironment+"=0" {
				return nil
			}
		}
	}
	return fmt.Errorf("cannot verify cache opt-out on the running daemon: %s", guidance)
}
