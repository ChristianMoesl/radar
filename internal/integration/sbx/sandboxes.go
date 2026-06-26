package sbx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"radar/internal/linking"
	"radar/internal/protocol"
)

type listResponse struct {
	Sandboxes []sandbox `json:"sandboxes"`
}

type sandbox struct {
	Name       string   `json:"name"`
	ID         string   `json:"id"`
	Agent      string   `json:"agent"`
	Status     string   `json:"status"`
	Workspaces []string `json:"workspaces"`
}

func SourceStatus(ctx context.Context) protocol.SourceStatus {
	status := protocol.SourceStatus{Name: "sbx", Status: "ok"}
	if _, err := exec.LookPath("sbx"); err != nil {
		status.Status = "disabled"
		status.Detail = "sbx not found"
		return status
	}
	output, err := sbxOutput(ctx, "ls", "--json")
	if err != nil {
		status.Status = "error"
		status.Detail = sbxErrorDetail(err)
		return status
	}
	sandboxes, err := parseSandboxes(output)
	if err != nil {
		status.Status = "error"
		status.Detail = err.Error()
		return status
	}
	status.Detail = fmt.Sprintf("%d sandboxes", len(sandboxes))
	return status
}

func FetchSandboxes(ctx context.Context, logger *slog.Logger) ([]protocol.SourceRef, protocol.SourceStatus) {
	status := protocol.SourceStatus{Name: "sbx", Status: "ok"}
	output, err := sbxOutput(ctx, "ls", "--json")
	if err != nil {
		status.Status = "error"
		status.Detail = sbxErrorDetail(err)
		return nil, status
	}

	sandboxes, err := parseSandboxes(output)
	if err != nil {
		status.Status = "error"
		status.Detail = err.Error()
		return nil, status
	}

	sourceRefs := make([]protocol.SourceRef, 0, len(sandboxes))
	for _, s := range sandboxes {
		if ref := s.SourceRef(); ref.ID != "" {
			sourceRefs = append(sourceRefs, ref)
		}
	}

	logger.Debug("collected sbx sandboxes", "count", len(sourceRefs))
	status.Detail = fmt.Sprintf("%d sandboxes", len(sourceRefs))
	return sourceRefs, status
}

func parseSandboxes(output string) ([]sandbox, error) {
	var response listResponse
	if err := json.Unmarshal([]byte(output), &response); err != nil {
		return nil, fmt.Errorf("unexpected sbx ls output: %w", err)
	}
	return response.Sandboxes, nil
}

func (s sandbox) SourceRef() protocol.SourceRef {
	name := strings.TrimSpace(s.Name)
	id := strings.TrimSpace(s.ID)
	if name == "" && id == "" {
		return protocol.SourceRef{}
	}

	title := name
	if title == "" {
		title = id
	}

	refID := "sbx:sandbox:" + name
	if name == "" {
		refID = "sbx:sandbox:" + id
	}

	workspace := primarySandboxWorkspace(s.Workspaces)
	canonicalKey := linking.WorkspaceKey(workspace)
	if canonicalKey == "" {
		canonicalKey = refID
	}

	metadata := map[string]string{
		"id":              id,
		"name":            name,
		"agent":           strings.TrimSpace(s.Agent),
		"workspace_count": strconv.Itoa(len(s.Workspaces)),
	}

	return protocol.SourceRef{
		ID:           refID,
		Source:       "sbx",
		SourceLabel:  "Docker sbx",
		Kind:         "sandbox",
		Title:        title,
		Path:         workspace,
		Status:       strings.TrimSpace(s.Status),
		CanonicalKey: canonicalKey,
		LinkingKeys:  linking.Keys(append(linking.TicketKeys(name, workspace), linking.WorkspaceKey(workspace))...),
		Metadata:     metadata,
	}
}

func primarySandboxWorkspace(workspaces []string) string {
	for _, workspace := range workspaces {
		workspace = linking.CleanPath(workspace)
		if workspace == "" {
			continue
		}
		if isPiAgentMount(workspace) {
			continue
		}
		return workspace
	}
	return ""
}

func isPiAgentMount(path string) bool {
	path = filepath.ToSlash(path)
	return strings.HasSuffix(path, "/.pi/agent") || strings.Contains(path, "/.pi/agent/")
}

func sbxOutput(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sbx", args...)
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("sbx %s failed: %s", strings.Join(args, " "), strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", err
	}
	return string(output), nil
}

func sbxErrorDetail(err error) string {
	if errors.Is(err, exec.ErrNotFound) {
		return "sbx not found"
	}
	return err.Error()
}
