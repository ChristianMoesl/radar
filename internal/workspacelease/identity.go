package workspacelease

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CanonicalPath identifies the physical resource, including a missing suffix
// below its nearest existing parent. Do not fall back to a lexical alias when
// resolution fails: a dangling link or inaccessible parent is not a new path.
func CanonicalPath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("workspace path is required")
	}
	if !filepath.IsAbs(path) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		// Abs/Join would clean symlink/.. before the filesystem resolves it.
		path = cwd + string(filepath.Separator) + path
	}
	path = strings.TrimRight(path, string(filepath.Separator))
	if path == "" {
		path = string(filepath.Separator)
	}
	var suffix []string
	for {
		physical, err := filepath.EvalSymlinks(path)
		if err == nil {
			return filepath.Join(append([]string{physical}, suffix...)...), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		// An existing symlink whose target is missing must fail, not be
		// stripped as if it were an ordinary not-yet-created component.
		if _, statErr := os.Lstat(path); statErr == nil || !errors.Is(statErr, os.ErrNotExist) {
			return "", err
		}
		index := strings.LastIndexByte(path, byte(filepath.Separator))
		if index < 0 || path == string(filepath.Separator) {
			return "", err
		}
		name := path[index+1:]
		if name == "." || name == ".." {
			return "", fmt.Errorf("cannot resolve traversal through a missing workspace path")
		}
		suffix = append([]string{name}, suffix...)
		path = path[:index]
		if path == "" {
			path = string(filepath.Separator)
		}
	}
}
