package server

import (
	"bytes"
	"encoding/json"
)

// Unlike general legacy probes, this new explicit native planner has an exact
// envelope/selector allowlist. Even empty/null unsupported selectors are denied.
func diagnosticRecreationPreviewFields(raw []byte) bool {
	var frame map[string]json.RawMessage
	if json.Unmarshal(raw, &frame) != nil || len(frame) != 2 {
		return false
	}
	if _, ok := frame["method"]; !ok {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(frame["diagnostics"], &fields) != nil {
		return false
	}
	for key, value := range fields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return false
		}
		switch key {
		case "section", "parent_id", "workspace", "limit":
		default:
			return false
		}
	}
	return true
}
