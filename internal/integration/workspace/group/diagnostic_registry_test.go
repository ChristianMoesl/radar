package workspacegroup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func diagnosticRegistryFixture(t *testing.T, root string, count int) []byte {
	t.Helper()
	registry := Registry{Version: Version, Workspaces: []Workspace{}}
	for i := range count {
		registry.Workspaces = append(registry.Workspaces, testWorkspace(root, fmt.Sprintf("work-%04d", i)))
	}
	data, err := json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(root), data, 0600); err != nil {
		t.Fatal(err)
	}
	return data
}

func TestDiagnosticRegistryBoundsAndAcquisition(t *testing.T) {
	for _, kind := range []string{"missing_root", "missing_file", "valid", "exact_bytes", "over_bytes", "exact_entries", "over_entries", "file_symlink", "root_symlink", "directory", "root_fifo", "invalid", "unsupported", "duplicate", "private_invalid"} {
		t.Run(kind, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "registry")
			count := 1
			if kind == "exact_entries" || kind == "over_entries" {
				count = diagnosticRegistryEntries
				if kind == "over_entries" {
					count++
				}
			}
			var original []byte
			if kind != "missing_root" && kind != "root_fifo" {
				original = diagnosticRegistryFixture(t, root, count)
			}
			var err error
			switch kind {
			case "missing_file":
				err = os.Remove(Path(root))
			case "exact_bytes", "over_bytes":
				size := diagnosticRegistryBytes
				if kind == "over_bytes" {
					size++
				}
				err = os.WriteFile(Path(root), append(bytes.Clone(original), bytes.Repeat([]byte{' '}, size-len(original))...), 0600)
			case "file_symlink":
				err = os.Rename(Path(root), Path(root)+".original")
				if err == nil {
					err = os.Symlink(Path(root)+".original", Path(root))
				}
			case "root_symlink":
				err = os.Rename(root, root+".original")
				if err == nil {
					err = os.Symlink(root+".original", root)
				}
			case "directory":
				err = os.Remove(Path(root))
				if err == nil {
					err = os.Mkdir(Path(root), 0700)
				}
			case "root_fifo":
				err = unix.Mkfifo(root, 0600)
			case "invalid":
				err = os.WriteFile(Path(root), []byte("private-invalid"), 0600)
			case "unsupported":
				err = os.WriteFile(Path(root), []byte(`{"version":-1,"workspaces":[]}`), 0600)
			case "duplicate":
				registry := Registry{Version: Version, Workspaces: []Workspace{testWorkspace(root, "same"), testWorkspace(root, "same")}}
				data, _ := json.Marshal(registry)
				err = os.WriteFile(Path(root), data, 0600)
			case "private_invalid":
				registry := Registry{Version: Version, Workspaces: []Workspace{{ID: "private-id", Name: "private-name", Path: "private-relative"}}}
				data, _ := json.Marshal(registry)
				err = os.WriteFile(Path(root), data, 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			registry, err := DiagnosticLoad(root)
			valid := kind == "missing_root" || kind == "missing_file" || kind == "valid" || kind == "exact_bytes" || kind == "exact_entries"
			if valid {
				want := count
				if kind == "missing_root" || kind == "missing_file" {
					want = 0
				}
				if err != nil || registry.Version != Version || len(registry.Workspaces) != want {
					t.Fatal("valid bounded registry/missing semantics lost", err)
				}
			} else if err == nil || !reflect.DeepEqual(registry, Registry{}) || err.Error() != "workspace registry diagnostic evidence unavailable" {
				t.Fatal("unsafe/invalid registry became usable or exposed private details")
			}
			if kind == "missing_root" {
				if _, err := os.Lstat(root); !os.IsNotExist(err) {
					t.Fatal("diagnostic created absent root")
				}
			} else if kind != "root_fifo" {
				if _, err := os.Lstat(filepath.Join(root, lockFileName)); !os.IsNotExist(err) {
					t.Fatal("diagnostic created native coordination file")
				}
			}
		})
	}
}

