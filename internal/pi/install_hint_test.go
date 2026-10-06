package pi

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestInstallHintIsAtomicAndContentAddressed(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", root)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			path, err := InstallHintPath()
			if err != nil {
				t.Error(err)
				return
			}
			if !strings.HasPrefix(path, filepath.Join(root, "radar", "pi", "install-hint-")) {
				t.Errorf("unexpected hint path %q", path)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != string(installHint) {
				t.Errorf("partial or wrong notice: %v", err)
			}
		}()
	}
	wg.Wait()
	path, err := InstallHintPath()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("notice permissions: %v, %v", info, err)
	}
	if err := os.WriteFile(path, []byte("modified cache"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallHintPath(); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); string(data) != string(installHint) {
		t.Fatal("modified cache was not repaired")
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary cache files leaked: %v, %v", entries, err)
	}
}

func TestInstallHintUnavailableCache(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "radar"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, cache := range []string{root, "relative"} {
		t.Setenv("XDG_CACHE_HOME", cache)
		if _, err := InstallHintPath(); err == nil {
			t.Fatalf("cache %q unexpectedly succeeded", cache)
		}
	}
}

func TestRequiredSandboxGuardIsMaterializedOrFailsExplicitly(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	path, err := RequireSandboxPath()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(filepath.Base(path), "require-sandbox-") {
		t.Fatalf("guard path = %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != string(requireSandbox) {
		t.Fatalf("guard content = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("guard permissions: %v, %v", info, err)
	}
	t.Setenv("XDG_CACHE_HOME", "relative")
	if _, err := RequireSandboxPath(); err == nil {
		t.Fatal("unavailable mandatory guard silently skipped")
	}
}
