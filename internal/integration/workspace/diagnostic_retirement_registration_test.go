package workspace

import (
	"bytes"
	"fmt"
	"os/exec"
	"time"

	"context"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

func retirementRegistrationTask(group workspacegroup.Workspace) protocol.Task {
	return protocol.Task{ID: 1, SourceRefs: []protocol.SourceRef{
		{ID: "workspace:" + group.ID, Source: "workspace", Kind: "workspace", ProvidesWorkspace: true, WorkspaceID: group.ID, Path: group.Path},
		{ID: "cached-session", Source: "tmux", Kind: "session", Title: group.SessionName, Path: group.Path},
		{ID: "cached-sandbox", Source: "sbx", Kind: "sandbox", Path: group.Path, Metadata: map[string]string{"name": group.Sandbox.Name, "id": "cached-sandbox"}},
	}}
}

func retirementRegistrationGroup(root string) workspacegroup.Workspace {
	anchor := filepath.Join(root, "registered")
	return workspacegroup.Workspace{ID: workspacegroup.ID(anchor), Name: "registered", Path: anchor, SessionName: "fixture-session", Sandbox: &workspacegroup.Sandbox{Name: "fixture-sandbox", Agent: "shell"}, Members: []workspacegroup.Member{}}
}

func TestDiagnosticRetirementRegistrationDoesNotCreateState(t *testing.T) {
	for _, kind := range []string{"missing_root", "missing_registry", "valid_without_lock"} {
		t.Run(kind, func(t *testing.T) {
			root := configureWorkspaceRoot(t)
			group := retirementRegistrationGroup(root)
			task := retirementRegistrationTask(group)
			var native []protocol.CleanupTarget
			if kind == "missing_registry" {
				if err := os.MkdirAll(root, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "valid_without_lock" {
				if err := os.MkdirAll(group.Path, 0700); err != nil {
					t.Fatal(err)
				}
				if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
					t.Fatal(err)
				}
				for _, source := range []string{"tmux", "sbx", "workspace"} {
					targets, err := RuntimeCleanupTargets(task, source)
					if err != nil {
						t.Fatal(err)
					}
					native = append(native, targets...)
				}
				// Only this fixture owns this temporary native coordination file.
				if err := os.Remove(filepath.Join(root, ".radar-workspaces.lock")); err != nil {
					t.Fatal(err)
				}
			}
			// Never inspect an unrelated missing anchor associated with the task.
			task.SourceRefs = append(task.SourceRefs, protocol.SourceRef{ID: "workspace:unrelated", Source: "workspace", Kind: "workspace", ProvidesWorkspace: true, WorkspaceID: "unrelated", Path: filepath.Join(root, "unrelated")})
			targets, err := (Source{}).DiagnosticRetirementTargets(context.Background(), task, group.ID)
			if kind == "valid_without_lock" {
				if err != nil || len(targets) != 3 || !reflect.DeepEqual(targets, native) || targets[0].Operation["runtime_id"] != "cached-session" || targets[1].Operation["runtime_id"] != "cached-sandbox" {
					t.Fatal("diagnostic target projection diverged from the native planner", err)
				}
			} else if err == nil || targets != nil {
				t.Fatal("missing registration became usable retirement evidence")
			}
			if kind == "missing_root" {
				if _, err := os.Lstat(root); !os.IsNotExist(err) {
					t.Fatal("registration-only retirement inspection created missing root")
				}
			} else if _, err := os.Lstat(filepath.Join(root, ".radar-workspaces.lock")); !os.IsNotExist(err) {
				t.Fatal("registration-only retirement inspection created native coordination state")
			}
		})
	}
}

func diagnosticRetirementRegistryFixture(t *testing.T) (string, workspacegroup.Workspace, protocol.Task) {
	t.Helper()
	root := configureWorkspaceRoot(t)
	group := retirementRegistrationGroup(root)
	if err := os.MkdirAll(group.Path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
		t.Fatal(err)
	}
	return root, group, retirementRegistrationTask(group)
}

func TestDiagnosticRetirementRegistrationRejectsUnsafeEvidence(t *testing.T) {
	for _, kind := range []string{"missing_anchor", "symlink_anchor", "shared_session", "shared_sandbox", "wrong_reference", "wrong_path", "invalid_registry", "removed_registration", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			root, group, task := diagnosticRetirementRegistryFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch kind {
			case "missing_anchor":
				if err := os.Remove(group.Path); err != nil {
					t.Fatal(err)
				}
			case "symlink_anchor":
				if err := os.Remove(group.Path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), group.Path); err != nil {
					t.Fatal(err)
				}
			case "shared_session", "shared_sandbox":
				other := group
				other.Path = filepath.Join(root, "other")
				other.ID, other.Name, other.SessionName = workspacegroup.ID(other.Path), "other", "other-session"
				other.Sandbox = &workspacegroup.Sandbox{Name: "other-sandbox", Agent: "shell"}
				if kind == "shared_session" {
					other.SessionName = group.SessionName
				} else {
					other.Sandbox.Name = group.Sandbox.Name
				}
				if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group, other}}); err != nil {
					t.Fatal(err)
				}
			case "wrong_reference":
				task.SourceRefs[0].ID = "workspace:unrelated"
			case "wrong_path":
				task.SourceRefs[0].Path = filepath.Join(root, "unrelated")
			case "invalid_registry":
				if err := os.WriteFile(workspacegroup.Path(root), []byte(`{"version":-1,"workspaces":[]}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "removed_registration":
				if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version}); err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				cancel()
			}
			if err := os.Remove(filepath.Join(root, ".radar-workspaces.lock")); err != nil {
				t.Fatal(err)
			}
			if targets, err := (Source{}).DiagnosticRetirementTargets(ctx, task, group.ID); err == nil || targets != nil {
				t.Fatal("unsafe registration, target, anchor or cancelled context became usable")
			}
			if _, err := os.Lstat(filepath.Join(root, ".radar-workspaces.lock")); !os.IsNotExist(err) {
				t.Fatal("failed diagnostic inspection created native coordination state")
			}
		})
	}
}

func TestDiagnosticRetirementRegistrationNativeAcquisitionUnchanged(t *testing.T) {
	for _, kind := range []string{"file_symlink", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			root, group, task := diagnosticRetirementRegistryFixture(t)
			path := workspacegroup.Path(root)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "file_symlink" {
				if err := os.Rename(path, path+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+".original", path); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, append(data, bytes.Repeat([]byte{' '}, 1<<20)...), 0600); err != nil {
				t.Fatal(err)
			}
			for _, source := range []string{"tmux", "sbx", "workspace"} {
				if targets, err := RuntimeCleanupTargets(task, source); err != nil || len(targets) != 1 {
					t.Fatal("diagnostic acquisition policy changed normal native planning", err)
				}
			}
			if targets, err := (Source{}).DiagnosticRetirementTargets(context.Background(), task, group.ID); err == nil || targets != nil {
				t.Fatal("diagnostic planner fell back to unrestricted native loading")
			}
		})
	}
}

func TestDiagnosticRetirementRegistrationValidationRechecksUseSuppliedReader(t *testing.T) {
	for _, kind := range []string{"session_drift", "sandbox_drift", "read_unavailable"} {
		t.Run(kind, func(t *testing.T) {
			root, _, task := diagnosticRetirementRegistryFixture(t)
			if err := os.Remove(filepath.Join(root, ".radar-workspaces.lock")); err != nil {
				t.Fatal(err)
			}
			reads := 0
			load := func(root string) (workspacegroup.Registry, error) {
				registry, err := workspacegroup.DiagnosticLoad(root)
				reads++
				if reads == 2 && err == nil {
					switch kind {
					case "session_drift":
						registry.Workspaces[0].SessionName = "replacement-session"
					case "sandbox_drift":
						registry.Workspaces[0].Sandbox.Name = "replacement-sandbox"
					case "read_unavailable":
						return workspacegroup.Registry{}, fmt.Errorf("fixture read unavailable")
					}
				}
				return registry, err
			}
			if targets, err := runtimeCleanupTargets(task, "tmux", load); err == nil || targets != nil || reads != 2 {
				t.Fatal("per-target validation skipped bounded fresh registration read")
			}
			if _, err := os.Lstat(filepath.Join(root, ".radar-workspaces.lock")); !os.IsNotExist(err) {
				t.Fatal("per-target validation fell back to native coordination")
			}
		})
	}
}

func TestDiagnosticRetirementRegistrationDoesNotWaitForWriterOrFIFO(t *testing.T) {
	const variable = "RADAR_TEST_RETIREMENT_REGISTRY_CHILD"
	if kind := os.Getenv(variable); kind != "" {
		root, group, task := diagnosticRetirementRegistryFixture(t)
		switch kind {
		case "writer":
			lock, err := os.OpenFile(filepath.Join(root, ".radar-workspaces.lock"), os.O_RDWR, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Close()
			if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
				t.Fatal(err)
			}
			defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
		case "registry_fifo":
			if err := os.Remove(workspacegroup.Path(root)); err != nil {
				t.Fatal(err)
			}
			if err := unix.Mkfifo(workspacegroup.Path(root), 0600); err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatal("invalid fixture")
		}
		targets, err := (Source{}).DiagnosticRetirementTargets(context.Background(), task, group.ID)
		if kind == "writer" {
			if err != nil || len(targets) != 3 {
				t.Fatal("native writer prevented diagnostic-only target observation", err)
			}
		} else if err == nil || targets != nil {
			t.Fatal("FIFO became usable/empty diagnostic targets")
		}
		return
	}
	for _, kind := range []string{"writer", "registry_fifo"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDiagnosticRetirementRegistrationDoesNotWaitForWriterOrFIFO$", "-test.count=1")
			cmd.Env = append(os.Environ(), variable+"="+kind)
			output, err := cmd.CombinedOutput()
			if err != nil || ctx.Err() != nil {
				t.Fatalf("isolated retirement registration read blocked/failed; child collected: %s", output)
			}
		})
	}
}
