package pi

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestInstallInstructions(t *testing.T) {
	for _, existing := range []string{"missing", "file", "symlink", "dangling symlink", "concurrent"} {
		t.Run(existing, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "radar", "AGENTS.md")
			if existing != "missing" {
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
			}
			switch existing {
			case "file":
				if err := os.WriteFile(path, []byte("user instructions"), 0640); err != nil {
					t.Fatal(err)
				}
			case "symlink", "dangling symlink":
				target := filepath.Join(dir, "target")
				if existing == "symlink" {
					if err := os.WriteFile(target, []byte("user instructions"), 0640); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			var wg sync.WaitGroup
			for range 8 {
				wg.Go(func() {
					if err := InstallInstructions(path); err != nil {
						t.Error(err)
					}
				})
			}
			wg.Wait()
			info, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if existing == "symlink" || existing == "dangling symlink" {
				if info.Mode()&os.ModeSymlink == 0 {
					t.Fatal("replaced symlink")
				}
			}
			if existing == "dangling symlink" {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("created dangling symlink target")
				}
				return
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			want := DefaultInstructions
			mode := os.FileMode(0600)
			if existing == "file" || existing == "symlink" {
				want, mode = "user instructions", 0640
			}
			info, err = os.Stat(path)
			if err != nil || string(data) != want || info.Mode().Perm() != mode {
				t.Fatalf("unexpected instructions: %q, %v, %v", data, info, err)
			}
			entries, _ := os.ReadDir(filepath.Dir(path))
			if len(entries) != 1 {
				t.Fatal("temporary instructions leaked")
			}
		})
	}
}
