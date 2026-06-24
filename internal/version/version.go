package version

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"sync"

	"radar/internal/protocol"
)

var (
	currentOnce sync.Once
	current     string
)

func Current() string {
	currentOnce.Do(func() {
		current = protocol.Version
		path, err := os.Executable()
		if err != nil {
			return
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return
		}
		sum := sha256.Sum256(data)
		current = protocol.Version + "+" + hex.EncodeToString(sum[:])
	})
	return current
}
