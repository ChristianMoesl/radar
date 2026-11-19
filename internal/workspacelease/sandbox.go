package workspacelease

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"

	"radar/internal/socket"
)

// AcquireSandbox coordinates use of the reusable SBX name, including when its
// runtime is absent. Workspace paths alone cannot identify a shared sandbox.
// Keep this namespace distinct from canonical workspace-path hashes.
func AcquireSandbox(name string, exclusive bool) (func(), error) {
	return acquireSandbox(name, exclusive, "", "sandbox")
}

func acquireSandbox(name string, exclusive bool, prefix, kind string) (func(), error) {
	name = strings.TrimSpace(name)
	if name == "" || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return nil, fmt.Errorf("sandbox name is required and must not contain control characters")
	}
	base, err := socket.Path()
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(name))
	return acquire(filepath.Join(filepath.Dir(base), "leases"), prefix+"sbx-"+hex.EncodeToString(sum[:])+".lock", exclusive, kind, name)
}
