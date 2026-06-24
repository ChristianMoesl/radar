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

	"radar/internal/ingestion"
	"radar/internal/protocol"
	"radar/internal/sbx"
	"radar/internal/state"
)

func TestDeleteSbxSandboxE2EUsesForceWithoutWorkspaceCWD(t *testing.T) {
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
		NewWithSources(store, logger, nil, nil, []ingestion.Source{sbx.NewSource()}).handle(serverConn)
	}()

	encoder := json.NewEncoder(clientConn)
	decoder := json.NewDecoder(clientConn)
	if err := encoder.Encode(protocol.Request{Method: "delete-preview", TaskID: 1}); err != nil {
		t.Fatal(err)
	}
	var previewResponse protocol.Response
	if err := decoder.Decode(&previewResponse); err != nil {
		t.Fatal(err)
	}
	if !previewResponse.OK || previewResponse.DeletePreview == nil {
		t.Fatalf("preview response = %+v", previewResponse)
	}
	if previewResponse.DeletePreview.ConfirmTitle != "Delete sbx sandbox?" || previewResponse.DeletePreview.Warning == "" {
		t.Fatalf("delete preview = %+v", previewResponse.DeletePreview)
	}

	if err := encoder.Encode(protocol.Request{Method: "delete", Delete: previewResponse.DeletePreview}); err != nil {
		t.Fatal(err)
	}
	var deleteResponse protocol.Response
	if err := decoder.Decode(&deleteResponse); err != nil {
		t.Fatal(err)
	}
	_ = clientConn.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("server handler did not exit")
	}
	if !deleteResponse.OK {
		t.Fatalf("delete response error = %s", deleteResponse.Error)
	}
	if deleteResponse.DeleteResult == nil || deleteResponse.DeleteResult.Title != "sandbox-conn-test" {
		t.Fatalf("delete result = %+v", deleteResponse.DeleteResult)
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
