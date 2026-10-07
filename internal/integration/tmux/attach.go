package tmux

import (
	"fmt"
	"os/exec"
	"radar/internal/integration"
	"regexp"
	"strconv"
	"strings"
)

// AttachCommand lets the foreground UI release its terminal before attaching.
// Workspace creation owns starting the session; browsing never starts tmux.
func (Source) AttachCommand(target integration.SessionTarget) (*exec.Cmd, error) {
	name := firstNonEmpty(target.ID, target.Name)
	if name == "" {
		return nil, fmt.Errorf("tmux session target is required")
	}
	return exec.Command("tmux", "attach-session", "-t", name), nil
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
