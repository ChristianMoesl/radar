package client

import (
	"context"
	"fmt"
	"strings"
)

// Runtime is the identity and ownership evidence required for managed cleanup.
type Runtime struct {
	Name       string   `json:"name"`
	ID         string   `json:"id"`
	Status     string   `json:"status"`
	Workspaces []string `json:"workspaces"`
	Mounts     []string `json:"mounts"`
}

// FindRuntime returns nil only after a complete, identifiable listing proves
// absence. Malformed or ambiguous listings must never allow ownership removal.
func (c *Client) FindRuntime(ctx context.Context, name string) (*Runtime, error) {
	return c.findRuntime(ctx, name, "")
}

// FindRuntimeIdentity additionally refuses absence when the previewed lifetime
// survives under another name. A reusable name alone cannot prove removal.
// Full listing identities must be unique; ambiguity is not absence evidence.
func (c *Client) FindRuntimeIdentity(ctx context.Context, name, id string) (*Runtime, error) {
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("registered sandbox has no previewed runtime identity")
	}
	return c.findRuntime(ctx, name, id)
}

func (c *Client) findRuntime(ctx context.Context, name, id string) (*Runtime, error) {
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("registered sandbox has no runtime identity")
	}
	output, err := c.Run(ctx, "", "ls", "--json")
	if err != nil {
		return nil, err
	}
	rows, err := DecodeList[Runtime](output)
	if err != nil {
		return nil, err
	}
	var found *Runtime
	names, ids := map[string]bool{}, map[string]bool{}
	for _, row := range rows {
		if strings.TrimSpace(row.Name) == "" || strings.TrimSpace(row.ID) == "" {
			return nil, fmt.Errorf("sandbox listing has an unverifiable runtime identity")
		}
		if id != "" {
			if names[row.Name] || ids[row.ID] {
				return nil, fmt.Errorf("sandbox listing has ambiguous runtime identities")
			}
			names[row.Name], ids[row.ID] = true, true
			if row.ID == id && row.Name != name {
				return nil, fmt.Errorf("previewed sandbox lifetime remains under a different name; inspect before retrying")
			}
		}
		if row.Name != name {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("sandbox runtime identity is ambiguous")
		}
		copy := row
		found = &copy
	}
	return found, nil
}
