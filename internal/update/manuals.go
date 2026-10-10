package update

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Fixed paths keep the recovery journal from becoming an arbitrary file writer.
var manualPaths = []string{"share/man/man1/radar.1", "share/man/man5/radar-config.5"}

type ManualChange struct {
	Previous string `json:"previous"` // Empty means no previously installed page.
	Next     string `json:"next"`
}

func manualDigest(path string, required bool) (string, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) && !required {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > 1<<20 {
		return "", fmt.Errorf("manual must be a nonempty regular file of at most 1 MiB: %s", path)
	}
	return FileDigest(path)
}

func inspectManuals(prefix, root string) (map[string]ManualChange, error) {
	changes := make(map[string]ManualChange, len(manualPaths))
	for _, path := range manualPaths {
		previous, err := manualDigest(filepath.Join(prefix, path), false)
		if err != nil {
			return nil, err
		}
		next, err := manualDigest(filepath.Join(root, path), true)
		if err != nil {
			return nil, err
		}
		changes[path] = ManualChange{Previous: previous, Next: next}
	}
	return changes, nil
}

func validateManuals(changes map[string]ManualChange) error {
	if len(changes) != len(manualPaths) {
		return errors.New("invalid manual recovery journal")
	}
	for _, path := range manualPaths {
		change, ok := changes[path]
		if !ok || !digestPattern.MatchString(change.Next) || (change.Previous != "" && !digestPattern.MatchString(change.Previous)) {
			return errors.New("invalid manual recovery journal")
		}
	}
	return nil
}

func manualBackup(prefix, path string) string {
	return filepath.Join(prefix, transactionPath, "previous-"+filepath.Base(path))
}

func copyManual(source, destination string) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	dir := filepath.Dir(destination)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".radar-man-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, werr := f.Write(data)
	merr := f.Chmod(0644)
	serr := f.Sync()
	cerr := f.Close()
	if err := errors.Join(werr, merr, serr, cerr); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), destination); err != nil {
		return err
	}
	return syncDir(dir)
}

func (s *Staged) backupManuals() error {
	if err := validateManuals(s.Journal.Manuals); err != nil {
		return err
	}
	for _, path := range manualPaths {
		change := s.Journal.Manuals[path]
		previous, err := manualDigest(filepath.Join(s.Prefix, path), false)
		if err != nil || previous != change.Previous {
			return fmt.Errorf("installed manual changed during download: %s", path)
		}
		next, err := manualDigest(filepath.Join(s.Root, path), true)
		if err != nil || next != change.Next {
			return fmt.Errorf("staged manual changed: %s", path)
		}
		if previous != "" {
			if err := copyManual(filepath.Join(s.Prefix, path), manualBackup(s.Prefix, path)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Staged) activateManuals() error {
	for _, path := range manualPaths {
		if err := copyManual(filepath.Join(s.Root, path), filepath.Join(s.Prefix, path)); err != nil {
			return err
		}
	}
	// Persist newly created directory entries as well as each file replacement.
	for _, path := range []string{"share/man", "share", "."} {
		if err := syncDir(filepath.Join(s.Prefix, path)); err != nil {
			return err
		}
	}
	return nil
}

func recoverManuals(prefix string, changes map[string]ManualChange) error {
	for _, path := range manualPaths {
		change := changes[path]
		target := filepath.Join(prefix, path)
		current, err := manualDigest(target, false)
		if err != nil {
			return err
		}
		if current == change.Previous {
			continue
		}
		if current != change.Next {
			return fmt.Errorf("manual changed after interrupted update; refusing to overwrite %s", path)
		}
		if change.Previous == "" {
			if err := os.Remove(target); err != nil {
				return err
			}
			if err := syncDir(filepath.Dir(target)); err != nil {
				return err
			}
		} else {
			backup := manualBackup(prefix, path)
			if sum, err := manualDigest(backup, true); err != nil || sum != change.Previous {
				return fmt.Errorf("recovery manual unavailable or modified: %s", path)
			}
			if err := copyManual(backup, target); err != nil {
				return err
			}
		}
	}
	return nil
}
