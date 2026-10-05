package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// resolveSandboxEnvFile accepts the same host path forms as additional mounts.
// It does not inspect the file, so configuration can outlive a host file.
func resolveSandboxEnvFile(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve sbx.env_file %q: %w", path, err)
		}
		path = filepath.Join(home, path[2:])
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("sbx.env_file %q must be absolute or start with ~/", path)
	}
	return filepath.Clean(path), nil
}

// validateSandboxEnvFile only opens the file to check readability. SBX owns its
// format and reads its contents on creation; Radar never reads or interprets it.
func validateSandboxEnvFile(path string) error {
	if path == "" {
		return nil
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("sbx.env_file %q must be absolute", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("sbx.env_file %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("sbx.env_file %q must be a readable regular file", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("sbx.env_file %q is not readable: %w", path, err)
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil {
		return fmt.Errorf("sbx.env_file %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("sbx.env_file %q must be a readable regular file", path)
	}
	return nil
}

// sandboxCreateDiagnostic avoids propagating provider diagnostics that may echo
// env-file values. Retry classification must use the original error instead.
func sandboxCreateDiagnostic(err error, envFile string) error {
	if err == nil || envFile == "" {
		return conciseSandboxCreateError(err)
	}
	if retryableSandboxCreateError(err) {
		return errors.New("transient SBX runtime failure (provider diagnostics withheld to protect env-file values)")
	}
	return errors.New("SBX creation failed (provider diagnostics withheld to protect env-file values; check the env-file syntax, kit and SBX configuration)")
}

// sandboxCreateArgs is shared by first creation, missing-runtime recovery and
// reconciliation retries. The env-file is one host path, never shell text.
func sandboxCreateArgs(primaryWorkspace string, name string, kit SandboxKitConfig, envFile string, mounts []string) ([]string, error) {
	orderedMounts, err := sandboxPrimaryMounts(primaryWorkspace, mounts)
	if err != nil {
		return nil, err
	}
	args := []string{"create", "--name", name}
	if kit.Path != "" {
		args = append(args, "--kit", ExpandPath(kit.Path))
	}
	if envFile != "" {
		args = append(args, "--env-file", envFile)
	}
	args = append(args, kit.Name)
	return append(args, orderedMounts...), nil
}
