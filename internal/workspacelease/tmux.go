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

// AcquireTmux coordinates a reusable name or a resolved runtime incarnation.
// Callers prefix the identity with name: or runtime:; these must never alias
// each other, workspace paths, or SBX names.
func AcquireTmux(identity string, exclusive bool) (func(), error) {
	if strings.TrimSpace(identity) == "" || strings.IndexFunc(identity, unicode.IsControl) >= 0 {
		return nil, fmt.Errorf("tmux session identity is required and must not contain control characters")
	}
	base, err := socket.Path()
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(identity))
	return acquire(filepath.Join(filepath.Dir(base), "leases"), "tmux-"+hex.EncodeToString(sum[:])+".lock", exclusive, "tmux session", identity)
}
