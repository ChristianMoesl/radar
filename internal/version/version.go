package version

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sync"
)

var (
	Number = "dev"
	Commit = "unknown"
	Date   = "unknown"

	currentOnce sync.Once
	current     string
)

func Current() string {
	currentOnce.Do(func() {
		current = Number
		path, err := os.Executable()
		if err != nil {
			return
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return
		}
		sum := sha256.Sum256(data)
		current = Number + "+" + hex.EncodeToString(sum[:])
	})
	return current
}

func Text() string {
	return fmt.Sprintf("radar %s\ncommit %s\nbuilt %s", Number, Commit, Date)
}
