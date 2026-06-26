package git

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"radar/internal/linking"
	"radar/internal/protocol"
	"radar/internal/workspace"
)

var ticketPattern = regexp.MustCompile(`[A-Z][A-Z0-9]+-[0-9]+`)

func FetchWorktrees(ctx context.Context, logger *slog.Logger) ([]protocol.SourceRef, protocol.SourceStatus) {
	roots := gitRoots()
	source_refs := make([]protocol.SourceRef, 0)
	seen := map[string]bool{}
	status := protocol.SourceStatus{Name: "git", Status: "ok"}
	collectedRoots := 0
	failedRoots := 0

	if len(roots) == 0 {
		status.Status = "disabled"
		status.Detail = "no git roots configured"
		return source_refs, status
	}

	for _, root := range roots {
		worktrees, err := worktrees(ctx, root)
		if err != nil {
			failedRoots++
			logger.Debug("git worktree collection skipped", "root", root, "error", err)
			continue
		}
		collectedRoots++
		for _, wt := range worktrees {
			if seen[wt.Path] {
				continue
			}
			seen[wt.Path] = true
			source_refs = append(source_refs, wt.SourceRef(ctx))
		}
	}

	logger.Debug("collected git worktrees", "count", len(source_refs))
	if collectedRoots == 0 {
		status.Status = "error"
		status.Detail = "could not inspect any configured git roots"
		return source_refs, status
	}

	status.Detail = fmt.Sprintf("%d worktrees from %d roots", len(source_refs), collectedRoots)
	if failedRoots > 0 {
		status.Detail = fmt.Sprintf("%s, %d skipped", status.Detail, failedRoots)
	}
	return source_refs, status
}

func gitRoots() []string {
	if value := os.Getenv("RADAR_GIT_REPOS"); value != "" {
		parts := strings.Split(value, ":")
		roots := make([]string, 0, len(parts))
		for _, part := range parts {
			if part != "" {
				roots = append(roots, expandPath(part))
			}
		}
		return roots
	}

	roots := make([]string, 0)
	if cwd, err := os.Getwd(); err == nil {
		roots = appendUniqueRoot(roots, cwd)
	}
	for _, root := range workspaceGitRoots() {
		roots = appendUniqueRoot(roots, root)
	}
	for _, root := range tmuxSessionGitRoots() {
		roots = appendUniqueRoot(roots, root)
	}
	return roots
}

func workspaceGitRoots() []string {
	root, err := workspace.DefaultRoot()
	if err != nil || root == "" {
		return nil
	}
	matches, err := filepath.Glob(filepath.Join(root, "*", "*"))
	if err != nil {
		return nil
	}
	roots := make([]string, 0, len(matches))
	for _, match := range matches {
		info, err := os.Stat(match)
		if err == nil && info.IsDir() {
			roots = append(roots, match)
		}
	}
	return roots
}

func tmuxSessionGitRoots() []string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "tmux", "list-sessions", "-F", "#{session_path}").Output()
	if err != nil {
		return nil
	}
	roots := make([]string, 0)
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		root := strings.TrimSpace(scanner.Text())
		if root != "" {
			roots = appendUniqueRoot(roots, root)
		}
	}
	return roots
}

func appendUniqueRoot(roots []string, root string) []string {
	root = cleanRoot(root)
	if root == "" {
		return roots
	}
	for _, existing := range roots {
		if existing == root {
			return roots
		}
	}
	return append(roots, root)
}

func cleanRoot(root string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		return ""
	}
	if abs, err := filepath.Abs(root); err == nil {
		return abs
	}
	return root
}

func expandPath(path string) string {
	if path == "~" {
		home, _ := os.UserHomeDir()
		return home
	}
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[2:])
	}
	return path
}

type worktree struct {
	Path     string
	Branch   string
	Head     string
	Prunable bool
}

