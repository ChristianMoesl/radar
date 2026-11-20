package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"

	"radar/internal/integration"
	workspacegroup "radar/internal/integration/workspace/group"
)

// MemberCleanupValidation captures metadata and rooted handles under the caller's
// note transaction. Scan may run outside it; Validate must run inside the final
// note transaction before effects. This read-only object grants no deletion
// authority: mutation callers must retain their actual-owner/member admission.
type MemberCleanupValidation struct {
	root     string
	owner    workspacegroup.Workspace
	expected workspacegroup.Member
	anchor   *cleanupAnchor
	member   *os.Root
	rootInfo os.FileInfo
}

func PrepareDiscardingWorkspaceMember(root, id string, expected workspacegroup.Member, mode integration.CleanupMode) (*MemberCleanupValidation, error) {
	registry, err := workspacegroup.Load(root)
	if err != nil {
		return nil, err
	}
	group, found := workspacegroup.FindByID(registry, id)
	if !found {
		return nil, fmt.Errorf("discarding cleanup requires a registered workspace: %s", id)
	}
	if !slices.ContainsFunc(group.Members, func(member workspacegroup.Member) bool {
		return sameCleanPath(member.Path, expected.Path) && sameCleanPath(member.Repository, expected.Repository) && member.Branch == expected.Branch
	}) {
		return nil, fmt.Errorf("managed worktree registration changed; preview cleanup again")
	}
	if !mode.DiscardChanges() {
		return nil, fmt.Errorf("member discard validation requires a discarding cleanup mode")
	}
	if err := validateDiscardingAnchor(root, registry, group); err != nil {
		return nil, err
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	a, err := openCleanupAnchor(root, group.Path)
	if err != nil {
		return nil, err
	}
	if a.anchor == nil {
		a.close()
		return nil, fmt.Errorf("workspace anchor is missing")
	}
	v := &MemberCleanupValidation{root: root, owner: group, expected: expected, anchor: a, rootInfo: rootInfo}
	name := filepath.Base(expected.Path)
	info, err := a.anchor.Lstat(name)
	if err != nil {
		v.Close()
		return nil, err
	}
	if !info.IsDir() {
		v.Close()
		return nil, fmt.Errorf("managed workspace member must be a real directory: %s", expected.Path)
	}
	v.member, err = a.anchor.OpenRoot(name)
	if err != nil {
		v.Close()
		return nil, err
	}
	pinned, err := v.member.Stat(".")
	if err != nil || !os.SameFile(info, pinned) {
		v.Close()
		return nil, fmt.Errorf("managed worktree filesystem identity changed; preview cleanup again")
	}
	return v, nil
}

func (v *MemberCleanupValidation) Close() {
	if v.member != nil {
		v.member.Close()
	}
	v.anchor.close()
}

func (v *MemberCleanupValidation) checkFilesystem() error {
	root, err := os.Stat(v.root)
	if err != nil {
		return err
	}
	if !os.SameFile(v.rootInfo, root) {
		return fmt.Errorf("workspace root filesystem identity changed; preview cleanup again")
	}
	if err := v.anchor.validatePath(v.owner.Path); err != nil {
		return err
	}
	current, err := v.anchor.anchor.Lstat(filepath.Base(v.expected.Path))
	if err != nil {
		return err
	}
	pinned, err := v.member.Stat(".")
	if err != nil {
		return err
	}
	if !current.IsDir() || !os.SameFile(current, pinned) {
		return fmt.Errorf("managed worktree filesystem identity changed; preview cleanup again")
	}
	return nil
}

func (v *MemberCleanupValidation) Scan(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := v.checkFilesystem(); err != nil {
		return err
	}
	managed := map[string]bool{}
	for _, member := range v.owner.Members {
		managed[pathKey(filepath.Base(member.Path))] = true
	}
	if err := v.anchor.inspectDiscardingTree(v.owner, managed, filepath.Base(v.expected.Path)); err != nil {
		return err
	}
	return ctx.Err()
}

func (v *MemberCleanupValidation) Validate(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	registry, err := workspacegroup.Load(v.root)
	if err != nil {
		return err
	}
	current, found := workspacegroup.FindByID(registry, v.owner.ID)
	if !found || !reflect.DeepEqual(current, v.owner) {
		return fmt.Errorf("managed worktree registration changed; preview cleanup again")
	}
	if err := v.checkFilesystem(); err != nil {
		return err
	}
	if err := validateDiscardingAnchor(v.root, registry, current); err != nil {
		return err
	}
	return ctx.Err()
}
