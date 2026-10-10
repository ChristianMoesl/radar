package pi

import (
	_ "embed"
	"errors"
	"os"
	"path/filepath"
)

// DefaultInstructions is also shipped by the source and archive installers.
// Embedding it lets package-manager installs create user files during setup,
// after consent, rather than from a Homebrew installation hook.
//
//go:embed default-AGENTS.md
var DefaultInstructions string

// InstallInstructions publishes the defaults only if no file or symlink exists.
// A concurrent user edit wins; existing instructions are never replaced.
func InstallInstructions(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".AGENTS.md-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.WriteString(DefaultInstructions)
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return err
	}
	if err := os.Link(file.Name(), path); err != nil && !os.IsExist(err) {
		return err
	}
	return nil
}
