package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"

	"radar/internal/configfile"
)

// SetupDraft retains the original document so setup changes only fields the
// user edits, including when the document contains settings unknown to this UI.
// It also detects edits to either file while questions are being answered.
type SetupDraft struct {
	Config                 Config
	Existing               bool
	configFile, secretFile setupFile
	original               *yaml.Node
}

type setupFile struct {
	path, target string
	data         []byte
	exists       bool
	mode         os.FileMode
}

func readSetupFile(path string) (setupFile, error) {
	f := setupFile{path: path, target: path}
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return f, nil
	} else if err != nil {
		return f, err
	}
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return f, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return f, err
	}
	if !info.Mode().IsRegular() {
		return f, fmt.Errorf("setup requires a regular configuration file: %s", path)
	}
	f.mode = info.Mode()
	f.target = target
	f.exists = true
	f.data, err = os.ReadFile(target)
	return f, err
}

func LoadSetupDraft() (*SetupDraft, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	f, err := readSetupFile(path)
	if err != nil {
		return nil, err
	}
	if f.exists {
		if _, err := configfile.Parse(f.data); err != nil {
			return nil, fmt.Errorf("config.yaml is invalid — repair it before running setup: %w", err)
		}
	}
	cfg, err := Load()
	if err != nil {
		return nil, fmt.Errorf("config.yaml is invalid — repair it before running setup: %w", err)
	}
	if _, err := LoadSecrets(); err != nil {
		return nil, err
	}
	secretPath, err := SecretsPath()
	if err != nil {
		return nil, err
	}
	secrets, err := readSetupFile(secretPath)
	if err != nil {
		return nil, err
	}
	original, err := configDocument(cfg)
	if err != nil {
		return nil, err
	}
	d := &SetupDraft{Config: cfg, Existing: f.exists, configFile: f, secretFile: secrets, original: original}
	if err := d.CheckUnchanged(); err != nil {
		return nil, err
	}
	return d, nil
}

func (f setupFile) unchanged() error {
	current, err := readSetupFile(f.path)
	if err != nil {
		return err
	}
	if current.mode != f.mode || current.exists != f.exists || current.target != f.target || !bytes.Equal(current.data, f.data) {
		return fmt.Errorf("%s changed during setup — rerun setup to review the current values", filepath.Base(f.path))
	}
	return nil
}

// Call under WithSetupLock, before creating directories or writing any files.
func (d *SetupDraft) CheckUnchanged() error {
	if err := d.configFile.unchanged(); err != nil {
		return err
	}
	return d.secretFile.unchanged()
}

// Preview is also the exact document saved after approval. Patch the YAML tree
// rather than serializing defaults over user settings and comments.
func (d *SetupDraft) Preview(cfg Config) ([]byte, error) {
	if err := validate(cfg); err != nil {
		return nil, err
	}
	next, err := configDocument(cfg)
	if err != nil {
		return nil, err
	}
	if !d.Existing {
		return configfile.Encode(next)
	}
	document, err := configfile.Parse(d.configFile.data)
	if err != nil {
		return nil, err
	}
	if !configfile.Patch(document, d.original, next) {
		return append([]byte(nil), d.configFile.data...), nil
	}
	return configfile.Encode(document)
}

// Save only publishes config.yaml. The caller holds WithSetupLock and saves
// approved secrets first; config remains the first-run completion marker.
func (d *SetupDraft) Save(cfg Config) error {
	if err := d.configFile.unchanged(); err != nil {
		return err
	}
	data, err := d.Preview(cfg)
	if err != nil {
		return err
	}
	if d.Existing && bytes.Equal(data, d.configFile.data) {
		return nil
	}
	return writeSetupFile(d.configFile.target, data, !d.Existing, !d.Existing)
}
