package client

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"strings"
)

// Shared exact public-projection boundary: finite size/depth, unique canonical
// keys, no unknown members, and lossless typed roundtrip (required/null fields).
func decodeDiagnosticProjection(raw []byte, wire any) bool {
	if len(raw) > 36*1024 {
		return false
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
		return false
	}
	if _, err := decoder.Token(); err != io.EOF {
		return false
	}
	decoder = json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(wire) != nil {
		return false
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return false
	}
	var a, b any
	if json.Unmarshal(raw, &a) != nil || json.Unmarshal(encoded, &b) != nil {
		return false
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
	return sameShape(a, b)
}
