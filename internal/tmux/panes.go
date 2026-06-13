package tmux

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"radar.nvim/internal/protocol"
)

var ticketPattern = regexp.MustCompile(`(?i)[A-Z][A-Z0-9]+-[0-9]+`)

const paneFormat = "#{session_name}\t#{window_index}\t#{window_name}\t#{pane_id}\t#{pane_current_path}\t#{pane_current_command}\t#{pane_title}"

type pane struct {
	SessionName string
	WindowIndex string
	WindowName  string
	PaneID      string
	Path        string
	Command     string
	Title       string
}

func ServiceStatus(ctx context.Context) protocol.ServiceStatus {
	status := protocol.ServiceStatus{Name: "tmux", Status: "ok"}
	if _, err := exec.LookPath("tmux"); err != nil {
		status.Status = "disabled"
		status.Detail = "tmux not found"
		return status
	}
	if _, err := tmuxOutput(ctx, "list-panes", "-a", "-F", paneFormat); err != nil {
		status.Status = "disabled"
		status.Detail = tmuxErrorDetail(err)
	}
	return status
}

func FetchPanes(ctx context.Context, logger *slog.Logger) ([]protocol.SourceRef, protocol.ServiceStatus) {
	status := protocol.ServiceStatus{Name: "tmux", Status: "ok"}
	output, err := tmuxOutput(ctx, "list-panes", "-a", "-F", paneFormat)
	if err != nil {
		status.Status = "error"
		status.Detail = tmuxErrorDetail(err)
		return nil, status
	}

	panes, err := parsePanes(output)
	if err != nil {
		status.Status = "error"
		status.Detail = err.Error()
		return nil, status
	}

	sourceRefs := make([]protocol.SourceRef, 0, len(panes))
	for _, p := range panes {
		if !hasTicket(p) {
			continue
		}
		sourceRefs = append(sourceRefs, p.SourceRef())
	}

	logger.Debug("collected tmux panes", "count", len(sourceRefs))
	status.Detail = fmt.Sprintf("%d ticket panes", len(sourceRefs))
	return sourceRefs, status
}

func parsePanes(output string) ([]pane, error) {
	panes := make([]pane, 0)
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 7 {
			return nil, fmt.Errorf("unexpected tmux pane output: got %d fields", len(fields))
		}
		panes = append(panes, pane{
			SessionName: fields[0],
			WindowIndex: fields[1],
			WindowName:  fields[2],
			PaneID:      fields[3],
			Path:        fields[4],
			Command:     fields[5],
			Title:       fields[6],
		})
	}
	return panes, scanner.Err()
}

func hasTicket(p pane) bool {
	for _, value := range []string{p.SessionName, p.WindowName, p.Path, p.Command, p.Title} {
		if ticketPattern.MatchString(value) {
			return true
		}
	}
	return false
}

func (p pane) SourceRef() protocol.SourceRef {
	metadata := map[string]string{
		"session":      p.SessionName,
		"window_index": p.WindowIndex,
		"window_name":  p.WindowName,
		"pane_id":      p.PaneID,
		"command":      p.Command,
	}
	if p.Title != "" {
		metadata["pane_title"] = p.Title
	}
	if ticket := ticketPattern.FindString(strings.Join([]string{p.SessionName, p.WindowName, p.Path, p.Title}, " ")); ticket != "" {
		metadata["ticket"] = strings.ToUpper(ticket)
	}

	return protocol.SourceRef{
		ID:       "tmux:pane:" + p.PaneID,
		Source:   "tmux",
		Kind:     "pane",
		Title:    p.title(),
		Path:     p.Path,
		Status:   p.Command,
		Metadata: metadata,
	}
}

func (p pane) title() string {
	parts := []string{p.SessionName}
	if p.WindowName != "" {
		parts = append(parts, p.WindowName)
	}
	if p.Command != "" {
		parts = append(parts, p.Command)
	}
	if p.Path != "" {
		parts = append(parts, filepath.Base(p.Path))
	}
	return strings.Join(parts, " · ")
}

func tmuxOutput(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "tmux", args...)
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("tmux %s failed: %s", strings.Join(args, " "), strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", err
	}
	return string(output), nil
}

func tmuxErrorDetail(err error) string {
	if errors.Is(err, exec.ErrNotFound) {
		return "tmux not found"
	}
	detail := err.Error()
	if strings.Contains(detail, "no server running") || strings.Contains(detail, "failed to connect to server") {
		return "no tmux server running"
	}
	return detail
}
