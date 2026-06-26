package tmux

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"radar/internal/linking"
	"radar/internal/protocol"
	"radar/internal/workspace"
)

var ticketPattern = regexp.MustCompile(`(?i)[A-Z][A-Z0-9]+-[0-9]+`)

const sessionFormat = "#{session_id}\t#{session_name}\t#{session_attached}\t#{session_windows}\t#{session_path}"

type session struct {
	ID            string
	Name          string
	AttachedCount int
	WindowCount   int
	Path          string
}

func SourceStatus(ctx context.Context) protocol.SourceStatus {
	status := protocol.SourceStatus{Name: "tmux", Status: "ok"}
	if _, err := exec.LookPath("tmux"); err != nil {
		status.Status = "disabled"
		status.Detail = "tmux not found"
		return status
	}
	if _, err := tmuxOutput(ctx, "list-sessions", "-F", sessionFormat); err != nil {
		status.Status = "disabled"
		status.Detail = tmuxErrorDetail(err)
	}
	return status
}

func FetchSessions(ctx context.Context, logger *slog.Logger) ([]protocol.SourceRef, protocol.SourceStatus) {
	status := protocol.SourceStatus{Name: "tmux", Status: "ok"}
	output, err := tmuxOutput(ctx, "list-sessions", "-F", sessionFormat)
	if err != nil {
		status.Status = "error"
		status.Detail = tmuxErrorDetail(err)
		return nil, status
	}

	sessions, err := parseSessions(output)
	if err != nil {
		status.Status = "error"
		status.Detail = err.Error()
		return nil, status
	}

	sourceRefs := make([]protocol.SourceRef, 0, len(sessions))
	for _, s := range sessions {
		sourceRefs = append(sourceRefs, s.SourceRef())
	}

	logger.Debug("collected tmux sessions", "count", len(sourceRefs))
	status.Detail = fmt.Sprintf("%d sessions", len(sourceRefs))
	return sourceRefs, status
}

func parseSessions(output string) ([]session, error) {
	sessions := make([]session, 0)
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 5 {
			return nil, fmt.Errorf("unexpected tmux session output: got %d fields", len(fields))
		}
		attachedCount, err := strconv.Atoi(fields[2])
		if err != nil {
			return nil, fmt.Errorf("unexpected tmux session attached count %q", fields[2])
		}
		windowCount, err := strconv.Atoi(fields[3])
		if err != nil {
			return nil, fmt.Errorf("unexpected tmux session window count %q", fields[3])
		}
		sessions = append(sessions, session{
			ID:            fields[0],
			Name:          fields[1],
			AttachedCount: attachedCount,
			WindowCount:   windowCount,
			Path:          fields[4],
		})
	}
	return sessions, scanner.Err()
}

func (s session) SourceRef() protocol.SourceRef {
	status := "detached"
	if s.AttachedCount > 0 {
		status = "attached"
	}
	metadata := map[string]string{
		"session_id":        s.ID,
		"session":           s.Name,
		"attached_count":    strconv.Itoa(s.AttachedCount),
		"window_count":      strconv.Itoa(s.WindowCount),
		"switch_target":     s.ID,
		"working_directory": s.Path,
		"session_path":      s.Path,
	}
	if ticket := ticketPattern.FindString(s.Name); ticket != "" {
		metadata["ticket"] = strings.ToUpper(ticket)
	}

	return protocol.SourceRef{
		ID:          "tmux:session:" + s.ID,
		Source:      "tmux",
		SourceLabel: "tmux",
		Kind:        "session",
		Title:       s.Name,
		Path:        s.Path,
		Status:      status,
		LinkingKeys: linking.Keys(append(linking.TicketKeys(s.Name, s.Path), linking.WorkspaceKey(s.Path))...),
		Metadata:    metadata,
	}
}

func SessionTarget(task protocol.Task) string {
	if task.Kind == "session" {
		if target := metadataValue(task.Metadata, "switch_target", "session_id", "session"); target != "" {
			return target
		}
	}
	for _, ref := range task.SourceRefs {
		if ref.Source == "tmux" && ref.Kind == "session" {
			if target := metadataValue(ref.Metadata, "switch_target", "session_id", "session"); target != "" {
				return target
			}
			return ref.Title
		}
	}
	return ""
}

func SessionRefMatchesCurrent(ref protocol.SourceRef, current protocol.CurrentContext) bool {
	return metadataMatchesSession(ref.Metadata, current) || ref.Title == current.SessionName
}

func DeleteSession(ctx context.Context, target string) (protocol.DeleteResult, error) {
	deleted, err := workspace.DeleteSession(ctx, workspace.ExecRunner{}, target)
	if err != nil {
		return protocol.DeleteResult{}, err
	}
	return protocol.DeleteResult{Source: "tmux", Kind: "session", Title: deleted.SessionName, SessionName: deleted.SessionName}, nil
}

func metadataMatchesSession(metadata map[string]string, current protocol.CurrentContext) bool {
	for _, key := range []string{"switch_target", "session_id", "session"} {
		value := metadata[key]
		if value != "" && (value == current.SessionID || value == current.SessionName) {
			return true
		}
	}
	return false
}

func metadataValue(metadata map[string]string, keys ...string) string {
	for _, key := range keys {
		if metadata[key] != "" {
			return metadata[key]
		}
	}
	return ""
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
