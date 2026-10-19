package sbx

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"radar/internal/config"
	"radar/internal/setup"
	sandboxkit "radar/sandbox"
)

type sandboxSetupFile struct {
	path, purpose string
	data          []byte
	exists        bool
}

type sandboxSetupPlan struct {
	root  string
	files []sandboxSetupFile
}

func hasSandboxRecipe(draft *config.SetupDraft) bool {
	for _, key := range []string{"kit", "env_file", "ready_command", "additional_mounts"} {
		if draft.HasSetting("sbx", key) {
			return true
		}
	}
	return false
}

func planSandboxFiles(configPath string, cfg *config.SBXConfig) (*sandboxSetupPlan, error) {
	root, err := filepath.Abs(filepath.Join(filepath.Dir(configPath), "sandbox"))
	if err != nil {
		return nil, err
	}
	// Env-file values must fit on one line. Spaces are supported by SBX.
	if strings.ContainsAny(root, "\r\n\x00") {
		return nil, fmt.Errorf("Radar config directory cannot contain line breaks for the sandbox starter")
	}
	p := &sandboxSetupPlan{root: root}
	for _, f := range []sandboxSetupFile{
		{path: filepath.Join(root, "spec.yaml"), purpose: "Editable sandbox kit", data: []byte(sandboxkit.KitSpec)},
		{path: filepath.Join(root, "sandbox.env"), purpose: "Sandbox startup directory", data: []byte("SBX_STARTUP_DIR=" + filepath.Join(root, "startup.d") + "\n")},
		{path: filepath.Join(root, "startup.d", "README.md"), purpose: "Startup script instructions", data: []byte(sandboxkit.StartupReadme)},
	} {
		info, err := os.Lstat(f.path)
		if err == nil {
			if !info.Mode().IsRegular() {
				return nil, fmt.Errorf("starter file %s is not a regular file; left unchanged", f.path)
			}
			f.data, err = os.ReadFile(f.path)
			f.exists = true
		}
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		p.files = append(p.files, f)
	}
	cfg.Kit = config.SBXKitConfig{Name: root}
	cfg.EnvFile = filepath.Join(root, "sandbox.env")
	cfg.AdditionalMounts = []string{filepath.Join(root, "startup.d") + ":ro"}
	cfg.ReadyCommand = []string{"sandbox-startup", "run"}
	return p, nil
}

func (p *sandboxSetupPlan) checkUnchanged() error {
	if p == nil {
		return nil
	}
	for _, f := range p.files {
		info, err := os.Lstat(f.path)
		if !f.exists && os.IsNotExist(err) {
			continue
		}
		if err != nil || !f.exists || !info.Mode().IsRegular() {
			return fmt.Errorf("sandbox starter changed during setup — rerun `radar setup`; no files were overwritten")
		}
		data, err := os.ReadFile(f.path)
		if err != nil || !bytes.Equal(data, f.data) {
			return fmt.Errorf("sandbox starter changed during setup — rerun `radar setup`; no files were overwritten")
		}
	}
	return nil
}

func (p *sandboxSetupPlan) apply() error {
	if err := p.checkUnchanged(); err != nil {
		return err
	}
	if p == nil {
		return nil
	}
	for _, f := range p.files {
		if f.exists {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(f.path), 0700); err != nil {
			return err
		}
		// Never truncate a file that appeared after review, even outside setup's lock.
		out, err := os.OpenFile(f.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return fmt.Errorf("create sandbox starter (existing files are never overwritten): %w", err)
		}
		_, writeErr := out.Write(f.data)
		closeErr := out.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func (p *sandboxSetupPlan) reviewFiles() []setup.File {
	if p == nil {
		return nil
	}
	var result []setup.File
	for _, f := range p.files {
		item := setup.File{Path: f.path, Purpose: f.purpose, Action: "CREATE", Details: string(f.data)}
		if f.exists {
			// In particular, do not reveal values a user added to sandbox.env.
			item.Action, item.Details = "UNCHANGED", "Existing user-owned file is preserved; contents are not displayed."
		}
		result = append(result, item)
	}
	return result
}

// stage prepares only missing starter files outside the config directory. The
// disposable probe can run before final review without publishing user config.
// Existing custom kits/env-files are used in place, not copied or interpreted.
func (p *sandboxSetupPlan) stage(temp string, cfg config.SBXConfig) (config.SBXConfig, error) {
	if p == nil {
		return cfg, nil
	}
	if err := p.checkUnchanged(); err != nil {
		return cfg, err
	}
	cfg.AdditionalMounts = append([]string(nil), cfg.AdditionalMounts...)
	startup := filepath.Join(p.root, "startup.d")
	if _, err := os.Stat(startup); os.IsNotExist(err) {
		startup = filepath.Join(temp, "startup.d")
		if err := os.Mkdir(startup, 0700); err != nil {
			return cfg, err
		}
		cfg.AdditionalMounts = []string{startup + ":ro"}
	} else if err != nil {
		return cfg, err
	}
	for _, f := range p.files {
		if f.exists {
			continue
		}
		var target string
		data := f.data
		switch filepath.Base(f.path) {
		case "spec.yaml":
			cfg.Kit.Name = filepath.Join(temp, "kit")
			target = filepath.Join(cfg.Kit.Name, "spec.yaml")
		case "sandbox.env":
			cfg.EnvFile = filepath.Join(temp, "sandbox.env")
			target, data = cfg.EnvFile, []byte("SBX_STARTUP_DIR="+startup+"\n")
		default:
			continue // README is non-executable and not required for a probe.
		}
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return cfg, err
		}
		if err := os.WriteFile(target, data, 0600); err != nil {
			return cfg, err
		}
	}
	return cfg, nil
}
