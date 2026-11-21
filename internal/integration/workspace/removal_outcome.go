package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Point-in-time observations under the caller's retained branch admission. They
// are not a reservation against external Git/filesystem changes or retry consent.
// Pin the repository and common directory so a replaced repository cannot supply
// absence evidence for the repository whose worktree was removed.
type removalRepository struct {
	repository string
	files      map[string]os.FileInfo
}

func observeRemovalRepository(ctx context.Context, runner Runner, repository string) (removalRepository, error) {
	view := removalRepository{repository: repository, files: map[string]os.FileInfo{}}
	info, err := os.Stat(repository)
	if err != nil {
		return view, err
	}
	view.files[repository] = info
	common, err := runner.Run(ctx, repository, "git", "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return view, err
	}
	common = strings.TrimSpace(common)
	if !filepath.IsAbs(common) {
		return view, fmt.Errorf("managed removal repository identity unavailable")
	}
	info, err = os.Stat(common)
	if err != nil {
		return view, err
	}
	view.files[common] = info
	return view, view.validate(ctx)
}

func (view removalRepository) validate(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for path, expected := range view.files {
		actual, err := os.Stat(path)
		if err != nil {
			return err
		}
		if !actual.IsDir() || !os.SameFile(actual, expected) {
			return fmt.Errorf("managed removal repository changed; inspect before retrying")
		}
	}
	return ctx.Err()
}

func (view removalRepository) worktreeRemoved(ctx context.Context, runner Runner, path string) error {
	if err := view.validate(ctx); err != nil {
		return err
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		if err != nil {
			return err
		}
		return fmt.Errorf("managed worktree path remains or was replaced after removal; inspect before retrying")
	}
	// NUL framing preserves native paths containing newlines, tabs or Git quoting
	// characters. Do not filter prunable records: they still retain registration.
	output, err := runner.Run(ctx, view.repository, "git", "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return fmt.Errorf("managed worktree removal could not be observed: %w", err)
	}
	if err := view.validate(ctx); err != nil {
		return err
	}
	if len(output) > 1024*1024 || !strings.HasSuffix(output, "\x00\x00") {
		return fmt.Errorf("managed worktree registration evidence unavailable")
	}
	seen := map[string]bool{}
	for _, record := range strings.Split(strings.TrimSuffix(output, "\x00\x00"), "\x00\x00") {
		fields := strings.Split(record, "\x00")
		nativePath, ok := strings.CutPrefix(fields[0], "worktree ")
		if !ok || !filepath.IsAbs(nativePath) || len(fields) < 2 {
			return fmt.Errorf("managed worktree registration evidence invalid")
		}
		identity := physicalPath(nativePath)
		if seen[identity] {
			return fmt.Errorf("managed worktree registration evidence ambiguous")
		}
		seen[identity] = true
		if identity == physicalPath(path) {
			return fmt.Errorf("managed worktree remains registered after removal; inspect before retrying")
		}
		for _, field := range fields[1:] {
			if field != "bare" && field != "detached" && field != "locked" && field != "prunable" && !strings.HasPrefix(field, "HEAD ") && !strings.HasPrefix(field, "branch refs/heads/") && !strings.HasPrefix(field, "locked ") && !strings.HasPrefix(field, "prunable ") {
				return fmt.Errorf("managed worktree registration evidence invalid")
			}
		}
	}
	// Repeat filesystem evidence after the native read, not just before it.
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		if err != nil {
			return err
		}
		return fmt.Errorf("managed worktree path reappeared during observation; inspect before retrying")
	}
	return view.validate(ctx)
}
