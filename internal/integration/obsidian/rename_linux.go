package obsidian

import "golang.org/x/sys/unix"

// renameNote moves a note atomically without replacing any existing destination.
func renameNote(source, destination string) error {
	return unix.Renameat2(unix.AT_FDCWD, source, unix.AT_FDCWD, destination, unix.RENAME_NOREPLACE)
}
