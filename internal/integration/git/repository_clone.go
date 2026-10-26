package git

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"radar/internal/command"
	"radar/internal/config"
	"radar/internal/integration"
	"radar/internal/integration/github/identity"
	"radar/internal/integration/workspace"
)

func (Source) PreviewRepositoryClone(ctx context.Context, req integration.RepositoryCloneRequest) (integration.RepositoryClonePlan, error) {
	return previewRepositoryClone(ctx, cloneRunner{}, req)
}

func (Source) ApplyRepositoryClone(ctx context.Context, req integration.RepositoryCloneRequest, planID string) (integration.RepositoryCloneResult, error) {
	return applyRepositoryClone(ctx, cloneRunner{}, req, planID)
}

func previewRepositoryClone(ctx context.Context, runner workspace.Runner, req integration.RepositoryCloneRequest) (integration.RepositoryClonePlan, error) {
	var plan integration.RepositoryClonePlan
	remote, want, err := identity.CloneRemote(req.Repository)
	if err != nil {
		return plan, err
	}
	cfg, err := config.Load()
	if err != nil {
		return plan, err
	}
	_, registered, found, err := workspace.RegisteredWorkspace(req.Workspace, workspace.ExpandPath(cfg.Workspace.RootDir))
	if err != nil {
		return plan, err
	}
	if !found {
		return plan, fmt.Errorf("repository cloning requires a registered Radar workspace")
	}
	if !filepath.IsAbs(req.Destination) || req.Destination != filepath.Clean(req.Destination) || strings.EqualFold(filepath.Base(req.Destination), ".git") || strings.ContainsAny(req.Destination, "\x00\r\n\t") {
		return plan, fmt.Errorf("destination must be an absolute, clean direct-child path under repository_dirs")
	}
	roots := workspace.CanonicalRepositoryDirs(cfg)
	root := ""
	for _, candidate := range roots {
		if filepath.Dir(req.Destination) == candidate {
			root = candidate
			break
		}
	}
	if root == "" {
		return plan, fmt.Errorf("destination must be a direct child of an existing configured repository_dirs root")
	}
	managedRoot, err := filepath.Abs(workspace.ExpandPath(cfg.Workspace.RootDir))
	if err != nil {
		return plan, err
	}
	if canonical, e := filepath.EvalSymlinks(managedRoot); e == nil {
		managedRoot = canonical
	}
	if cloneWithin(req.Destination, managedRoot) || req.Destination == registered.Path {
		return plan, fmt.Errorf("source repositories cannot be cloned into managed workspace directories")
	}
	parent, err := os.Stat(root)
	if err != nil {
		return plan, fmt.Errorf("source repository root is unavailable")
	}
	stat, ok := parent.Sys().(*syscall.Stat_t)
	if !ok {
		return plan, fmt.Errorf("source repository root identity unavailable")
	}
	plan = integration.RepositoryClonePlan{WorkspaceID: registered.ID, Remote: remote, Identity: want, RepositoryDir: root,
		RootIdentity: fmt.Sprintf("%d:%d", stat.Dev, stat.Ino), Destination: req.Destination, Repository: req.Destination, Status: "clone", RequiresConfirmation: true}

	destinationExists := false
	if _, err := os.Lstat(req.Destination); err == nil {
		destinationExists = true
		if err := verifyCloneCheckout(ctx, runner, req.Destination, want); err != nil {
			return integration.RepositoryClonePlan{}, err
		}
	} else if !os.IsNotExist(err) {
		return integration.RepositoryClonePlan{}, fmt.Errorf("cannot inspect clone destination")
	}

	repos, err := workspace.DiscoverRepos(ctx, runner, req.Workspace)
	if err != nil {
		return integration.RepositoryClonePlan{}, err
	}
	if destinationExists {
		repos = append(repos, req.Destination)
	}
	matches := map[string]bool{}
	for _, repo := range repos {
		canonical, e := filepath.EvalSymlinks(repo)
		if e != nil {
			continue
		}
		canonical, e = filepath.Abs(canonical)
		if e != nil || canonical != filepath.Clean(repo) || cloneWithin(canonical, managedRoot) {
			continue
		}
		allowed := false
		for _, root := range roots {
			if canonical != root && cloneWithin(canonical, root) {
				allowed = true
				break
			}
		}
		if !allowed {
			continue
		}
		origin, e := runner.Run(ctx, canonical, "git", "config", "--get-all", "remote.origin.url")
		if e != nil {
			continue
		}
		_, actual, e := identity.CloneRemote(strings.TrimSuffix(origin, "\n"))
		if e != nil || actual != want {
			continue
		}
		if e := verifyCloneCheckout(ctx, runner, canonical, want); e != nil {
			return integration.RepositoryClonePlan{}, e
		}
		matches[canonical] = true
	}
	if len(matches) > 1 {
		return integration.RepositoryClonePlan{}, fmt.Errorf("multiple matching source checkouts; resolve the ambiguity before cloning")
	}
	for existing := range matches {
		plan.Status = "reused"
		plan.Repository = existing
		plan.RequiresConfirmation = false
	}
	data, _ := json.Marshal(plan)
	digest := sha256.Sum256(data)
	plan.PlanID = hex.EncodeToString(digest[:])
	return plan, nil
}

