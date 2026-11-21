package server

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"strings"

	"radar/internal/protocol"
)

// encoding/json accepts repeated members, merges repeated objects and matches
// struct fields case-insensitively. Reject that ambiguity before routing: a
// repeated method must not escape a guarded handler by selecting another route.
// Keep provider-owned map keys case-sensitive, as in the native decoder.
func uniqueRequestMembers(raw []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if !uniqueJSONValue(decoder, reflect.TypeFor[protocol.Request](), 0) {
		return false
	}
	_, err := decoder.Token()
	return err == io.EOF
}

func uniqueJSONValue(decoder *json.Decoder, typ reflect.Type, depth int) bool {
	// Native request shapes are shallow. Bound traversal of ignored/opaque
	// fields too; malformed frames must not become a recursive work budget.
	if depth > 64 {
		return false
	}
	for typ != nil && typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
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
			if err != nil || !ok {
				return false
			}
			var child reflect.Type
			if typ != nil {
				switch typ.Kind() {
				case reflect.Struct:
					// Exact match takes precedence, just as encoding/json does.
					// EqualFold also catches escaped Unicode simple-fold aliases.
					for _, exact := range []bool{true, false} {
						for i := 0; i < typ.NumField(); i++ {
							field := typ.Field(i)
							name := strings.SplitN(field.Tag.Get("json"), ",", 2)[0]
							if field.PkgPath != "" || name == "-" {
								continue
							}
							if name == "" {
								name = field.Name
							}
							if (exact && key == name) || (!exact && strings.EqualFold(key, name)) {
								key, child = name, field.Type
								break
							}
						}
						if child != nil {
							break
						}
					}
				case reflect.Map:
					child = typ.Elem()
				}
			}
			if seen[key] {
				return false
			}
			seen[key] = true
			if !uniqueJSONValue(decoder, child, depth+1) {
				return false
			}
		}
		end, err := decoder.Token()
		return err == nil && end == json.Delim('}')
	case json.Delim('['):
		var child reflect.Type
		if typ != nil && (typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array) {
			child = typ.Elem()
		}
		for decoder.More() {
			if !uniqueJSONValue(decoder, child, depth+1) {
				return false
			}
		}
		end, err := decoder.Token()
		return err == nil && end == json.Delim(']')
	default:
		return true
	}
}
