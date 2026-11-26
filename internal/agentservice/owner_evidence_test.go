package agentservice

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"radar/internal/protocol"
)

func invalidOwnerRecord(t *testing.T, reg Registration, kind string) string {
	t.Helper()
	dir, _ := instanceDir(reg.ID)
	path := filepath.Join(dir, "pid")
	data := "private-invalid-owner"
	switch kind {
	case "empty":
		data = ""
	case "zero":
		data = "0"
	case "negative":
		data = "-1"
	case "overflow":
		data = "2147483648"
	case "over_bound":
		data = strings.Repeat(" ", 65) + "987654"
	case "directory":
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		return path
	case "symlink", "dangling_symlink":
		target := filepath.Join(t.TempDir(), "private-owner")
		if kind == "symlink" {
			if err := os.WriteFile(target, []byte("987654"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
		return path
	case "fifo":
		if err := syscall.Mkfifo(path, 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNativeAgentUnknownOwnerRecordCannotAuthorizeRecoveryStopOrRemoval(t *testing.T) {
	for _, kind := range []string{"malformed", "empty", "zero", "negative", "overflow", "over_bound", "directory", "symlink", "dangling_symlink"} {
		for _, operation := range []string{"start", "stop", "restart", "remove"} {
			t.Run(kind+"/"+operation, func(t *testing.T) {
				reg := probeFixture(t)
				path := invalidOwnerRecord(t, reg, kind)
				before, err := os.Lstat(path)
				if err != nil {
					t.Fatal(err)
				}
				prepared := 0
				manager := nativeStatusManager(reg, &prepared)
				_, err = manager.Handle(context.Background(), protocol.AgentRequest{ID: reg.ID, Operation: operation})
				if err == nil || prepared != 0 {
					t.Error("unknown owner record admitted native controls or workspace preparation", err, prepared)
				}
				if err != nil && strings.Contains(err.Error(), "private-") {
					t.Error("private ownership data escaped")
				}
				after, err := os.Lstat(path)
				if err != nil || !os.SameFile(before, after) {
					t.Error("refusal rewrote ownership record", err)
				}
				if _, err := load(reg.ID); err != nil {
					t.Error("unknown ownership removed registration", err)
				}
				if err := CheckWorkspaceRemoval(reg.Workspace.Path); err == nil {
					t.Error("unknown ownership released workspace hold")
				}
			})
		}
	}
}

func TestNativeAgentUnknownOwnerRecordRefusesShutdownBeforeDispatch(t *testing.T) {
	for _, kind := range []string{"malformed", "symlink", "dangling_symlink", "overflow"} {
		t.Run(kind, func(t *testing.T) {
			reg := probeFixture(t)
			invalidOwnerRecord(t, reg, kind)
			methods := nativeStatusPeer(t, reg, `{"protocol":1,"instanceId":"worker"}`, nativeStatusSnapshot(reg))
			if err := stop(context.Background(), reg); err == nil {
				t.Error("unknown independent ownership accepted shutdown")
			}
			for _, method := range methods() {
				if method != "hello" && method != "snapshot" {
					t.Error("unknown ownership dispatched native effect", method)
				}
			}
		})
	}
}

func TestNativeAgentOwnerRecordDriftAfterShutdownCannotCertifyStopped(t *testing.T) {
	reg := probeFixture(t)
	dir, _ := instanceDir(reg.ID)
	path := filepath.Join(dir, "pid")
	if err := os.WriteFile(path, []byte("987654"), 0600); err != nil {
		t.Fatal(err)
	}
	changed := make(chan error, 1)
	methods := rawDiagnosticAgentPeer(t, reg, func(method string) string {
		result := `{"stopping":true}`
		switch method {
		case "hello":
			result = `{"protocol":1,"instanceId":"worker"}`
		case "snapshot":
			result = nativeStatusSnapshot(reg)
		case "shutdown":
			changed <- os.WriteFile(path, []byte("private-unknown-owner"), 0600)
		}
		return `{"jsonrpc":"2.0","id":1,"result":` + result + `}`
	})
	err := stop(context.Background(), reg)
	if mutation := <-changed; mutation != nil {
		t.Fatal(mutation)
	}
	if err == nil || len(methods()) != 3 {
		t.Error("acknowledged shutdown and invalid owner record certified stopped or retried", err, methods())
	}
	if data, _ := os.ReadFile(path); string(data) != "private-unknown-owner" {
		t.Error("shutdown refusal repaired private owner data")
	}
}

func TestNativeAgentOwnerFIFORefusesWithoutWaitingForAWriter(t *testing.T) {
	for _, operation := range []string{"start", "stop", "restart", "remove"} {
		t.Run(operation, func(t *testing.T) {
			reg := probeFixture(t)
			path := invalidOwnerRecord(t, reg, "fifo")
			prepared := 0
			manager := nativeStatusManager(reg, &prepared)
			done := make(chan error, 1)
			go func() {
				_, err := manager.Handle(context.Background(), protocol.AgentRequest{ID: reg.ID, Operation: operation})
				done <- err
			}()
			select {
			case err := <-done:
				if err == nil || prepared != 0 {
					t.Error("FIFO ownership authorized native effect", err, prepared)
				}
			case <-time.After(time.Second):
				// Unblock the original unrestricted reader so a red regression cannot
				// leak a blocked fixture goroutine or require a timeout/process kill.
				writer, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0600)
				if err != nil {
					t.Fatal("cannot release original FIFO reader", err)
				}
				_, _ = writer.Write([]byte("987654"))
				_ = writer.Close()
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					t.Fatal("FIFO fixture cleanup did not complete")
				}
				t.Error("native ownership observation waited for a FIFO writer")
			}
		})
	}
}

func TestNativeAgentMissingAndFiniteDeadOwnerRecordsRetainNativeSemantics(t *testing.T) {
	for _, data := range []string{"missing", "987654", " 987654\n", strings.Repeat(" ", 58) + "987654", "2147483647", strconv.Itoa(os.Getpid())} {
		t.Run(fmt.Sprintf("record_%q", data), func(t *testing.T) {
			reg := probeFixture(t)
			dir, _ := instanceDir(reg.ID)
			if data != "missing" {
				if err := os.WriteFile(filepath.Join(dir, "pid"), []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := stop(context.Background(), reg)
			live := data == strconv.Itoa(os.Getpid())
			if (err != nil) != live {
				t.Fatal("known absent/dead/live record semantics changed", err)
			}
			if live && errors.Is(err, os.ErrNotExist) {
				t.Fatal("live owner became missing")
			}
		})
	}
}

func TestNativeAgentUnknownOwnerEvidenceDoesNotBroadenDiagnosticProbe(t *testing.T) {
	reg := probeFixture(t)
	invalidOwnerRecord(t, reg, "fifo")
	methods := nativeStatusPeer(t, reg, `{"protocol":1,"instanceId":"worker"}`, nativeStatusSnapshot(reg))
	view, err := ProbeDiagnosticAgent(context.Background(), reg.Workspace.Path, reg.ID)
	if err != nil || view.RunnerEvidence != "reachable_matching_protocol_identity" || view.SnapshotEvidence != "fresh_runner_snapshot" || view.OwnershipHold != "registered_owner_even_when_stopped" || len(methods()) != 2 {
		t.Fatal("diagnostic observation expanded into native owner/private-file acquisition", view, err, methods())
	}
}
