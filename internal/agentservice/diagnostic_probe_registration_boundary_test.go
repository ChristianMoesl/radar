package agentservice

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestDiagnosticAgentProbeRejectsRegistrationDirectorySymlinksBeforeSocket(t *testing.T) {
	for _, kind := range []string{"root", "instance"} {
		t.Run(kind, func(t *testing.T) {
			reg := probeFixture(t)
			methods := probeServer(t, reg, func(method string) any {
				if method == "hello" {
					return map[string]any{"protocol": 1, "instanceId": reg.ID}
				}
				return probeSnapshot(reg)
			})
			path := os.Getenv("RADAR_AGENT_ROOT")
			if kind == "instance" {
				path = filepath.Join(path, reg.ID)
			}
			moved := path + "-private-original"
			if err := os.Rename(path, moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(moved, path); err != nil {
				t.Fatal(err)
			}
			// The unrestricted production loader keeps its existing policy. Only
			// the explicitly approved narrow diagnostic observation is hardened.
			if native, err := load(reg.ID); err != nil || native.ID != reg.ID {
				t.Fatalf("native registration loading changed: %+v %v", native, err)
			}
			view, err := ProbeDiagnosticAgent(context.Background(), reg.Workspace.Path, reg.ID)
			if err == nil || len(methods()) != 0 || view.Snapshot != nil {
				t.Fatalf("directory symlink admitted diagnostic socket access: view=%+v err=%v methods=%v", view, err, methods())
			}
			if strings.Contains(err.Error(), "private-original") {
				t.Fatal("private directory error escaped")
			}
		})
	}
}

// Replace only registration ancestry while preserving the exact registration
// inode and bytes. Leaf-only before/after checks cannot notice this change.
func replaceProbeRegistrationDirectory(root, id, kind string) error {
	path := root
	if strings.HasPrefix(kind, "instance") {
		path = filepath.Join(root, id)
	}
	moved := path + "-private-original"
	if err := os.Rename(path, moved); err != nil {
		return err
	}
	if strings.HasSuffix(kind, "symlink") {
		return os.Symlink(moved, path)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		return err
	}
	oldLeaf, newLeaf := filepath.Join(moved, "registration.json"), filepath.Join(path, "registration.json")
	if strings.HasPrefix(kind, "root") {
		if err := os.Mkdir(filepath.Join(path, id), 0700); err != nil {
			return err
		}
		oldLeaf, newLeaf = filepath.Join(moved, id, "registration.json"), filepath.Join(path, id, "registration.json")
	}
	return os.Link(oldLeaf, newLeaf)
}

func TestDiagnosticAgentProbeRejectsDirectoryDriftWithUnchangedRegistration(t *testing.T) {
	for _, kind := range []string{"root_replaced", "instance_replaced", "root_symlink", "instance_symlink"} {
		t.Run(kind, func(t *testing.T) {
			reg := probeFixture(t)
			root := os.Getenv("RADAR_AGENT_ROOT")
			leaf := filepath.Join(root, reg.ID, "registration.json")
			before, err := os.Stat(leaf)
			if err != nil {
				t.Fatal(err)
			}
			mutation := make(chan error, 1)
			methods := probeServer(t, reg, func(method string) any {
				if method == "hello" {
					return map[string]any{"protocol": 1, "instanceId": reg.ID}
				}
				mutation <- replaceProbeRegistrationDirectory(root, reg.ID, kind)
				return probeSnapshot(reg)
			})
			view, probeErr := ProbeDiagnosticAgent(context.Background(), reg.Workspace.Path, reg.ID)
			if err := <-mutation; err != nil {
				t.Fatal("fixture mutation:", err)
			}
			after, err := os.Stat(leaf)
			if err != nil || !os.SameFile(before, after) {
				t.Fatal("fixture did not preserve registration inode", err)
			}
			if got := methods(); len(got) != 2 || got[0] != "hello" || got[1] != "snapshot" {
				t.Fatal("fixture did not reach both native methods", got)
			}
			if probeErr == nil || view.Snapshot != nil || view.RunnerEvidence != "" {
				t.Fatalf("changed directory lifetime published live evidence: %+v %v", view, probeErr)
			}
			if strings.Contains(probeErr.Error(), "private-original") {
				t.Fatal("private path escaped")
			}
		})
	}
}

func TestDiagnosticAgentProbeDirectoryAdmissionKeepsOneTargetAndIgnoresDirectoryMtime(t *testing.T) {
	reg := probeFixture(t)
	root := os.Getenv("RADAR_AGENT_ROOT")
	dir := filepath.Join(root, reg.ID)
	if err := os.Mkdir(filepath.Join(root, "unselected"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(root, "unselected", "registration.json"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ingress.token", "snapshot.json", "collection.json", "schedules.json"} {
		if err := syscall.Mkfifo(filepath.Join(dir, name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	mutation := make(chan error, 1)
	methods := probeServer(t, reg, func(method string) any {
		if method == "hello" {
			return map[string]any{"protocol": 1, "instanceId": reg.ID}
		}
		// Normal checkpoint creation and unrelated registrations can change
		// directory metadata, but are not changes of this selected ancestry.
		err := os.WriteFile(filepath.Join(dir, "unrelated-private-data"), []byte("private-body"), 0600)
		if err == nil {
			err = os.Mkdir(filepath.Join(root, "new-unselected"), 0700)
		}
		mutation <- err
		return probeSnapshot(reg)
	})
	view, err := ProbeDiagnosticAgent(context.Background(), reg.Workspace.Path, reg.ID)
	if mutationErr := <-mutation; mutationErr != nil {
		t.Fatal("fixture mutation:", mutationErr)
	}
	if err != nil || view.Problem != "none" || view.Snapshot == nil || view.Snapshot.Pending != 7 || len(methods()) != 2 {
		t.Fatalf("selected stable registration lost live evidence: %+v %v methods=%v", view, err, methods())
	}
	if view.OwnershipHold != "registered_owner_even_when_stopped" || view.LeaseEvidence != "unknown_not_inspected" {
		t.Fatal("directory admission changed native ownership/lease evidence", view)
	}
	if view.SnapshotEvidence != "fresh_runner_snapshot" || view.Snapshot.DefinitionVersion != 4 {
		t.Fatal("native snapshot projection changed", view)
	}
}
