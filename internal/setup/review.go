package setup

import (
	"bytes"
	"os"

	"github.com/aymanbagabas/go-udiff"

	"radar/internal/pathdisplay"
)

func ReviewFileChange(path, purpose string, after []byte) (File, error) {
	file := File{Path: path, Purpose: purpose}
	before, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		file.Action, file.Details = "CREATE", string(after)
	case err != nil:
		return file, err
	case bytes.Equal(before, after):
		file.Action, file.Details = "UNCHANGED", "Existing file is preserved."
	default:
		file.Action = "UPDATE"
		label := pathdisplay.HomeRelative(path)
		file.Details = udiff.Unified(label+" (current)", label+" (proposed)", string(before), string(after))
	}
	return file, nil
}
