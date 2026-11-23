package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"

	"radar/internal/protocol"
)

// Cleanup has a separate exact response boundary: reject private siblings,
// duplicate/folded members, missing required fields and oversized frames before
// projecting anything through the feedback CLI. No raw daemon error is exposed.
func decodeDiagnosticCleanup(raw []byte) (protocol.Response, error) {
	fail := func() (protocol.Response, error) {
		return protocol.Response{}, errors.New("invalid native manual cleanup response; outcome may be unknown, do not replay")
	}
	if len(raw) > 36*1024 {
		return fail()
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var unique func(int) bool
	unique = func(depth int) bool {
		if depth > 16 {
			return false
		}
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		switch token {
		case json.Delim('{'):
			seen := map[string]bool{}
			for decoder.More() {
				token, err := decoder.Token()
				key, ok := token.(string)
				if err != nil || !ok || key != strings.ToLower(key) || seen[key] {
					return false
				}
				seen[key] = true
				if !unique(depth + 1) {
					return false
				}
			}
			end, err := decoder.Token()
			return err == nil && end == json.Delim('}')
		case json.Delim('['):
			for decoder.More() {
				if !unique(depth + 1) {
					return false
				}
			}
			end, err := decoder.Token()
			return err == nil && end == json.Delim(']')
		default:
			return true
		}
	}
	if !unique(0) {
		return fail()
	}
	if _, err := decoder.Token(); err != io.EOF {
		return fail()
	}
	var wire struct {
		OK      bool                        `json:"ok"`
		Error   string                      `json:"error,omitempty"`
		Cleanup *protocol.DiagnosticCleanup `json:"diagnostic_cleanup,omitempty"`
	}
	decoder = json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&wire) != nil {
		return fail()
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return fail()
	}
	var a, b any
	if json.Unmarshal(raw, &a) != nil || json.Unmarshal(encoded, &b) != nil {
		return fail()
	}
	var sameShape func(any, any) bool
	sameShape = func(a, b any) bool {
		switch a := a.(type) {
		case map[string]any:
			b, ok := b.(map[string]any)
			if !ok || len(a) != len(b) {
				return false
			}
			for k, v := range a {
				w, ok := b[k]
				if !ok || !sameShape(v, w) {
					return false
				}
			}
			return true
		case []any:
			b, ok := b.([]any)
			if !ok || len(a) != len(b) {
				return false
			}
			for i := range a {
				if !sameShape(a[i], b[i]) {
					return false
				}
			}
			return true
		default:
			return reflect.DeepEqual(a, b)
		}
	}
	if !sameShape(a, b) || wire.OK != (wire.Cleanup != nil) || wire.OK && wire.Error != "" || !wire.OK && wire.Error == "" {
		return fail()
	}
	if wire.Cleanup != nil {
		v := wire.Cleanup
		req := protocol.DiagnosticMaintenanceRequest{Schema: protocol.DiagnosticsVersion, Operation: "cleanup", Mode: v.Mode, Scope: v.Scope, ParentID: v.RecordID}
		if v.Mode == "apply" {
			req.PlanID = v.PlanID
		}
		if !protocol.ValidDiagnosticCleanup(*v, req) {
			return fail()
		}
	}
	if !wire.OK {
		wire.Error = "native manual cleanup unavailable; outcome may be unknown, do not replay or automatically retry"
	}
	return protocol.Response{OK: wire.OK, Error: wire.Error, DiagnosticCleanup: wire.Cleanup}, nil
}