func worktrees(ctx context.Context, root string) ([]worktree, error) {
	output, err := gitOutput(ctx, root, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}

	items := make([]worktree, 0)
	var current worktree
	scanner := bufio.NewScanner(strings.NewReader(output))
	flush := func() {
		if current.Path != "" && !current.Prunable {
			items = append(items, current)
		}
		current = worktree{}
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			flush()
			continue
		}
		key, value, _ := strings.Cut(line, " ")
		switch key {
		case "worktree":
			current.Path = value
		case "HEAD":
			current.Head = value
		case "branch":
			current.Branch = strings.TrimPrefix(value, "refs/heads/")
		case "prunable":
			current.Prunable = true
		}
	}
	flush()
	return items, scanner.Err()
}

func (w worktree) SourceRef(ctx context.Context) protocol.SourceRef {
	status := worktreeStatus(ctx, w.Path)
	title := w.Branch
	if title == "" {
		title = filepath.Base(w.Path)
	}

	originRepo := worktreeOriginRepo(ctx, w.Path)
	metadata := map[string]string{
		"head": w.Head,
	}
	if ticket := ticketPattern.FindString(w.Branch); ticket != "" {
		metadata["ticket"] = ticket
	}
	if status.DirtyFiles > 0 {
		metadata["dirty_files"] = fmt.Sprintf("%d", status.DirtyFiles)
	}
	if status.Ahead != "" {
		metadata["ahead"] = status.Ahead
	}
	if status.Behind != "" {
		metadata["behind"] = status.Behind
	}

	canonicalKey := linking.WorkspaceKey(w.Path)
	linkingKeys := linking.Keys(append(linking.TicketKeys(w.Branch, w.Path, originRepo), canonicalKey, linking.BranchKey(originRepo, gitBranchKey(w.Branch)))...)

	return protocol.SourceRef{
		ID:           "git:worktree:" + w.Path,
		Source:       "git",
		SourceLabel:  "git",
		Kind:         "worktree",
		Title:        title,
		Repo:         originRepo,
		Path:         w.Path,
		Branch:       w.Branch,
		Status:       status.Label(),
		CanonicalKey: canonicalKey,
		LinkingKeys:  linkingKeys,
		Metadata:     metadata,
	}
}

type status struct {
	DirtyFiles int
	Ahead      string
	Behind     string
}

func (s status) Label() string {
	parts := make([]string, 0, 3)
	if s.DirtyFiles > 0 {
		parts = append(parts, fmt.Sprintf("%d dirty", s.DirtyFiles))
	}
	if s.Ahead != "" {
		parts = append(parts, "ahead "+s.Ahead)
	}
	if s.Behind != "" {
		parts = append(parts, "behind "+s.Behind)
	}
	if len(parts) == 0 {
		return "clean"
	}
	return strings.Join(parts, ", ")
}

func worktreeOriginRepo(ctx context.Context, path string) string {
	output, err := gitOutput(ctx, path, "remote", "get-url", "origin")
	if err != nil {
		return ""
	}
	return normalizeGitHubRepo(output)
}

func gitBranchKey(branch string) string {
	branch = strings.TrimSpace(branch)
	branch = strings.TrimPrefix(branch, "refs/remotes/")
	branch = strings.TrimPrefix(branch, "origin/")
	branch = strings.TrimPrefix(branch, "refs/heads/")
	return strings.ReplaceAll(branch, "/", "-")
}

func normalizeGitHubRepo(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimSuffix(value, ".git")
	value = strings.TrimPrefix(value, "https://github.com/")
	value = strings.TrimPrefix(value, "http://github.com/")
	value = strings.TrimPrefix(value, "git@github.com:")
	if strings.Contains(value, "://") || strings.Contains(value, "@") {
		return ""
	}
	return value
}

func worktreeStatus(ctx context.Context, path string) status {
	output, err := gitOutput(ctx, path, "status", "--porcelain=v2", "--branch")
	if err != nil {
		return status{}
	}
	var s status
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "# branch.ab ") {
			fields := strings.Fields(line)
			if len(fields) >= 4 {
				s.Ahead = strings.TrimPrefix(fields[2], "+")
				s.Behind = strings.TrimPrefix(fields[3], "-")
			}
			continue
		}
		if line != "" && !strings.HasPrefix(line, "#") {
			s.DirtyFiles++
		}
	}
	return s
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("git %s failed: %s", strings.Join(args, " "), strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", err
	}
	return string(output), nil
}
