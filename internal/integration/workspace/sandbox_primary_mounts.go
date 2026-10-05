package workspace

import (
	"fmt"
	"path/filepath"
	"strings"
)

// sandboxPrimaryMounts orders CLI arguments without changing the persisted,
// canonical mount set. SBX treats the first path as its writable primary
// workspace; mount normalization sorts paths and can remove covered children.
func sandboxPrimaryMounts(primaryWorkspace string, mounts []string) ([]string, error) {
	primary := filepath.Clean(primaryWorkspace)
	if !filepath.IsAbs(primary) {
		return nil, fmt.Errorf("SBX primary workspace must be absolute")
	}
	type mount struct {
		path     string
		readOnly bool
	}
	parsed := make([]mount, 0, len(mounts))
	coverDepth := -1
	coverReadOnly, coverConflict := false, false
	for _, argument := range mounts {
		argument = strings.TrimSpace(argument)
		if argument == "" {
			continue
		}
		readOnly := strings.HasSuffix(argument, ":ro")
		path := filepath.Clean(strings.TrimSuffix(argument, ":ro"))
		if !filepath.IsAbs(path) {
			return nil, fmt.Errorf("SBX workspace mounts must be absolute")
		}
		if path == primary && readOnly {
			return nil, fmt.Errorf("SBX primary workspace must be writable; remove its conflicting read-only mount")
		}
		parsed = append(parsed, mount{path: path, readOnly: readOnly})
		if !pathContains(path, primary) {
			continue
		}
		// A more-specific mount determines effective access beneath an ancestor.
		depth := len(path)
		if depth > coverDepth {
			coverDepth, coverReadOnly, coverConflict = depth, readOnly, false
		} else if depth == coverDepth && coverReadOnly != readOnly {
			coverConflict = true
		}
	}
	if coverDepth < 0 {
		return nil, fmt.Errorf("SBX primary workspace is not covered by a writable mount")
	}
	if coverReadOnly || coverConflict {
		return nil, fmt.Errorf("SBX primary workspace is covered by read-only or conflicting mount permissions")
	}

	ordered := make([]string, 1, len(parsed)+1)
	ordered[0] = primary
	for _, candidate := range parsed {
		if candidate.path == primary {
			continue
		}
		argument := candidate.path
		if candidate.readOnly {
			argument += ":ro"
		}
		ordered = append(ordered, argument)
	}
	return ordered, nil
}
