package agentservice

import (
	"fmt"
	"path/filepath"
	"strings"
)

// WorkspaceRemovalOwners observes registrations without probing/starting runners
// or reading conversations, tokens, schedule payloads or event data. A stopped
// registration pins its workspace just as a running registration does.
func WorkspaceRemovalOwners(paths []string) (map[string][]string, error) {
	regs, err := registrations()
	if err != nil {
		return nil, err
	}
	owners := make(map[string][]string, len(paths))
	for _, path := range paths {
		candidate, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		owners[path] = []string{}
		for _, reg := range regs {
			if registrationWithinWorkspace(reg, candidate) {
				owners[path] = append(owners[path], reg.ID)
			}
		}
	}
	return owners, nil
}

func registrationWithinWorkspace(reg Registration, path string) bool {
	relative, err := filepath.Rel(path, reg.Workspace.Path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

// Registration pins its workspace even when intentionally stopped. Remove the
// registration explicitly to permit cleanup; the durable store is retained.
func CheckWorkspaceRemoval(path string) error {
	owners, err := WorkspaceRemovalOwners([]string{path})
	if err != nil {
		return err
	}
	if len(owners[path]) > 0 {
		id := owners[path][0]
		return fmt.Errorf("workspace is associated with agent %s; stop it and run radar agent remove %s before cleanup (durable data is retained)", id, id)
	}
	return nil
}
