package workspace

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

// Native metadata must not hide safety evidence behind encoding/json's
// last-member-wins or case-insensitive struct-field matching. Validate each
// interpreted object separately; unknown metadata is not an execution contract.
func unambiguousRecreationMetadataObject(raw []byte, fields ...string) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return false
	}
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok || seen[name] {
			return false
		}
		seen[name] = true
		for _, field := range fields {
			if name != field && strings.EqualFold(name, field) {
				return false
			}
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return false
		}
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') {
		return false
	}
	_, err = decoder.Token()
	return err == io.EOF
}
