package agentservice

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"radar/internal/protocol"
)

// Validate only the selected native object boundary, not arbitrary private
// result/extension subtrees. Decoded keys catch escaped duplicates; exact known
// names prevent Go's case-folded struct matching from choosing evidence. Unknown
// private extensions remain valid and are never included in diagnostic output.
func agentResponseObject(raw []byte, known ...string) (map[string]json.RawMessage, error) {
	invalid := func() (map[string]json.RawMessage, error) {
		return nil, errors.New("invalid agent response object")
	}
	if len(raw) == 0 || len(raw) > protocol.AgentMaxFrameBytes {
		return invalid()
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return invalid()
	}
	members := make(map[string]json.RawMessage)
	for decoder.More() {
		if len(members) >= 64 {
			return invalid()
		}
		token, err := decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok {
			return invalid()
		}
		if _, duplicate := members[name]; duplicate {
			return invalid()
		}
		for _, canonical := range known {
			if name != canonical && strings.EqualFold(name, canonical) {
				return invalid()
			}
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return invalid()
		}
		members[name] = value
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') {
		return invalid()
	}
	if _, err := decoder.Token(); err != io.EOF {
		return invalid()
	}
	return members, nil
}
