package sbx

import (
	"path/filepath"
	"strings"

	"radar/internal/protocol"
	"radar/internal/integration/workspace"
)

const OpenShellAction = "sbx_shell"

type OpenShellOptions struct {
	SessionTarget string
	SwitchClient  bool
}

type OpenShellResult struct {
	SessionName    string
	CreatedSession bool
}

func IsSandboxRef(ref protocol.SourceRef) bool {
	return ref.Source == "sbx" && ref.Kind == "sandbox"
}

func SandboxName(ref protocol.SourceRef) string {
	if !IsSandboxRef(ref) {
		return ""
	}
	if name := strings.TrimSpace(ref.Metadata["name"]); name != "" {
		return name
	}
	if name := strings.TrimSpace(ref.Title); name != "" {
		return name
	}
	return strings.TrimPrefix(ref.ID, "sbx:sandbox:")
}

func SandboxSessionName(ref protocol.SourceRef) string {
	path := strings.TrimSpace(ref.Path)
	if path != "" {
		return workspace.SessionName(filepath.Base(filepath.Dir(path)), filepath.Base(path))
	}
	return workspace.WorktreeName(SandboxName(ref))
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
