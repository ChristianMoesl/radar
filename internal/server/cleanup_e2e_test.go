package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/integration/sbx"
	"radar/internal/protocol"
	"radar/internal/state"
)

func TestCleanupSbxSandboxE2EUsesForceWithoutWorkspaceCWD(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("RADAR_STATE", filepath.Join(tmp, "state", "tasks.json"))

	logPath := filepath.Join(tmp, "sbx.log")
	installFakeSbx(t, tmp, logPath)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := state.NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}
	missingWorkspace := filepath.Join(tmp, "missing-workspace")
	store.SetTasks([]protocol.Task{{
		Title: "sandbox-conn-test",
		SourceRefs: []protocol.SourceRef{{
			ID:     "sbx:sandbox:sandbox-conn-test",
			Source: "sbx",
			Kind:   "sandbox",
			Title:  "sandbox-conn-test",
			Path:   missingWorkspace,
			Metadata: map[string]string{
				"name": "sandbox-conn-test",
			},
		}},
	}})

	serverConn, clientConn := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		New(store, logger, nil, nil, nil, cleanup.New([]integration.CleanupProvider{sbx.NewSource()})).handle(serverConn)
	}()

	encoder := json.NewEncoder(clientConn)
	decoder := json.NewDecoder(clientConn)
	if err := encoder.Encode(protocol.Request{Method: "cleanup-preview", TaskID: 1}); err != nil {
		t.Fatal(err)
	}
	var previewResponse protocol.Response
	if err := decoder.Decode(&previewResponse); err != nil {
		t.Fatal(err)
	}
	if !previewResponse.OK || previewResponse.CleanupPreview == nil {
		t.Fatalf("preview response = %+v", previewResponse)
	}
	if len(previewResponse.CleanupPreview.Targets) != 1 || previewResponse.CleanupPreview.Targets[0].SandboxName != "sandbox-conn-test" {
		t.Fatalf("cleanup preview = %+v", previewResponse.CleanupPreview)
	}

	if err := encoder.Encode(protocol.Request{Method: "cleanup", Cleanup: previewResponse.CleanupPreview}); err != nil {
		t.Fatal(err)
	}
	var cleanupResponse protocol.Response
	if err := decoder.Decode(&cleanupResponse); err != nil {
		t.Fatal(err)
	}
	_ = clientConn.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("server handler did not exit")
	}
	if !cleanupResponse.OK {
		t.Fatalf("cleanup response error = %s", cleanupResponse.Error)
	}
	if cleanupResponse.CleanupResult == nil || len(cleanupResponse.CleanupResult.Targets) != 1 || cleanupResponse.CleanupResult.Targets[0].SandboxName != "sandbox-conn-test" {
		t.Fatalf("cleanup result = %+v", cleanupResponse.CleanupResult)
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	log := strings.TrimSpace(string(logData))
	if !strings.Contains(log, "|rm --force sandbox-conn-test") {
		t.Fatalf("fake sbx log = %q, want forced rm", log)
	}
	if strings.HasPrefix(log, missingWorkspace+"|") {
		t.Fatalf("fake sbx ran in missing workspace: %q", log)
	}
}

func installFakeSbx(t *testing.T, tmp string, logPath string) {
	t.Helper()
	bin := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
printf '%s|%s\n' "$PWD" "$*" >> "$SBX_LOG"
if [ "$1" = "rm" ] && [ "$2" = "--force" ] && [ -n "$3" ]; then
  exit 0
fi
echo 'ERROR: stdin is not a terminal; use --force to skip confirmation' >&2
exit 1
`
	if err := os.WriteFile(filepath.Join(bin, "sbx"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SBX_LOG", logPath)
}
