package workspacegroup

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sys/unix"

	"radar/internal/config"
)

func DefaultRoot() (string, error) {
	cfg, err := config.Load()
	if err != nil {
		return "", err
	}
	root := cfg.Workspace.RootDir
	if root == "~" || strings.HasPrefix(root, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		root = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(root, "~"), "/"))
	}
	return filepath.Clean(root), nil
}

var noteMu sync.Mutex

// WithNoteLock serializes note relocation with workspace creation. The registry
// lock remains separate so callbacks can load and update registered workspaces.
func WithNoteLock(root string, run func() error) error {
	noteMu.Lock()
	defer noteMu.Unlock()
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(root, ".radar-notes.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return err
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	return run()
}