func verifyCloneCheckout(ctx context.Context, runner workspace.Runner, path, want string) error {
	conflict := func() error {
		return fmt.Errorf("destination conflicts with an existing file, directory, or nonmatching source checkout: %s", path)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return conflict()
	}
	gitDir, err := os.Lstat(filepath.Join(path, ".git"))
	if err != nil || !gitDir.IsDir() || gitDir.Mode()&os.ModeSymlink != 0 {
		return conflict()
	}
	top, err := runner.Run(ctx, path, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return conflict()
	}
	canonical, err := filepath.EvalSymlinks(strings.TrimSpace(top))
	if err != nil || canonical != path {
		return conflict()
	}
	bare, err := runner.Run(ctx, path, "git", "rev-parse", "--is-bare-repository")
	if err != nil || strings.TrimSpace(bare) != "false" {
		return conflict()
	}
	origin, err := runner.Run(ctx, path, "git", "config", "--get-all", "remote.origin.url")
	if err != nil {
		return conflict()
	}
	_, actual, err := identity.CloneRemote(strings.TrimSuffix(origin, "\n"))
	if err != nil || actual != want {
		return conflict()
	}
	return nil
}

func applyRepositoryClone(ctx context.Context, runner workspace.Runner, req integration.RepositoryCloneRequest, expected string) (integration.RepositoryCloneResult, error) {
	plan, err := previewRepositoryClone(ctx, runner, req)
	if err != nil {
		return integration.RepositoryCloneResult{}, err
	}
	if expected == "" || plan.PlanID != expected {
		return integration.RepositoryCloneResult{}, fmt.Errorf("clone plan changed or confirmation missing; preview and confirm again")
	}
	if err := ctx.Err(); err != nil {
		return integration.RepositoryCloneResult{}, err
	}
	result := integration.RepositoryCloneResult{OK: true, Status: plan.Status, Repository: plan.Repository, Remote: plan.Remote}
	if !plan.RequiresConfirmation {
		return result, nil
	}

	// Recheck the approved root immediately before reserving the destination.
	if identity, err := cloneRootIdentity(plan.RepositoryDir); err != nil || identity != plan.RootIdentity {
		return integration.RepositoryCloneResult{}, fmt.Errorf("source root changed; preview and confirm again")
	}
	// Exclusive reservation: never clone into someone else's existing directory.
	if err := os.Mkdir(plan.Destination, 0o755); err != nil {
		return integration.RepositoryCloneResult{}, fmt.Errorf("clone destination changed; preview again (nothing overwritten)")
	}
	reserved, err := os.Lstat(plan.Destination)
	if err != nil {
		return integration.RepositoryCloneResult{}, fmt.Errorf("clone destination reservation unavailable")
	}
	parent, err := os.Stat(plan.RepositoryDir)
	canonical, e := filepath.EvalSymlinks(plan.RepositoryDir)
	current, ce := os.Lstat(plan.Destination)
	if err != nil || e != nil || canonical != plan.RepositoryDir || ce != nil || !os.SameFile(reserved, current) ||
		!current.IsDir() || current.Mode()&os.ModeSymlink != 0 {
		return cloneFailure(result, plan.Destination, "clone destination changed; inspect the reserved directory before retrying"), nil
	}
	stat := parent.Sys().(*syscall.Stat_t)
	if fmt.Sprintf("%d:%d", stat.Dev, stat.Ino) != plan.RootIdentity {
		return cloneFailure(result, plan.Destination, "source root changed; inspect the reserved directory before retrying"), nil
	}
	bounded, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	_, err = runner.Run(bounded, plan.RepositoryDir, "git", "-c", "core.hooksPath=/dev/null", "clone", "--", plan.Remote, plan.Destination)
	if err != nil {
		message := "host Git clone failed; verify host authentication and inspect the partial destination before retrying"
		if bounded.Err() != nil {
			message = "host Git clone cancelled or timed out; inspect the partial destination before retrying"
		}
		return cloneFailure(result, plan.Destination, message), nil
	}
	if err := verifyCloneCheckout(ctx, runner, plan.Destination, plan.Identity); err != nil {
		return cloneFailure(result, plan.Destination, "clone completed but checkout verification failed; inspect the destination"), nil
	}
	result.Status = "cloned"
	return result, nil
}

func cloneFailure(result integration.RepositoryCloneResult, path, message string) integration.RepositoryCloneResult {
	result.OK = false
	result.Status = "failed"
	result.Error = message
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		result.PartialPath = path
	}
	return result
}

func cloneRootIdentity(root string) (string, error) {
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil || canonical != root {
		return "", fmt.Errorf("source root changed")
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("source root unavailable")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("source root identity unavailable")
	}
	return fmt.Sprintf("%d:%d", stat.Dev, stat.Ino), nil
}

func cloneWithin(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// The existing workspace runner handles discovery. Git uses the same cancellable
// command constructor, but discards stderr and caps stdout: helper failures may
// contain credentials and must not enter tool results or logs.
type cloneRunner struct{}

func (cloneRunner) LookPath(name string) error { return (workspace.ExecRunner{}).LookPath(name) }
func (cloneRunner) Run(ctx context.Context, cwd, name string, args ...string) (string, error) {
	if name != "git" {
		return (workspace.ExecRunner{}).Run(ctx, cwd, name, args...)
	}
	cmd := command.CommandContext(ctx, name, args...)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never")
	output := &cloneOutput{}
	cmd.Stdout = output
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("host Git command failed")
	}
	if output.truncated {
		return "", fmt.Errorf("host Git output exceeded the inspection limit")
	}
	return strings.TrimSuffix(output.value.String(), "\n"), nil
}

type cloneOutput struct {
	value     strings.Builder
	truncated bool
}

func (b *cloneOutput) Write(p []byte) (int, error) {
	const limit = 64 * 1024
	n := len(p)
	keep := min(n, limit-b.value.Len())
	if keep > 0 {
		_, _ = b.value.Write(p[:keep])
	}
	if keep < n {
		b.truncated = true
	}
	return n, nil
}

var _ integration.RepositoryCloneProvider = Source{}
