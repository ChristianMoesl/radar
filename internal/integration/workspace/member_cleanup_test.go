package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"radar/internal/integration"
)

func TestMemberCleanupValidationClosesPinnedHandles(t *testing.T) {
	root, g, _ := inspectionFixture(t)
	v, err := PrepareDiscardingWorkspaceMember(root, g.ID, g.Members[0], integration.CleanupConfirmed)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := v.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	v.Close()
	for _, handle := range []*os.Root{v.anchor.parent, v.anchor.anchor, v.member} {
		if _, err := handle.Stat("."); !errors.Is(err, os.ErrClosed) {
			t.Fatal("pinned handle remained open", err)
		}
	}
}

func TestMemberCleanupValidationRejectsSwappedTree(t *testing.T) {
	for _, stage := range []string{"scan", "validate"} {
		for _, kind := range []string{"member", "anchor", "root", "symlink"} {
			t.Run(stage+"/"+kind, func(t *testing.T) {
				root, g, _ := inspectionFixture(t)
				v, err := PrepareDiscardingWorkspaceMember(root, g.ID, g.Members[0], integration.CleanupConfirmed)
				if err != nil {
					t.Fatal(err)
				}
				defer v.Close()
				if stage == "validate" {
					if err := v.Scan(context.Background()); err != nil {
						t.Fatal(err)
					}
				}
				path := g.Members[0].Path
				if kind == "anchor" {
					path = g.Path
				}
				if kind == "root" {
					path = root
				}
				moved := path + "-pinned"
				if err := os.Rename(path, moved); err != nil {
					t.Fatal(err)
				}
				if kind == "symlink" {
					if err := os.Symlink(moved, path); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.MkdirAll(g.Members[0].Path, 0700); err != nil {
						t.Fatal(err)
					}
				}
				if stage == "scan" {
					err = v.Scan(context.Background())
				} else {
					err = v.Validate(context.Background())
				}
				if err == nil {
					t.Fatal("detached tree was accepted")
				}
				if _, err := os.Stat(moved); err != nil {
					t.Fatal("original tree changed", err)
				}
			})
		}
	}
}

func TestMemberCleanupValidationCancellation(t *testing.T) {
	root, g, _ := inspectionFixture(t)
	v, err := PrepareDiscardingWorkspaceMember(root, g.ID, g.Members[0], integration.CleanupConfirmed)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, run := range []func(context.Context) error{v.Scan, v.Validate} {
		if err := run(ctx); !errors.Is(err, context.Canceled) {
			t.Fatal("cancellation ignored", err)
		}
	}
}

func TestMemberCleanupValidationDoesNotFollowSymlinks(t *testing.T) {
	root, g, _ := inspectionFixture(t)
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(outside, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(g.Members[0].Path, "external")); err != nil {
		t.Fatal(err)
	}
	if err := ValidateDiscardingWorkspaceMember(root, g.ID, g.Members[0], integration.CleanupConfirmed); err != nil {
		t.Fatal("followed external symlink", err)
	}
	if _, err := os.Stat(filepath.Join(outside, ".git")); err != nil {
		t.Fatal("external contents changed", err)
	}
}