func TestDiagnosticRegistryNativeAcquisitionPolicyUnchanged(t *testing.T) {
	for _, kind := range []string{"oversized", "file_symlink", "root_symlink"} {
		t.Run(kind, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "registry")
			data := diagnosticRegistryFixture(t, root, 1)
			switch kind {
			case "oversized":
				if err := os.WriteFile(Path(root), append(data, bytes.Repeat([]byte{' '}, diagnosticRegistryBytes)...), 0600); err != nil {
					t.Fatal(err)
				}
			case "file_symlink":
				if err := os.Rename(Path(root), Path(root)+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(Path(root)+".original", Path(root)); err != nil {
					t.Fatal(err)
				}
			case "root_symlink":
				if err := os.Rename(root, root+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(root+".original", root); err != nil {
					t.Fatal(err)
				}
			}
			if value, err := Load(root); err != nil || len(value.Workspaces) != 1 {
				t.Fatal("diagnostic admission changed normal native loading", err)
			}
			if value, err := DiagnosticLoad(root); err == nil || !reflect.DeepEqual(value, Registry{}) {
				t.Fatal("diagnostic accepted native-only acquisition policy")
			}
		})
	}
}

func TestDiagnosticRegistryDoesNotWaitForCoordinationOrFIFO(t *testing.T) {
	const variable = "RADAR_TEST_DIAGNOSTIC_WORKSPACE_REGISTRY_CHILD"
	if kind := os.Getenv(variable); kind != "" {
		root := filepath.Join(t.TempDir(), "registry")
		diagnosticRegistryFixture(t, root, 1)
		lockPath := filepath.Join(root, lockFileName)
		switch kind {
		case "writer":
			// Isolated child: hold both native coordination mechanisms. A broken
			// diagnostic cannot strand the parent; CommandContext kills/joins it.
			registryMu.Lock()
			defer registryMu.Unlock()
			lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Close()
			if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
				t.Fatal(err)
			}
			defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
		case "lock_fifo":
			if err := unix.Mkfifo(lockPath, 0600); err != nil {
				t.Fatal(err)
			}
		case "registry_fifo":
			if err := os.Remove(Path(root)); err != nil {
				t.Fatal(err)
			}
			if err := unix.Mkfifo(Path(root), 0600); err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatal("invalid fixture")
		}
		registry, err := DiagnosticLoad(root)
		if kind == "registry_fifo" {
			if err == nil || !reflect.DeepEqual(registry, Registry{}) {
				t.Fatal("FIFO became usable/empty evidence")
			}
		} else if err != nil || len(registry.Workspaces) != 1 {
			t.Fatal("coordination state blocked a stable read", err)
		}
		return
	}
	for _, kind := range []string{"writer", "lock_fifo", "registry_fifo"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDiagnosticRegistryDoesNotWaitForCoordinationOrFIFO$", "-test.count=1")
			cmd.Env = append(os.Environ(), variable+"="+kind)
			output, err := cmd.CombinedOutput()
			if err != nil || ctx.Err() != nil {
				t.Fatalf("isolated registry inspection blocked/failed; child collected: %s", output)
			}
		})
	}
}

type diagnosticRegistryReadFixture struct {
	*os.File
	afterRead func()
	short     bool
	readError bool
}

func (f *diagnosticRegistryReadFixture) Read(p []byte) (int, error) {
	if f.short {
		n, _ := f.File.Read(p[:1])
		return n, io.EOF
	}
	n, err := f.File.Read(p)
	if f.afterRead != nil {
		mutate := f.afterRead
		f.afterRead = nil
		mutate()
	}
	if f.readError {
		return n, errors.New("private-read-error")
	}
	return n, err
}

