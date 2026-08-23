package pathdisplay

import (
	"os"
	"strings"
)

// HomeRelative shortens a path inside the current user's home directory for
// display. It does not clean, resolve, or otherwise change paths outside home.
func HomeRelative(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if strings.HasPrefix(path, home+string(os.PathSeparator)) {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}

// HomeRelativeSuffix shortens path when it is the suffix of a display value,
// such as a path-based source reference ID. The identity itself remains
// untouched; callers use the returned string for presentation only.
func HomeRelativeSuffix(value string, path string) string {
	shortened := HomeRelative(path)
	if shortened == path || !strings.HasSuffix(value, path) {
		return value
	}
	return strings.TrimSuffix(value, path) + shortened
}
