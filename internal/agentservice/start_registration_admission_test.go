package agentservice

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"radar/internal/integration"
	"radar/internal/protocol"
)

type registrationAdmissionFile struct {
	info os.FileInfo
	data []byte
	link string
}

func registrationAdmissionSnapshot(t *testing.T, path string) registrationAdmissionFile {
	t.Helper()
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return registrationAdmissionFile{}
	}
	if err != nil {
		t.Fatal(err)
	}
	result := registrationAdmissionFile{info: info}
	if info.Mode().IsRegular() {
		result.data, err = os.ReadFile(path)
	} else if info.Mode()&os.ModeSymlink != 0 {
		result.link, err = os.Readlink(path)
	}
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func registrationAdmissionDrift(t *testing.T, reg Registration, kind string) {
	t.Helper()
	dir, _ := instanceDir(reg.ID)
	path := filepath.Join(dir, "registration.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	changed := reg
	switch kind {
	case "unchanged":
		return
	case "unrelated_file":
		err = os.WriteFile(filepath.Join(dir, "unrelated.json"), []byte(`{}`), 0600)
	case "entrypoint":
		changed.Entrypoint += ".changed"
		err = atomicJSON(path, changed)
	case "collection":
		if changed.Collection == "" {
			changed.Collection = "github"
		} else {
			changed.Collection = ""
		}
		err = atomicJSON(path, changed)
	case "desired":
		changed.Desired = !changed.Desired
		err = atomicJSON(path, changed)
	case "workspace":
		changed.Workspace.ID += "-changed"
		err = atomicJSON(path, changed)
	case "version":
		changed.Version++
		err = atomicJSON(path, changed)
	case "extension":
		var object map[string]json.RawMessage
		if err = json.Unmarshal(before, &object); err == nil {
			object["private_extension"] = json.RawMessage(`{"changed":true}`)
			err = atomicJSON(path, object)
		}
	case "same_bytes_replacement":
		if err = os.WriteFile(path+".new", before, 0600); err == nil {
			err = os.Rename(path+".new", path)
		}
	case "same_inode_rewrite":
		err = os.WriteFile(path, append(before, '\n'), 0600)
	case "missing":
		err = os.Remove(path)
	case "symlink":
		if err = os.WriteFile(path+".target", before, 0600); err == nil {
			if err = os.Remove(path); err == nil {
				err = os.Symlink(path+".target", path)
			}
		}
	case "fifo":
		if err = os.Remove(path); err == nil {
			err = syscall.Mkfifo(path, 0600)
		}
	case "instance_replacement", "root_replacement":
		replaced := dir
		if kind == "root_replacement" {
			replaced = filepath.Dir(dir)
		}
		if err = os.Rename(replaced, replaced+".original"); err == nil {
			if err = os.MkdirAll(dir, 0700); err == nil {
				old := filepath.Join(replaced+".original", "registration.json")
				if kind == "root_replacement" {
					old = filepath.Join(replaced+".original", reg.ID, "registration.json")
				}
				err = os.Link(old, path) // Retain the EXACT leaf inode and bytes.
			}
		}
	default:
		t.Fatal("unknown drift", kind)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestNativeStartRegistrationAdmissionPreservesConcurrentChanges(t *testing.T) {
	for _, operation := range []string{"start", "restart"} {
		t.Run(operation, func(t *testing.T) {
			for _, desired := range []bool{false, true} {
				name := "stopped"
				if desired {
					name = "already_desired"
				}
				t.Run(name, func(t *testing.T) {
					for _, phase := range []string{"preparation", "healthy_lookup"} {
						t.Run(phase, func(t *testing.T) {
							for _, kind := range []string{"entrypoint", "collection", "desired", "workspace", "version", "extension", "same_bytes_replacement", "same_inode_rewrite", "missing", "symlink", "fifo", "instance_replacement", "root_replacement", "unrelated_file", "unchanged"} {
								t.Run(kind, func(t *testing.T) {
									reg := probeFixture(t)
									reg.Desired = desired
									dir, _ := instanceDir(reg.ID)
									path := filepath.Join(dir, "registration.json")
									if err := atomicJSON(path, reg); err != nil {
										t.Fatal(err)
									}
									var drifted registrationAdmissionFile
									mutate := func() {
										registrationAdmissionDrift(t, reg, kind)
										drifted = registrationAdmissionSnapshot(t, path)
										nativeStatusPeer(t, reg, `{"protocol":1,"instanceId":"worker"}`, nativeStatusSnapshot(reg))
									}
									lookups, prepared := 0, 0
									manager := New(workspaceFixture{
										lookup: func(context.Context, string) (protocol.AgentWorkspaceBinding, error) {
											lookups++
											if phase == "healthy_lookup" && lookups == 1 {
												mutate()
											}
											return reg.Workspace, nil
										},
										prepare: func(context.Context, integration.AgentWorkspaceRequest) (protocol.AgentWorkspaceBinding, integration.WorkspaceReconcilePlan, error) {
											prepared++
											mutate()
											return reg.Workspace, integration.WorkspaceReconcilePlan{}, nil
										},
									})
									retry := retryState{attempts: 3, problem: "previous failure"}
									manager.retry[reg.ID] = retry
									value, err := manager.Handle(context.Background(), protocol.AgentRequest{ID: reg.ID, Operation: operation})
									control := kind == "unchanged" || kind == "unrelated_file"
									if control {
										var status protocol.AgentStatus
										if err != nil || json.Unmarshal(value, &status) != nil || !status.Desired || !status.Running {
											t.Fatal("unchanged startup failed", err, string(value))
										}
										current, loadErr := load(reg.ID)
										want := reg
										want.Desired = true
										if loadErr != nil || current != want {
											t.Fatal("accepted startup lost desired intent", loadErr)
										}
										if _, exists := manager.retry[reg.ID]; exists {
											t.Fatal("accepted startup retained obsolete retry")
										}
									} else {
										if err == nil || !strings.Contains(err.Error(), "registration") {
											t.Error("changed registration authorized startup", err)
										}
										after := registrationAdmissionSnapshot(t, path)
										if !sameStopFile(drifted.info, after.info) || !bytes.Equal(drifted.data, after.data) || drifted.link != after.link {
											t.Error("startup overwrote/recreated concurrent registration changes")
										}
										if manager.retry[reg.ID] != retry {
											t.Error("refused startup cleared prior retry evidence")
										}
									}
									wantPrepared := 0
									if phase == "preparation" {
										wantPrepared = 1
									}
									if prepared != wantPrepared {
										t.Errorf("prepared %d times, want %d", prepared, wantPrepared)
									}
								})
							}
						})
					}
				})
			}
		})
	}
}

func TestNativeStartRegistrationAdmissionAlsoGuardsSupervision(t *testing.T) {
	reg := probeFixture(t)
	reg.Desired = true
	dir, _ := instanceDir(reg.ID)
	path := filepath.Join(dir, "registration.json")
	if err := atomicJSON(path, reg); err != nil {
		t.Fatal(err)
	}
	var drifted registrationAdmissionFile
	manager := New(workspaceFixture{
		lookup: func(context.Context, string) (protocol.AgentWorkspaceBinding, error) { return reg.Workspace, nil },
		prepare: func(context.Context, integration.AgentWorkspaceRequest) (protocol.AgentWorkspaceBinding, integration.WorkspaceReconcilePlan, error) {
			registrationAdmissionDrift(t, reg, "entrypoint")
			drifted = registrationAdmissionSnapshot(t, path)
			nativeStatusPeer(t, reg, `{"protocol":1,"instanceId":"worker"}`, nativeStatusSnapshot(reg))
			return reg.Workspace, integration.WorkspaceReconcilePlan{}, nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager.Run(ctx, cancel) // One real supervisor iteration, not a reconstructed start.
	after := registrationAdmissionSnapshot(t, path)
	if !sameStopFile(drifted.info, after.info) || !bytes.Equal(drifted.data, after.data) {
		t.Fatal("supervision overwrote a concurrent registration definition")
	}
	if retry := manager.retry[reg.ID]; retry.attempts != 1 || !strings.Contains(retry.problem, "registration") {
		t.Fatal("supervision did not retain ordinary native refusal/backoff", retry)
	}
}

func TestNativeStartRegistrationAdmissionPreflightIsReadOnly(t *testing.T) {
	for _, kind := range []string{"entrypoint", "collection", "desired", "workspace", "version", "missing", "symlink", "fifo", "oversized", "instance_symlink", "root_symlink", "cancelled", "unchanged"} {
		t.Run(kind, func(t *testing.T) {
			reg := probeFixture(t)
			reg.Desired = true
			dir, _ := instanceDir(reg.ID)
			path := filepath.Join(dir, "registration.json")
			if err := atomicJSON(path, reg); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch kind {
			case "cancelled":
				cancel()
			case "oversized":
				if err := os.WriteFile(path, bytes.Repeat([]byte(" "), protocol.AgentMaxFrameBytes+1), 0600); err != nil {
					t.Fatal(err)
				}
			case "instance_symlink", "root_symlink":
				old := dir
				if kind == "root_symlink" {
					old = filepath.Dir(dir)
				}
				if err := os.Rename(old, old+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(old+".original", old); err != nil {
					t.Fatal(err)
				}
			default:
				registrationAdmissionDrift(t, reg, kind)
			}
			before := registrationAdmissionSnapshot(t, path)
			nativeStatusPeer(t, reg, `{"protocol":1,"instanceId":"worker"}`, nativeStatusSnapshot(reg))
			lookups := 0
			manager := New(workspaceFixture{lookup: func(context.Context, string) (protocol.AgentWorkspaceBinding, error) {
				lookups++
				return reg.Workspace, nil
			}})
			retry := retryState{attempts: 2, problem: "prior failure"}
			manager.retry[reg.ID] = retry
			manager.mu.Lock()
			err := manager.start(ctx, reg, protocol.AgentRequest{})
			manager.mu.Unlock()
			if kind == "unchanged" {
				if err != nil || lookups != 1 {
					t.Fatal("valid native preflight refused", err, lookups)
				}
				return
			}
			if err == nil || lookups != 0 {
				t.Error("unsafe/stale native registration reached workspace inspection", err, lookups)
			}
			after := registrationAdmissionSnapshot(t, path)
			if !sameStopFile(before.info, after.info) || !bytes.Equal(before.data, after.data) || before.link != after.link || manager.retry[reg.ID] != retry {
				t.Error("read-only native preflight rewrote registration or retry evidence")
			}
		})
	}
}