func TestDiagnosticRegistryCaptureRejectsObservedChanges(t *testing.T) {
	for _, kind := range []string{"file_replaced", "root_symlink", "root_replaced", "removed", "grown", "short_read", "read_error", "unrelated_write", "stable"} {
		t.Run(kind, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "registry")
			data := diagnosticRegistryFixture(t, root, 1)
			before, _ := os.Stat(Path(root))
			registry, err := diagnosticLoad(root, func(file diagnosticRegistryFile, path string) ([]byte, error) {
				reader := &diagnosticRegistryReadFixture{File: file.(*os.File), short: kind == "short_read", readError: kind == "read_error"}
				reader.afterRead = func() {
					var err error
					switch kind {
					case "file_replaced":
						err = os.Rename(path, path+".original")
						if err == nil {
							err = os.WriteFile(path, data, 0600)
						}
						if err == nil {
							err = os.Chtimes(path, before.ModTime(), before.ModTime())
						}
					case "root_symlink", "root_replaced":
						err = os.Rename(root, root+".original")
						if err == nil && kind == "root_symlink" {
							err = os.Symlink(root+".original", root)
						} else if err == nil {
							err = os.Mkdir(root, 0700)
							if err == nil {
								err = os.Link(Path(root+".original"), path)
							}
						}
						current, statErr := os.Stat(path)
						if err == nil && (statErr != nil || !os.SameFile(before, current) || before.Size() != current.Size() || !before.ModTime().Equal(current.ModTime())) {
							t.Fatal("root fixture did not retain registry inode/metadata")
						}
					case "removed":
						err = os.Remove(path)
					case "grown":
						err = os.WriteFile(path, append(bytes.Clone(data), ' '), 0600)
					case "unrelated_write":
						err = os.WriteFile(filepath.Join(root, "unrelated"), nil, 0600)
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				return readDiagnosticRegistry(reader, path)
			})
			if kind == "stable" || kind == "unrelated_write" {
				if err != nil || len(registry.Workspaces) != 1 {
					t.Fatal("stable registry lost under unrelated directory writes", err)
				}
			} else if err == nil || !reflect.DeepEqual(registry, Registry{}) || strings.Contains(err.Error(), "private-") || strings.Contains(err.Error(), root) {
				t.Fatal("changed/incomplete registry became usable or exposed private details")
			}
		})
	}
}

func TestDiagnosticRegistrySharesNativeRecipeValidation(t *testing.T) {
	root := t.TempDir()
	group := testWorkspace(root, "recorded")
	group.Name = " recorded "
	group.Model = " private-model "
	group.Members = []Member{{Repository: filepath.Join(root, "source"), Path: filepath.Join(group.Path, "member"), Branch: " fixture "}}
	group.Sandbox = &Sandbox{Name: " sandbox ", Agent: " shell ", KitPath: filepath.Join(root, "kit"), EnvFile: filepath.Join(root, "private-env"), ReadyCommand: []string{"private-command"}, Mounts: []string{root + ":ro", root + ":ro"}, AdditionalMounts: []SandboxMount{{Path: filepath.Join(root, "z")}, {Path: filepath.Join(root, "a"), ReadOnly: true}}, Ports: []SandboxPort{{HostPort: 9002, SandboxPort: 80}, {HostPort: 9001, SandboxPort: 81}}}
	registry := Registry{Version: Version, Workspaces: []Workspace{group}}
	data, err := json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(root), data, 0600); err != nil {
		t.Fatal(err)
	}
	native, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := DiagnosticLoad(root)
	if err != nil || !reflect.DeepEqual(native, observed) {
		t.Fatal("diagnostic recipe normalization diverged from native loader", err)
	}
	// No referenced environment, command, kit, mount, repository or member exists.
	// Validation remains lexical; no private referenced content was required.
	for _, path := range []string{group.Sandbox.KitPath, group.Sandbox.EnvFile, group.Members[0].Repository} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatal("reader created referenced private state")
		}
	}
}

func TestDiagnosticRegistryRejectsRelativeRoot(t *testing.T) {
	value, err := DiagnosticLoad("relative-registry")
	if err == nil || !reflect.DeepEqual(value, Registry{}) {
		t.Fatal("relative selector became diagnostic registry evidence")
	}
}
