package agentservice

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Registration pins its workspace even when intentionally stopped. Remove the
// registration explicitly to permit cleanup; the durable store is retained.
func CheckWorkspaceRemoval(path string) error {
	regs, err := registrations()
	if err != nil {
		return err
	}
	candidate, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	for _, reg := range regs {
		relative, err := filepath.Rel(candidate, reg.Workspace.Path)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative) {
			return fmt.Errorf("workspace is associated with agent %s; stop it and run radar agent remove %s before cleanup (durable data is retained)", reg.ID, reg.ID)
		}
	}
	return nil
}
