package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/workspacelease"
)

func TestWorkspaceWriterBundleDeduplicatesCanonicalUnionAndReleasesPartialAdmission(t *testing.T) {
	root, current := sandboxActivationFixture(t)
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(current.Path, alias); err != nil {
		t.Fatal(err)
	}
	desired := current
	desired.Path = alias
	desired.Members = []workspacegroup.Member{{Path: alias}, {Path: filepath.Join(alias, "zz-new-member")}}
	release, err := acquireWorkspaceLeases(context.Background(), current, desired)
	if err != nil {
		t.Fatal("duplicate physical bundle identities self-conflicted", err)
	}
	for _, group := range []workspacegroup.Workspace{current, desired} {
		for _, path := range append([]string{group.Path}, group.Members[0].Path, desired.Members[1].Path) {
			if other, err := workspacelease.AcquireMutation(path); err == nil {
				other()
				t.Error("bundle omitted current or desired path", path)
			}
		}
	}
	for _, shared := range []workspacegroup.Workspace{
		{Path: t.TempDir(), Members: current.Members},
		{Path: t.TempDir(), Sandbox: current.Sandbox},
	} {
		if other, err := acquireWorkspaceLeases(context.Background(), shared); err == nil {
			other()
			t.Error("different anchors bypassed shared mutation resource")
		}
		// A later shared-resource conflict must not leak its earlier anchor.
		other, err := workspacelease.Acquire(shared.Path, true)
		if err != nil {
			t.Fatal("partial cleanup admission leaked", err)
		}
		other()
		other, err = workspacelease.AcquireMutation(shared.Path)
		if err != nil {
			t.Fatal("partial mutation admission leaked", err)
		}
		other()
	}
	// Independent resources can complete in either order without losing their
	// ownership. No note-lock-overlap qualification is implied by this probe.
	unrelated := workspacegroup.Workspace{Path: filepath.Join(root, "unrelated")}
	other, err := acquireWorkspaceLeases(context.Background(), unrelated)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if lease, err := workspacelease.AcquireMutation(unrelated.Path); err == nil {
		lease()
		t.Error("completing another bundle released unrelated ownership")
	}
	other()
	assertSandboxActivationReleased(t, current)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if release, err := acquireWorkspaceLeases(ctx, current); !errors.Is(err, context.Canceled) {
		if release != nil {
			release()
		}
		t.Fatal("cancelled admission was accepted", err)
	}
	assertSandboxActivationReleased(t, current)
}

func TestWorkspaceWriterEntriesRejectConflictingMutationBeforeEffects(t *testing.T) {
	for _, resource := range []string{"anchor-alias", "missing-member-alias", "sandbox"} {
		for _, entry := range []string{"open-live", "open-missing", "create-existing", "reconcile", "refresh", "standalone"} {
			t.Run(resource+"/"+entry, func(t *testing.T) {
				root, group := sandboxActivationFixture(t)
				alias := filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(group.Path, alias); err != nil {
					t.Fatal(err)
				}
				path := alias
				if resource == "missing-member-alias" {
					path = filepath.Join(alias, "member")
					if err := os.Remove(group.Members[0].Path); err != nil {
						t.Fatal(err)
					}
					// Standalone activation has just its selected path, not a
					// registered bundle. Its shared SBX identity is tested separately.
					if entry == "standalone" {
						group.Path = path
					}
				}
				var release func()
				var err error
				if resource == "sandbox" {
					release, err = workspacelease.AcquireSandboxMutation(group.Sandbox.Name)
				} else {
					release, err = workspacelease.AcquireMutation(path)
				}
				if err != nil {
					t.Fatal(err)
				}
				runner := &fakeRunner{hasSession: entry == "open-live"}
				switch entry {
				case "open-live", "open-missing":
					_, err = OpenRegisteredWorkspace(context.Background(), runner, group.Path, true)
				case "create-existing":
					_, err = Create(context.Background(), runner, CreateOptions{WorkspaceRoot: root, TaskLinkingKey: group.NoteKey(), Switch: true})
				case "reconcile":
					_, err = ApplyReconcileWorkspace(context.Background(), runner, nil, writerRequest(t, root, group))
				case "refresh":
					if removeErr := os.Remove(filepath.Join(group.Path, "notes.md")); removeErr != nil {
						t.Fatal(removeErr)
					}
					_, err = RefreshWorkspaceNote(root, group)
				case "standalone":
					_, err = CreateSessionWithOptions(context.Background(), runner, CreateSessionOptions{Path: group.Path, SessionName: group.SessionName, SandboxName: group.Sandbox.Name, Switch: true})
				}
				release()
				if err == nil || !strings.Contains(err.Error(), "mutation is in use") {
					t.Error("writer bypassed canonical mutation admission", err)
				}
				assertNotCalledContains(t, runner.calls, "tmux", "new-session")
				assertNotCalledContains(t, runner.calls, "tmux", "switch-client")
				assertNotCalledContains(t, runner.calls, "sbx", "create")
				assertNotCalledContains(t, runner.calls, "sbx", "exec")
				assertSandboxActivationReleased(t, group)
			})
		}
	}
}

func TestWorkspaceWriterCreateAndPreparationAdmitBeforeEffects(t *testing.T) {
	for _, mode := range []string{"new", "prepare"} {
		t.Run(mode, func(t *testing.T) {
			root := configureWorkspaceRoot(t)
			t.Setenv("RADAR_SOCKET", filepath.Join(t.TempDir(), "radar.sock"))
			options := CreateOptions{Name: "Writer", WorkspaceRoot: root, NoteAuthor: testNoteAuthor(t)}
			if mode == "prepare" {
				var group workspacegroup.Workspace
				root, group = activationFixture(t)
				options = CreateOptions{Path: group.Path, WorkspaceRoot: root, PrepareOnly: true}
			}
			plan, _, err := planCreate(context.Background(), &fakeRunner{}, options)
			if err != nil {
				t.Fatal(err)
			}
			release, err := workspacelease.AcquireMutation(plan.group.Path)
			if err != nil {
				t.Fatal(err)
			}
			runner := &fakeRunner{}
			_, err = Create(context.Background(), runner, options)
			release()
			if err == nil || !strings.Contains(err.Error(), "mutation is in use") {
				t.Fatal("creation/preparation bypassed mutation admission", err)
			}
			assertNotCalledContains(t, runner.calls, "tmux", "has-session")
			assertNotCalledContains(t, runner.calls, "tmux", "new-session")
			if mode == "new" {
				if _, err := os.Lstat(plan.group.Path); !os.IsNotExist(err) {
					t.Fatal("conflicting creation created its anchor", err)
				}
				registry, err := workspacegroup.Load(root)
				if err != nil || len(registry.Workspaces) != 0 {
					t.Fatal("conflicting creation published registration", registry, err)
				}
			}
		})
	}
}

func TestWorkspaceWriterCollectionKeepsCorrectLinksReadOnlyDuringMutation(t *testing.T) {
	root, group := activationFixture(t)
	release, err := workspacelease.Acquire(group.Path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := RefreshWorkspaceNote(root, group); err != nil {
		t.Fatal("read-only collection contended with cleanup", err)
	}
	if err := os.Remove(filepath.Join(group.Path, "notes.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := RefreshWorkspaceNote(root, group); err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatal("collection repaired a cleanup-owned target", err)
	}
	if _, err := os.Lstat(filepath.Join(group.Path, "notes.md")); !os.IsNotExist(err) {
		t.Fatal("blocked collection repaired a link", err)
	}
}
