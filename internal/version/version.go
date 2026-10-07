package version

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
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
		sum, err := executableDigest(path)
		if err != nil {
			return
		}
		current = Number + "+" + sum
	})
	return current
}

func Text() string {
	return fmt.Sprintf("radar %s\ncommit %s\nbuilt %s", Number, Commit, Date)
}

// CheckInstalled prevents a resident old client from mutating state or restarting
// an old daemon after the executable at its path has been atomically replaced.
func CheckInstalled() error {
	loaded := Current()
	path, err := os.Executable()
	if err != nil {
		return err
	}
	sum, err := executableDigest(path)
	if err != nil {
		return err
	}
	if loaded != Number+"+"+sum {
		return fmt.Errorf("Radar was replaced; reopen Radar to use the installed version")
	}
	return nil
}

func executableDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
