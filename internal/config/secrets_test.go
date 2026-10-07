package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func secretFixture(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	path, err := SecretsPath()
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSecretsSavePrivatelyAndPreserveOtherNamespaces(t *testing.T) {
	path := secretFixture(t)
	if err := SaveSecrets(Secrets{"other": {"token": "keep"}, "jira": {"api_token": "old", "other_key": "keep"}}); err != nil {
		t.Fatal(err)
	}
	if err := SaveSecrets(Secrets{"jira": {"api_token": "new"}, "datadog": {"api_key": "api", "app_key": "app"}}); err != nil {
		t.Fatal(err)
	}
	data, err := LoadSecrets()
	if err != nil {
		t.Fatal(err)
	}
	if data["jira"]["api_token"] != "new" || data["jira"]["other_key"] != "keep" || data["other"]["token"] != "keep" || data["datadog"]["app_key"] != "app" {
		t.Fatal("secret updates did not preserve other entries")
	}
	for name, mode := range map[string]os.FileMode{path: 0600, filepath.Dir(path): 0700} {
		info, err := os.Stat(name)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != mode {
			t.Fatalf("%s mode = %o", name, info.Mode().Perm())
		}
	}
	t.Setenv("RADAR_TEST_SECRET", "")
	if got, err := Secret("RADAR_TEST_SECRET", "jira", "api_token"); got != "new" || err != nil {
		t.Fatal("stored secret not resolved")
	}
	t.Setenv("RADAR_TEST_SECRET", "override")
	if got, err := Secret("RADAR_TEST_SECRET", "jira", "api_token"); got != "override" || err != nil {
		t.Fatal("environment must override disk")
	}
}

func TestSecretsRejectUnsafeFilesWithoutLeakingValues(t *testing.T) {
	for _, scenario := range []string{"invalid", "wrong type", "null", "public", "symlink"} {
		t.Run(scenario, func(t *testing.T) {
			path := secretFixture(t)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			raw := `jira:
  api_token: do-not-echo`
			switch scenario {
			case "invalid":
				raw = `do-not-echo`
			case "wrong type":
				raw = `jira: do-not-echo`
			case "null":
				raw = `null`
			}
			if scenario == "symlink" {
				target := filepath.Join(t.TempDir(), "target")
				_ = os.WriteFile(target, []byte(raw), 0600)
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "public" {
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			}
			_, err := LoadSecrets()
			if err == nil || strings.Contains(err.Error(), "do-not-echo") {
				t.Fatalf("unsafe error: %v", err)
			}
			if err := SaveSecrets(Secrets{"jira": {"api_token": "replacement"}}); err == nil {
				t.Fatal("must not overwrite invalid or unsafe secrets")
			}
			data, _ := os.ReadFile(path)
			if string(data) != raw {
				t.Fatal("invalid secrets were modified")
			}
		})
	}
}

func TestCreateConfigIsPrivateValidatedAndExclusive(t *testing.T) {
	secretFixture(t)
	cfg := Default()
	cfg.LinkingMarkPrefixes = []string{"invalid-prefix"}
	if err := Create(cfg); err == nil {
		t.Fatal("invalid config accepted")
	}
	path, _ := Path()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("invalid config was written")
	}
	cfg = Default()
	if err := Create(cfg); err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(path)
	if err := Create(cfg); !os.IsExist(err) {
		t.Fatalf("second create: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != string(original) {
		t.Fatal("config overwritten")
	}
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("config not private")
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".radar-write-") {
			t.Fatal("temp file leaked")
		}
	}
}

func TestSecretsDefaultAndXDGPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	path, err := SecretsPath()
	if err != nil || path != filepath.Join(home, ".config", "radar", "secrets.yaml") {
		t.Fatalf("path = %s %v", path, err)
	}
}

func TestSetupLockRejectsConcurrentSaveAndReleases(t *testing.T) {
	secretFixture(t)
	if err := WithSetupLock(func() error {
		if err := WithSetupLock(func() error { t.Fatal("concurrent save entered critical section"); return nil }); err == nil {
			t.Fatal("concurrent setup must not mix config and secrets")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := WithSetupLock(func() error { return nil }); err != nil {
		t.Fatalf("lock not released: %v", err)
	}
}
