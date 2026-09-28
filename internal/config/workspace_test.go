package config

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestLoadDisposableEntries(t *testing.T) {
	for _, tc := range []struct {
		name, contents string
		want           []string
	}{
		{"omitted", `{}`, []string{}},
		{"empty", `{"workspace":{"cleanup":{"disposable_entries":[]}}}`, []string{}},
		{"configured", `{"workspace":{"cleanup":{"disposable_entries":[".pnpm-store","scratch.log","cache files"]}}}`, []string{".pnpm-store", "scratch.log", "cache files"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			path, err := EnsureFile()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(tc.contents), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load()
			if err != nil || !reflect.DeepEqual(cfg.Workspace.Cleanup.DisposableEntries, tc.want) {
				t.Fatalf("config = %+v, err = %v", cfg.Workspace.Cleanup, err)
			}
			if data, err := os.ReadFile(path); err != nil || string(data) != tc.contents {
				t.Fatalf("user config was rewritten: %s, %v", data, err)
			}
		})
	}
}

func TestDisposableEntriesDefaultIsEmpty(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, err := EnsureFile()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"disposable_entries": []`) {
		t.Fatalf("generated defaults do not expose an empty allowlist: %s", data)
	}
	if got := Default().Workspace.Cleanup.DisposableEntries; len(got) != 0 {
		t.Fatalf("default permissions = %v", got)
	}
}

func TestLoadRejectsInvalidDisposableEntries(t *testing.T) {
	for _, name := range []string{
		"", " ", " cache", "cache ", ".", "..", "../cache", "cache/child", "cache/", "/tmp/cache",
		`cache\child`, `C:\cache`, "*", "cache?", "[abc]", "cache{1,2}", "cache\x00", "cache\n",
		"notes.md", "NOTES.MD",
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			path, err := EnsureFile()
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal([]string{name})
			if err != nil {
				t.Fatal(err)
			}
			data := `{"workspace":{"cleanup":{"disposable_entries":` + string(encoded) + `}}}`
			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), "workspace.cleanup.disposable_entries[0]") {
				t.Fatalf("Load(%q) error = %v", name, err)
			}
		})
	}
}

func TestRejectDuplicateDisposableEntries(t *testing.T) {
	cfg := Default()
	cfg.Workspace.Cleanup.DisposableEntries = []string{"cache", "cache"}
	if err := validate(cfg); err == nil || !strings.Contains(err.Error(), "duplicates") {
		t.Fatalf("validation = %v", err)
	}
}
