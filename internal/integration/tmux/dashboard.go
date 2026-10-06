package tmux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"radar/internal/command"
)

// OpenDashboard attaches a real terminal client before opening the popup. A
// detached server is not an active client; testing only has-session would leave
// an ordinary shell outside tmux and make workspace switching impossible.
func (Source) OpenDashboard(ctx context.Context) error {
	if (Source{}).ClientActive() {
		return fmt.Errorf("dashboard client is already inside tmux")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		return fmt.Errorf("tmux 3.2+ is required — install tmux and run `radar` again")
	}
	probe, cancel := context.WithTimeout(ctx, 3*time.Second)
	version, err := command.CommandContext(probe, "tmux", "-V").Output()
	cancel()
	if err != nil || !SupportsPopup(string(version)) {
		return fmt.Errorf("tmux 3.2+ is required for Radar popups — update tmux and try again")
	}
	probe, cancel = context.WithTimeout(ctx, 3*time.Second)
	err = command.CommandContext(probe, "tmux", "has-session").Run()
	cancel()
	running := err == nil
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return fmt.Errorf("inspect tmux sessions: %w", err)
		}
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	args := dashboardArgs(running, executable, cwd)
	// Unlike short probes, the attached client must remain in the terminal's
	// foreground process group, with all streams connected, until it detaches.
	cmd := exec.CommandContext(ctx, "tmux", args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("open Radar in tmux: %w", err)
	}
	return nil
}

func dashboardArgs(running bool, executable, cwd string) []string {
	args := []string{"attach-session"}
	if !running {
		args = []string{"new-session", "-A", "-s", "radar", "-c", cwd}
	}
	return append(args, ";", "display-popup", "-E", "-w", "90%", "-h", "90%", "-d", cwd, shellQuote(executable))
}

func SupportsPopup(version string) bool {
	match := regexp.MustCompile(`^tmux (?:next-)?([0-9]+)\.([0-9]+)`).FindStringSubmatch(strings.TrimSpace(version))
	if match == nil {
		return false
	}
	major, _ := strconv.Atoi(match[1])
	minor, _ := strconv.Atoi(match[2])
	return major > 3 || major == 3 && minor >= 2
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
