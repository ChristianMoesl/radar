package setup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDirectoryValidation(t *testing.T) {
	if _, err := DirectoryPath("relative/path"); err == nil {
		t.Fatal("accepted relative directory")
	}
	file := filepath.Join(t.TempDir(), "file")
	_ = os.WriteFile(file, nil, 0600)
	if _, err := DirectoryPath(filepath.Join(file, "child")); err == nil {
		t.Fatal("accepted file parent")
	}
}
