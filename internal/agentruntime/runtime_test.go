package agentruntime

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

type member struct {
	name, content string
	mode          os.FileMode
}

func privateTemp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func fixture(t *testing.T, members ...member) []byte {
	t.Helper()
	var out bytes.Buffer
	w := zip.NewWriter(&out)
	for _, m := range members {
		h := &zip.FileHeader{Name: m.name}
		h.SetMode(m.mode)
		f, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(m.content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestContentAddressedActivationRetainsOldRuntimeAndState(t *testing.T) {
	root := privateTemp(t)
	state := filepath.Join(root, "counter", "checkpoint")
	if err := os.MkdirAll(filepath.Dir(state), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state, []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	first := fixture(t, member{"dist/src/runner.js", "old", 0600}, member{"node_modules/pkg", "../dist", os.ModeSymlink | 0700})
	old, err := materialize(root, first)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(old)
	again, err := materialize(root, first)
	if err != nil || again != old {
		t.Fatalf("reuse: %s, %v", again, err)
	}
	unchanged, _ := os.Stat(again)
	if !os.SameFile(info, unchanged) {
		t.Fatal("existing runtime was rewritten")
	}
	newRunner, err := materialize(root, fixture(t, member{"dist/src/runner.js", "new", 0600}))
	if err != nil || old == newRunner {
		t.Fatalf("upgrade: %s, %v", newRunner, err)
	}
	for file, want := range map[string]string{old: "old", newRunner: "new", state: "retained"} {
		got, err := os.ReadFile(file)
		if err != nil || string(got) != want {
			t.Fatalf("%s: %s, %v", file, got, err)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(old))), "node_modules/pkg/src/runner.js")); err != nil {
		t.Fatalf("relative dependency link: %v", err)
	}
}

func TestConcurrentActivationAndAbandonedPreparation(t *testing.T) {
	root := privateTemp(t)
	cache := filepath.Join(root, ".runtime")
	if err := os.MkdirAll(filepath.Join(cache, ".prepare-interrupted"), 0700); err != nil {
		t.Fatal(err)
	}
	data := fixture(t, member{"dist/src/runner.js", "runner", 0600})
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := materialize(root, data); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	entries, _ := os.ReadDir(cache)
	if len(entries) != 2 {
		t.Fatalf("partial runtime activated or scratch leaked: %v", entries)
	}
}

func TestRejectSharedOrSymlinkedCache(t *testing.T) {
	for _, kind := range []string{"shared", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			root := privateTemp(t)
			cache := filepath.Join(root, ".runtime")
			if kind == "shared" {
				if err := os.Mkdir(cache, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(cache, 0777); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Symlink(t.TempDir(), cache); err != nil {
				t.Fatal(err)
			}
			if _, err := materialize(root, fixture(t, member{"dist/src/runner.js", "runner", 0600})); err == nil {
				t.Fatal("unsafe cache accepted")
			}
		})
	}
}

func TestRejectUnsafeArchiveWithoutActivating(t *testing.T) {
	for _, m := range []member{
		{"../escaped", "no", 0600}, {"/absolute", "no", 0600},
		{"link", "../outside", os.ModeSymlink | 0700},
		{"link", "/outside", os.ModeSymlink | 0700},
		{".complete", "forged", 0600},
	} {
		t.Run(m.name+m.content, func(t *testing.T) {
			root := privateTemp(t)
			if _, err := materialize(root, fixture(t, m)); err == nil {
				t.Fatal("unsafe archive accepted")
			}
			entries, _ := os.ReadDir(filepath.Join(root, ".runtime"))
			if len(entries) != 0 {
				t.Fatal("failed preparation left active runtime")
			}
		})
	}
}
