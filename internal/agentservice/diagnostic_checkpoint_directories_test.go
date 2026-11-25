package agentservice

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func checkpointDirectoryFixture(t *testing.T) (string, string, string, []byte) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "agents")
	dir := filepath.Join(root, "worker")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "checkpoint.json")
	body := []byte(`{"private":"retained-body"}`)
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	return root, dir, path, body
}

func TestDiagnosticCheckpointRejectsUnsafeDirectoryAcquisition(t *testing.T) {
	for _, kind := range []string{"root_symlink", "instance_symlink", "root_missing", "instance_missing", "leaf_missing", "stable"} {
		t.Run(kind, func(t *testing.T) {
			root, dir, path, body := checkpointDirectoryFixture(t)
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			parent := root
			if kind == "instance_symlink" || kind == "instance_missing" {
				parent = dir
			}
			switch kind {
			case "root_symlink", "instance_symlink", "root_missing", "instance_missing":
				if err := os.Rename(parent, parent+".original"); err != nil {
					t.Fatal(err)
				}
				if kind == "root_symlink" || kind == "instance_symlink" {
					if err := os.Symlink(parent+".original", parent); err != nil {
						t.Fatal(err)
					}
				}
			case "leaf_missing":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			data, info, err := diagnosticCheckpoint(path)
			switch kind {
			case "stable":
				if err != nil || !bytes.Equal(data, body) || info == nil || !os.SameFile(before, info) {
					t.Fatal("stable directory capture changed")
				}
			case "leaf_missing":
				if !os.IsNotExist(err) || data != nil || info != nil {
					t.Fatal("valid-ancestry missing leaf lost native default semantics")
				}
			default:
				if err == nil || os.IsNotExist(err) || data != nil || info != nil {
					t.Fatal("unsafe/missing directory became usable checkpoint or missing-leaf defaults")
				}
			}
		})
	}
}

func TestDiagnosticCheckpointRejectsObservedDirectoryReplacement(t *testing.T) {
	for _, kind := range []string{"root_symlink", "root_replacement", "instance_symlink", "instance_replacement", "root_removed", "instance_removed", "unrelated_root_write", "unrelated_instance_write", "stable"} {
		t.Run(kind, func(t *testing.T) {
			root, dir, path, body := checkpointDirectoryFixture(t)
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			reads := 0
			data, info, err := captureDiagnosticCheckpoint(path, func(file diagnosticCheckpointFile, path string) ([]byte, os.FileInfo, error) {
				reads++
				reader := &diagnosticCheckpointReadFixture{File: file.(*os.File)}
				reader.afterRead = func() {
					var err error
					switch kind {
					case "root_symlink", "root_replacement", "root_removed":
						if err = os.Rename(root, root+".original"); err != nil {
							t.Fatal(err)
						}
						switch kind {
						case "root_symlink":
							err = os.Symlink(root+".original", root)
						case "root_replacement":
							if err = os.Mkdir(root, 0700); err == nil {
								err = os.Rename(filepath.Join(root+".original", "worker"), dir)
							}
						}
					case "instance_symlink", "instance_replacement", "instance_removed":
						if err = os.Rename(dir, dir+".original"); err != nil {
							t.Fatal(err)
						}
						switch kind {
						case "instance_symlink":
							err = os.Symlink(dir+".original", dir)
						case "instance_replacement":
							if err = os.Mkdir(dir, 0700); err == nil {
								err = os.Link(filepath.Join(dir+".original", "checkpoint.json"), path)
							}
						}
					case "unrelated_root_write":
						err = os.WriteFile(filepath.Join(root, "unrelated"), nil, 0600)
					case "unrelated_instance_write":
						err = os.WriteFile(filepath.Join(dir, "unrelated"), nil, 0600)
					}
					if err != nil {
						t.Fatal(err)
					}
					if kind != "root_removed" && kind != "instance_removed" {
						current, err := os.Stat(path)
						if err != nil || !os.SameFile(before, current) || before.Size() != current.Size() || !before.ModTime().Equal(current.ModTime()) {
							t.Fatal("fixture did not retain the original checkpoint inode and metadata")
						}
					}
				}
				return readDiagnosticCheckpoint(reader, path)
			})
			if reads != 1 {
				t.Fatal("capture retried the reader")
			}
			switch kind {
			case "stable", "unrelated_root_write", "unrelated_instance_write":
				if err != nil || !bytes.Equal(data, body) || info == nil || !os.SameFile(before, info) || !before.ModTime().Equal(info.ModTime()) {
					t.Fatal("unrelated directory writes invalidated stable checkpoint evidence")
				}
			default:
				if err == nil || data != nil || info != nil || err.Error() != "checkpoint directories changed or unavailable" {
					t.Fatal("replaced directory retained usable checkpoint bytes or timestamps")
				}
			}
		})
	}
}
