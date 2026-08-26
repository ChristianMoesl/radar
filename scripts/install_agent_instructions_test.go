package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstallAgentInstructionsUsesXDGConfigHome(t *testing.T) {
	configHome := filepath.Join(t.TempDir(), "config")
	runInstaller(t, configHome, t.TempDir())

	path := filepath.Join(configHome, "radar", "AGENTS.md")
	assertInstalledInstructions(t, path)
	assertMode(t, filepath.Dir(path), 0o700)
	assertMode(t, path, 0o600)
}

func TestInstallAgentInstructionsUsesHomeFallback(t *testing.T) {
	home := t.TempDir()
	runInstaller(t, "", home)

	assertInstalledInstructions(t, filepath.Join(home, ".config", "radar", "AGENTS.md"))
}

func TestInstallAgentInstructionsPreservesExistingFile(t *testing.T) {
	configHome := filepath.Join(t.TempDir(), "config")
	path := filepath.Join(configHome, "radar", "AGENTS.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	const existing = "user-owned instructions\n"
	if err := os.WriteFile(path, []byte(existing), 0o640); err != nil {
		t.Fatal(err)
	}

	runInstaller(t, configHome, t.TempDir())

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != existing {
		t.Fatalf("existing instructions changed to %q", data)
	}
	assertMode(t, path, 0o640)
}

func runInstaller(t *testing.T, configHome, home string) {
	t.Helper()
	command := exec.Command("bash", "./install-agent-instructions.sh", "../internal/pi/default-AGENTS.md")
	command.Env = append(filteredEnvironment("HOME", "XDG_CONFIG_HOME"), "HOME="+home)
	if configHome != "" {
		command.Env = append(command.Env, "XDG_CONFIG_HOME="+configHome)
	}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("install agent instructions: %v\n%s", err, output)
	}
}

func filteredEnvironment(keys ...string) []string {
	blocked := make(map[string]bool, len(keys))
	for _, key := range keys {
		blocked[key] = true
	}
	result := make([]string, 0, len(os.Environ()))
	for _, value := range os.Environ() {
		keep := true
		for key := range blocked {
			if strings.HasPrefix(value, key+"=") {
				keep = false
				break
			}
		}
		if keep {
			result = append(result, value)
		}
	}
	return result
}

func assertInstalledInstructions(t *testing.T, path string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("..", "internal", "pi", "default-AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("installed instructions = %q, want %q", got, want)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix file modes are not available on Windows")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode for %s = %o, want %o", path, got, want)
	}
}
