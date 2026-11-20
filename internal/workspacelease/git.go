package workspacelease

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"radar/internal/socket"
)

// AcquireGitBranchMutation coordinates cooperating worktree writers and branch
// deletion, including the gap after worktree removal but before ref deletion.
// commonDir is Git's actual common directory, not a checkout's working path;
// branch is the exact short local branch name (origin/foo may itself be local).
// This does not replace workspace deletion/reference admission or Git's locks.
func AcquireGitBranchMutation(commonDir, branch string) (func(), error) {
	physical, err := CanonicalPath(commonDir)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(physical)
	if err != nil {
		return nil, fmt.Errorf("inspect Git common directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("Git common directory is not a directory")
	}
	branch = strings.TrimSpace(branch)
	if branch == "" || strings.IndexFunc(branch, unicode.IsControl) >= 0 {
		return nil, fmt.Errorf("Git branch is required and must not contain control characters")
	}
	base, err := socket.Path()
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(physical + "\x00" + branch))
	return acquire(filepath.Join(filepath.Dir(base), "leases"), "git-branch-"+hex.EncodeToString(sum[:])+".lock", true, "git branch mutation", branch)
}
