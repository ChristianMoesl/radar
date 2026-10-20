package command

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionRetainsStderrWithoutExposingOrdinaryDiagnostics(t *testing.T) {
	tool := filepath.Join(t.TempDir(), "version-tool")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\nprintf 'v0.4.0\\n' >&2\n"), 0700); err != nil {
		t.Fatal(err)
	}
	version, err := Version(context.Background(), tool, nil)
	if err != nil || strings.TrimSpace(string(version)) != "v0.4.0" {
		t.Fatal(string(version), err)
	}
	ordinary, err := Output(context.Background(), tool, nil, nil)
	if err != nil || len(ordinary) != 0 {
		t.Fatal("ordinary API diagnostics escaped")
	}
}
