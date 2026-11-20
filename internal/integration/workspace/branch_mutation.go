package workspace

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"radar/internal/workspacelease"
)

// Resolve through Git so a linked worktree and its primary repository use the
// same lease. No lexical working-directory fallback may authorize an effect.
func acquireWorktreeBranchMutation(ctx context.Context, runner Runner, repo, branch string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	common, err := runner.Run(ctx, repo, "git", "rev-parse", "--path-format=absolute", "--git-common-dir")
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, fmt.Errorf("inspect Git branch mutation identity: %w", err)
	}
	common = strings.TrimSpace(common)
	if !filepath.IsAbs(common) {
		return nil, fmt.Errorf("Git common directory must be an absolute path for branch mutation")
	}
	release, err := workspacelease.AcquireGitBranchMutation(common, branch)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		release()
		return nil, err
	}
	return release, nil
}
