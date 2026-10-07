package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"

	"radar/internal/configfile"
)

// Secrets are kept outside Config so previews, diagnostics and config consumers
// cannot accidentally serialize credentials. Each integration owns its namespace.
type Secrets map[string]map[string]string

func SecretsPath() (string, error) {
	path, err := Path()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(path), "secrets.yaml"), nil
}

func LoadSecrets() (Secrets, error) {
	path, err := SecretsPath()
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return Secrets{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read secrets.yaml: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("secrets.yaml must be a regular owner-only file — use chmod 600 on %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read secrets.yaml: %w", err)
	}
	var secrets Secrets
	// YAML errors can include input values. Never propagate them from a secret file.
	if configfile.Decode(data, &secrets) != nil || secrets == nil {
		return nil, fmt.Errorf("secrets.yaml must contain a YAML mapping of integration secret mappings")
	}
	return secrets, nil
}

// Secret keeps environment credentials available for managed/automated setups.
// Only consult disk when the caller actually needs a stored secret.
func Secret(environment, integration, key string) (string, error) {
	if value := strings.TrimSpace(os.Getenv(environment)); value != "" {
		return value, nil
	}
	secrets, err := LoadSecrets()
	if err != nil {
		return "", err
	}
	return secrets[integration][key], nil
}

func EnvOr(environment, value string) string {
	if override := strings.TrimSpace(os.Getenv(environment)); override != "" {
		return override
	}
	return strings.TrimSpace(value)
}

// SaveSecrets updates only supplied keys, preserving other integrations. The
// private temp file is renamed, never truncated in place or written via a symlink.
func SaveSecrets(updates Secrets) error {
	if len(updates) == 0 {
		return nil
	}
	secrets, err := LoadSecrets()
	if err != nil {
		return err
	}
	before, err := secretsDocument(secrets)
	if err != nil {
		return fmt.Errorf("encode secrets.yaml")
	}
	for integration, values := range updates {
		if secrets[integration] == nil {
			secrets[integration] = map[string]string{}
		}
		for key, value := range values {
			secrets[integration][key] = value
		}
	}
	next, err := secretsDocument(secrets)
	if err != nil {
		return fmt.Errorf("encode secrets.yaml")
	}
	path, err := SecretsPath()
	if err != nil {
		return err
	}
	original, err := os.ReadFile(path)
	if err == nil {
		doc, err := configfile.Parse(original)
		if err != nil {
			return fmt.Errorf("secrets.yaml must contain valid YAML mappings")
		}
		if !configfile.Patch(doc, before, next) {
			return nil
		}
		next = doc
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read secrets.yaml: %w", err)
	}
	data, err := configfile.Encode(next)
	if err != nil {
		return fmt.Errorf("encode secrets.yaml")
	}
	if bytes.Equal(data, original) {
		return nil
	}
	return writePrivateFile(path, data, false)
}

// Create writes a validated config without replacing even a concurrently created
// file. Its presence is the first-run completion marker; background startup must
// not write a default config ahead of the user's consent.
func Create(cfg Config) error {
	if err := validate(cfg); err != nil {
		return err
	}
	data, err := marshalConfig(cfg)
	if err != nil {
		return err
	}
	path, err := Path()
	if err != nil {
		return err
	}
	return writePrivateFile(path, data, true)
}

func writePrivateFile(path string, data []byte, exclusive bool) error {
	return writeSetupFile(path, data, exclusive, true)
}

func writeSetupFile(path string, data []byte, exclusive, privateDirectory bool) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if privateDirectory {
		if err := os.Chmod(dir, 0o700); err != nil {
			return err
		}
	}
	file, err := os.CreateTemp(dir, ".radar-write-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if exclusive {
		return os.Link(file.Name(), path)
	}
	return os.Rename(file.Name(), path)
}

// WithSetupLock serializes the config/secrets bundle, not just individual file
// renames. Locking the directory needs no persistent marker that a cancelled
// process could strand. Manual config writers are still protected by Create's
// no-replace operation.
func WithSetupLock(action func() error) error {
	path, err := Path()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	file, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return fmt.Errorf("another Radar setup is saving configuration — wait for it to finish before retrying")
	}
	defer unix.Flock(int(file.Fd()), unix.LOCK_UN)
	return action()
}
