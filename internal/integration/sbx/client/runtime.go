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
	for _, row := range rows {
		if strings.TrimSpace(row.Name) == "" || strings.TrimSpace(row.ID) == "" {
			return nil, fmt.Errorf("sandbox listing has an unverifiable runtime identity")
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
