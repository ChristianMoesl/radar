package client

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// DecodeList implements the SBX top-level array contract. null and legacy envelopes
// are errors, not empty inventories: callers must not infer resource absence.
func DecodeList[T any](output string) ([]T, error) {
	data := bytes.TrimSpace([]byte(output))
	if len(data) == 0 || data[0] != '[' {
		return nil, fmt.Errorf("unexpected sbx list output: expected an array")
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("unexpected sbx list output: %w", err)
	}
	for _, row := range raw {
		if trimmed := bytes.TrimSpace(row); len(trimmed) == 0 || trimmed[0] != '{' {
			return nil, fmt.Errorf("unexpected sbx list row: expected an object")
		}
	}
	var rows []T
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, fmt.Errorf("unexpected sbx list output: %w", err)
	}
	return rows, nil
}

// ValidateVersion enforces the minimum stable CLI version without an upper bound.
// Callers validate command response schemas separately.
func ValidateVersion(output string) error {
	var version struct {
		Client struct {
			Version string `json:"version"`
		} `json:"client"`
	}
	if err := json.Unmarshal([]byte(output), &version); err != nil {
		return fmt.Errorf("unexpected sbx version output: %w", err)
	}
	var major, minor, patch int
	if n, err := fmt.Sscanf(version.Client.Version, "v%d.%d.%d", &major, &minor, &patch); err != nil || n != 3 || major < 0 || minor < 0 || patch < 0 || (major == 0 && minor < 48) || version.Client.Version != fmt.Sprintf("v%d.%d.%d", major, minor, patch) {
		return fmt.Errorf("unsupported SBX version %q: Radar requires SBX 0.48.0 or newer", version.Client.Version)
	}
	return nil
}
