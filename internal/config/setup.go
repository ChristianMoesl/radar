package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
)

// SetupDraft retains the original document so setup changes only fields the
// user edits, including when the document contains settings unknown to this UI.
// It also detects edits to either file while questions are being answered.
type SetupDraft struct {
	Config                 Config
	Existing               bool
	configFile, secretFile setupFile
	original               map[string]any
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
		var object map[string]json.RawMessage
		if json.Unmarshal(f.data, &object) != nil || object == nil {
			return nil, fmt.Errorf("config.json must contain a valid JSON object — repair it before running setup")
		}
	}
	cfg, err := Load()
	if err != nil {
		return nil, fmt.Errorf("config.json is invalid — repair it before running setup: %w", err)
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
	d := &SetupDraft{Config: cfg, Existing: f.exists, configFile: f, secretFile: secrets, original: configObject(cfg)}
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

func configObject(cfg Config) map[string]any {
	data, _ := json.Marshal(cfg)
	var object map[string]any
	_ = json.Unmarshal(data, &object)
	return object
}

// Preview is also the exact document saved after approval. Apply the typed
// delta to the original JSON, rather than serializing defaults over user data.
func (d *SetupDraft) Preview(cfg Config) ([]byte, error) {
	if err := validate(cfg); err != nil {
		return nil, err
	}
	next := configObject(cfg)
	if d.Existing {
		var document map[string]any
		decoder := json.NewDecoder(bytes.NewReader(d.configFile.data))
		decoder.UseNumber()
		if err := decoder.Decode(&document); err != nil {
			return nil, err
		}
		patchSetupObject(document, d.original, next)
		next = document
	}
	data, err := json.MarshalIndent(next, "", "  ")
	return append(data, '\n'), err
}

func patchSetupObject(document, before, after map[string]any) {
	for key := range before {
		if _, ok := after[key]; !ok {
			delete(document, key)
		}
	}
	for key, value := range after {
		old, ok := before[key]
		if ok && reflect.DeepEqual(old, value) {
			continue
		}
		oldObject, oldOK := old.(map[string]any)
		newObject, newOK := value.(map[string]any)
		existing, existingOK := document[key].(map[string]any)
		if oldOK && newOK && existingOK {
			patchSetupObject(existing, oldObject, newObject)
		} else {
			document[key] = value
		}
	}
}

// Save only publishes config.json. The caller holds WithSetupLock and saves
// approved secrets first; config remains the first-run completion marker.
func (d *SetupDraft) Save(cfg Config) error {
	if err := d.configFile.unchanged(); err != nil {
		return err
	}
	data, err := d.Preview(cfg)
	if err != nil {
		return err
	}
	return writeSetupFile(d.configFile.target, data, !d.Existing, !d.Existing)
}
