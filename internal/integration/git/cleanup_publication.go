package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"radar/internal/integration/workspace"
	workspacegroup "radar/internal/integration/workspace/group"
)

// Publication can involve remote I/O. Its proof is valid only for this exact
// branch and owner. The caller retains deletion admission/update prevention,
// then resumes the original note transaction and repeats member validation.
// This is not authority to move destructive Git or note operations outside it.
type cleanupPublication struct {
	owner  workspacegroup.Workspace
	member workspacegroup.Member
	head   string
	files  map[string]os.FileInfo
}

func newCleanupPublication(ctx context.Context, root string, owner workspacegroup.Workspace, member workspacegroup.Member) (*cleanupPublication, error) {
	p := &cleanupPublication{owner: owner, member: member, files: map[string]os.FileInfo{}}
	for _, path := range []string{root, owner.Path, member.Path, member.Repository} {
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		p.files[path] = info
	}
	head, err := gitOutput(ctx, member.Repository, "rev-parse", "--verify", "refs/heads/"+member.Branch)
	if err != nil {
		return nil, err
	}
	p.head = strings.TrimSpace(head)
	return p, nil
}

func (p *cleanupPublication) refresh(ctx context.Context, root string, current workspacegroup.Workspace) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	configured, err := workspace.DefaultRoot()
	if err != nil {
		return err
	}
	if filepath.Clean(configured) != filepath.Clean(root) {
		return fmt.Errorf("configured workspace root changed; preview cleanup again")
	}
	if !reflect.DeepEqual(p.owner, current) {
		return fmt.Errorf("managed worktree registration changed; preview cleanup again")
	}
	for path, expected := range p.files {
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if !os.SameFile(expected, info) {
			return fmt.Errorf("managed worktree filesystem identity changed; preview cleanup again")
		}
	}
	return ctx.Err()
}

func (p *cleanupPublication) checkHead(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	head, err := gitOutput(ctx, p.member.Repository, "rev-parse", "--verify", "refs/heads/"+p.member.Branch)
	if err != nil {
		return err
	}
	if strings.TrimSpace(head) != p.head {
		return fmt.Errorf("managed branch changed during publication verification; preview cleanup again")
	}
	return ctx.Err()
}

func (p *cleanupPublication) verify(ctx context.Context) error {
	if err := p.checkHead(ctx); err != nil {
		return err
	}
	safe, err := workspace.BranchPublishedOrMerged(ctx, workspace.ExecRunner{}, p.member.Repository, p.member.Branch)
	if err != nil {
		return fmt.Errorf("branch publication or merge could not be verified: %w", err)
	}
	if err := p.checkHead(ctx); err != nil {
		return err
	}
	if !safe {
		return fmt.Errorf("local branch has commits not verified as published or merged")
	}
	return nil
}
