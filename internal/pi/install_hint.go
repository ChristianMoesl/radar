package pi

import (
	"crypto/sha256"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed install-hint.ts
var installHint []byte

//go:embed require-sandbox.ts
var requireSandbox []byte

// RequireSandboxPath supplies a fail-closed prerequisite check in the actual
// Pi runtime. It never installs packages or implements sandbox tool routing.
func RequireSandboxPath() (string, error) {
	return materializeLaunchExtension("require-sandbox", requireSandbox)
}

// InstallHintPath materializes only the launch-time installation advice, never
// the separately installed pi-radar integration. Content-addressed paths keep
// concurrent launches and reloads independent of Radar binary updates.
func InstallHintPath() (string, error) {
	return materializeLaunchExtension("install-hint", installHint)
}

func materializeLaunchExtension(name string, source []byte) (string, error) {
	cache := os.Getenv("XDG_CACHE_HOME")
	if cache == "" {
		var err error
		cache, err = os.UserCacheDir()
		if err != nil {
			return "", err
		}
	}
	if !filepath.IsAbs(cache) {
		return "", fmt.Errorf("Pi install hint cache directory must be absolute")
	}
	dir := filepath.Join(cache, "radar", "pi")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, fmt.Sprintf("%s-%x.ts", name, sha256.Sum256(source)))
	if data, err := os.ReadFile(path); err == nil && string(data) == string(source) {
		return path, nil
	}
	file, err := os.CreateTemp(dir, "."+name+"-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(source)
	closeErr := file.Close()
	if writeErr != nil {
		return "", writeErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return "", err
	}
	return path, nil
}
