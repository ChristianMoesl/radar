//go:build !darwin && !linux

package git

import "errors"

func gitFileIdentity(path string) (string, error) {
	return "", errors.New("concrete Git binding identity is unavailable on this platform")
}
