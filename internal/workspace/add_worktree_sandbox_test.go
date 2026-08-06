package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/integration"
	"radar/internal/workspacegroup"
)

func TestAddWorktreeSandboxFailureIsRetryable(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	ctx := context.Background()
	tmp := t.TempDir()
	root := filepath.Join(tmp, "workspaces")
	primaryRepo := filepath.Join(tmp, "primary-source")
	targetRepo := filepath.Join(tmp, "target-source")
	initRepository(t, ctx, primaryRepo)
	initRepository(t, ctx, targetRepo)
	runGitE2E(t, ctx, targetRepo, "branch", "feature/retry")
	primaryPath := filepath.Join(root, "primary", "RAD-8-retry")
	runGitE2E(t, ctx, primaryRepo, "worktree", "add", "-b", "RAD-8-retry", primaryPath, "HEAD")
	commonDir := strings.TrimSpace(gitOutputE2E(t, ctx, primaryPath, "rev-parse", "--path-format=absolute", "--git-common-dir"))
	group := workspacegroup.Workspace{
		ID: workspacegroup.ID(primaryPath), Name: "RAD-8-retry", PrimaryPath: primaryPath,
		Sandbox: &workspacegroup.Sandbox{Name: "radar-retry", Agent: "shell", Mounts: []string{primaryPath, commonDir}},
		Members: []workspacegroup.Member{{Repository: primaryRepo, Path: primaryPath, Branch: "RAD-8-retry", Primary: true, SetupScheduled: true}},
	}
	if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
		t.Fatal(err)
	}

	bin := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(tmp, "sbx-state.json")
	initial := `{"sandboxes":[{"name":"radar-retry","agent":"shell","status":"running","workspaces":[` + quoteJSON(primaryPath) + `,` + quoteJSON(commonDir) + `]}]}`
	if err := os.WriteFile(statePath, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
set -eu
case "$1" in
  ls) if [ -f "$RADAR_SBX_STATE" ]; then cat "$RADAR_SBX_STATE"; else printf '{"sandboxes":[]}\n'; fi ;;
  rm) rm -f "$RADAR_SBX_STATE" ;;
  create)
    if [ "${RADAR_SBX_ALLOW_CREATE:-}" != 1 ]; then echo 'create failed' >&2; exit 1; fi
    shift; [ "$1" = --name ]; name=$2; shift 2; agent=$1; shift
    python3 - "$RADAR_SBX_STATE" "$name" "$agent" "$@" <<'PY'
import json,sys
path,name,agent,*mounts=sys.argv[1:]
with open(path,'w') as f: json.dump({'sandboxes':[{'name':name,'agent':agent,'status':'running','workspaces':mounts}]},f)
PY
    ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "sbx"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RADAR_SBX_STATE", statePath)
	request := AddWorktreeRequest{Workspace: primaryPath, Repository: targetRepo, BranchMode: integration.WorkspaceBranchExisting, Branch: "feature/retry", WorkspaceRoot: root}
	partial, err := ApplyAddWorktree(ctx, ExecRunner{}, request)
	if err != nil {
		t.Fatal(err)
	}
	if partial.OK || !partial.WorktreeCreated || !partial.WorkspaceMembershipSaved || partial.SandboxReconciled || !partial.Retryable {
		t.Fatalf("partial = %+v", partial)
	}

	t.Setenv("RADAR_SBX_ALLOW_CREATE", "1")
	retry, err := ApplyAddWorktree(ctx, ExecRunner{}, request)
	if err != nil {
		t.Fatal(err)
	}
	if !retry.OK || !retry.WorktreeCreated || !retry.WorkspaceMembershipSaved || !retry.SandboxReconciled {
		t.Fatalf("retry = %+v", retry)
	}
}

func quoteJSON(value string) string {
	result := `"`
	for _, char := range value {
		switch char {
		case '\\', '"':
			result += `\` + string(char)
		default:
			result += string(char)
		}
	}
	return result + `"`
}
