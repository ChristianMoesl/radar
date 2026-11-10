package sbx

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	sbxclient "radar/internal/integration/sbx/client"
	"radar/internal/integration/workspace"
	"radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

func retireSandbox(ctx context.Context, runner workspace.Runner, target protocol.CleanupTarget) (protocol.CleanupTarget, error) {
	delete(target.Operation, "runtime_retired")
	root, err := workspace.DefaultRoot()
	if err != nil {
		return target, err
	}
	err = workspacegroup.WithNoteLock(root, func() error {
		if _, err := workspace.ValidateRuntimeCleanupTarget(root, target); err != nil {
			return err
		}
		client := sbxclient.New(runner)
		output, err := client.Run(ctx, "", "ls", "--json")
		if err != nil {
			return err
		}
		rows, err := sbxclient.DecodeList[struct {
			Name       string   `json:"name"`
			ID         string   `json:"id"`
			Status     string   `json:"status"`
			Workspaces []string `json:"workspaces"`
			Mounts     []string `json:"mounts"`
		}](output)
		if err != nil {
			return err
		}
		matches := 0
		for _, row := range rows {
			if strings.TrimSpace(row.Name) == "" || strings.TrimSpace(row.ID) == "" {
				return fmt.Errorf("sandbox listing has an unverifiable runtime identity")
			}
			if row.Name == target.ResourceID {
				matches++
			}
		}
		if matches > 1 {
			return fmt.Errorf("sandbox runtime identity is ambiguous")
		}
		for _, row := range rows {
			if row.Name != target.ResourceID {
				continue
			}
			if row.ID == "" {
				return fmt.Errorf("sandbox runtime has no stable identity")
			}
			if expected := target.Operation["runtime_id"]; expected != "" && row.ID != expected {
				return fmt.Errorf("sandbox runtime identity changed; preview cleanup again")
			}
			owned := false
			for _, mount := range append(row.Workspaces, row.Mounts...) {
				if !strings.HasSuffix(mount, ":ro") && filepath.Clean(mount) == filepath.Clean(target.Path) {
					owned = true
				}
			}
			if !owned {
				return fmt.Errorf("sandbox runtime no longer mounts its registered workspace read/write")
			}
			switch strings.ToLower(row.Status) {
			case "stopped":
				return nil
			case "running":
				_, err := client.Run(ctx, "", "stop", target.ResourceID)
				if err == nil {
					target.Operation["runtime_retired"] = "true"
				}
				return err
			default:
				return fmt.Errorf("sandbox runtime state %q cannot be safely retired", row.Status)
			}
		}
		return nil // Already missing; retain its recorded configuration for recovery.
	})
	return target, err
}
