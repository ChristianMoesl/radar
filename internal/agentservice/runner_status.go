package agentservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"

	"radar/internal/protocol"
)

// The native manager and privacy-limited diagnostic probe share the same exact
// hello boundary. Neither may choose a duplicate or case-folded identity.
type runnerHello struct {
	Protocol   int    `json:"protocol"`
	InstanceID string `json:"instanceId"`
}

func (hello *runnerHello) UnmarshalJSON(raw []byte) error {
	if _, err := agentResponseObject(raw, "protocol", "instanceId"); err != nil {
		return err
	}
	type plain runnerHello
	var value plain
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	*hello = runnerHello(value)
	return nil
}

func decodeRunnerSnapshot(raw []byte, expected string) (*protocol.AgentSnapshot, error) {
	if _, err := agentResponseObject(raw, "protocol", "instanceId", "definitionVersion", "pid", "presentation", "pending", "problem", "tasks", "recent"); err != nil {
		return nil, fmt.Errorf("invalid agent snapshot")
	}
	var snapshot protocol.AgentSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return nil, fmt.Errorf("invalid agent snapshot")
	}
	// A JSON-safe integer is not necessarily a native process selector. Both
	// supported Unix hosts use signed-32-bit pid_t; never let liveness checks
	// truncate an unrelated PID or interpret a wrapped process-group selector.
	if !runnerIdentityMatches(snapshot.Protocol, snapshot.InstanceID, expected) || snapshot.PID <= 0 || int64(snapshot.PID) > 1<<31-1 {
		return nil, fmt.Errorf("agent snapshot protocol/identity/PID mismatch")
	}
	return &snapshot, nil
}

// observeRunner returns an error for uncertain native evidence, independently of
// desired state. Only an uncancelled missing/refused Unix dial proves no listener;
// EOF, timeout, invalid replies and RPC errors cannot authorize recovery/removal.
// It never reads a cached snapshot, which cannot establish live ownership.
func observeRunner(ctx context.Context, reg Registration) (protocol.AgentStatus, error) {
	dir, _ := instanceDir(reg.ID)
	result := protocol.AgentStatus{ID: reg.ID, Entrypoint: reg.Entrypoint, DataDir: dir, Desired: reg.Desired, Workspace: reg.Workspace}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	path, err := socketPath(reg.ID)
	if err != nil {
		return result, err
	}
	var hello runnerHello
	if err = call(ctx, path, "hello", nil, &hello); err != nil {
		var transport *net.OpError
		if ctx.Err() == nil && errors.As(err, &transport) && transport.Op == "dial" &&
			(errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED)) {
			return result, nil
		}
		return result, fmt.Errorf("runner hello unavailable; inspect its logs before recovery or removal")
	}
	if !runnerIdentityMatches(hello.Protocol, hello.InstanceID, reg.ID) {
		return result, fmt.Errorf("agent protocol/identity mismatch")
	}
	var raw json.RawMessage
	if err = call(ctx, path, "snapshot", nil, &raw); err != nil {
		return result, fmt.Errorf("runner snapshot unavailable; inspect its logs before recovery or removal")
	}
	snapshot, err := decodeRunnerSnapshot(raw, reg.ID)
	if err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	result.Running, result.Snapshot = true, snapshot
	return result, nil
}

func status(ctx context.Context, reg Registration) protocol.AgentStatus {
	result, err := observeRunner(ctx, reg)
	if err != nil {
		result.Error = err.Error()
	} else if !result.Running && reg.Desired {
		result.Error = "runner unavailable"
	}
	if !result.Running {
		if data, readErr := os.ReadFile(filepath.Join(result.DataDir, "snapshot.json")); readErr == nil {
			result.Snapshot, _ = decodeRunnerSnapshot(data, reg.ID)
		}
	}
	return result
}
