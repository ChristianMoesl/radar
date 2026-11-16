package agentservice

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"radar/internal/protocol"
)

func runnerIdentityMatches(version int, id, expected string) bool {
	return version == protocol.AgentProtocolVersion && id == expected
}

// ProbeDiagnosticAgent contacts only the selected registered runner's native
// hello/snapshot methods. It never calls Manager.status (workspace probes),
// reads a token or checkpoint, acquires a lease, or starts/reconciles work.
func ProbeDiagnosticAgent(ctx context.Context, workspace, id string) (protocol.DiagnosticAgentProbe, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if ctx.Err() != nil {
		return protocol.DiagnosticAgentProbe{}, fmt.Errorf("agent probe cancelled")
	}
	reg, before, err := diagnosticProbeRegistration(id)
	if err != nil || !filepath.IsAbs(reg.Workspace.Path) || (workspace != "" && !registrationWithinWorkspace(reg, workspace)) {
		return protocol.DiagnosticAgentProbe{}, fmt.Errorf("agent probe parent is unavailable or outside the selected scope")
	}
	view := protocol.DiagnosticAgentProbe{ID: reg.ID, Registered: true, Desired: reg.Desired, OwnershipHold: "registered_owner_even_when_stopped", RunnerEvidence: "unknown_not_reached", SnapshotEvidence: "unavailable", Problem: "unavailable", LeaseEvidence: "unknown_not_inspected"}
	path, err := socketPath(reg.ID)
	if err == nil {
		var hello struct {
			Protocol   int    `json:"protocol"`
			InstanceID string `json:"instanceId"`
		}
		if err = call(ctx, path, "hello", nil, &hello); err == nil {
			if !runnerIdentityMatches(hello.Protocol, hello.InstanceID, reg.ID) {
				view.Problem = "identity_or_protocol_mismatch"
			} else {
				view.RunnerEvidence = "reachable_matching_protocol_identity"
				view.Problem = "snapshot_unavailable"
				// Decode only the public diagnostic counters. Private tasks,
				// presentations, actions, conversations, errors and PID are ignored.
				var snapshot struct {
					Protocol          int    `json:"protocol"`
					InstanceID        string `json:"instanceId"`
					DefinitionVersion *int   `json:"definitionVersion"`
					Pending           *int   `json:"pending"`
					Problem           string `json:"problem"`
					Presentation      struct {
						Attention *bool `json:"attention"`
						Revision  *int  `json:"revision"`
					} `json:"presentation"`
				}
				if err = call(ctx, path, "snapshot", nil, &snapshot); err == nil {
					view.Problem = "invalid_snapshot"
					validCount := func(value *int) bool { return value != nil && *value >= 0 && int64(*value) <= 9007199254740991 }
					if runnerIdentityMatches(snapshot.Protocol, snapshot.InstanceID, reg.ID) && validCount(snapshot.DefinitionVersion) && *snapshot.DefinitionVersion > 0 && validCount(snapshot.Pending) && validCount(snapshot.Presentation.Revision) && snapshot.Presentation.Attention != nil {
						view.Snapshot = &protocol.DiagnosticAgentSnapshot{DefinitionVersion: *snapshot.DefinitionVersion, Pending: *snapshot.Pending, Revision: *snapshot.Presentation.Revision, Attention: *snapshot.Presentation.Attention, Problem: snapshot.Problem != ""}
						view.SnapshotEvidence = "fresh_runner_snapshot"
						view.Problem = "none"
					}
				}
			}
		}
	}
	_, after, err := diagnosticProbeRegistration(id)
	if err != nil || before.sum != after.sum || !os.SameFile(before.info, after.info) {
		return protocol.DiagnosticAgentProbe{}, fmt.Errorf("agent registration changed or unavailable; probe again")
	}
	deadline, bounded := ctx.Deadline()
	if ctx.Err() != nil || (bounded && !time.Now().Before(deadline)) {
		// Socket deadlines may fire before the context timer is scheduled.
		// An expired or partial handshake must never leave usable live evidence.
		view.RunnerEvidence, view.SnapshotEvidence, view.Problem = "unknown_not_reached", "unavailable", "cancelled_or_timed_out"
		view.Snapshot = nil
	}
	return view, nil
}

type diagnosticRegistrationMarker struct {
	info os.FileInfo
	sum  [32]byte
}

// The normal loader retains its existing policy. This narrower explicit probe
// accepts only one bounded stable regular registration, decoded by native rules.
func diagnosticProbeRegistration(id string) (Registration, diagnosticRegistrationMarker, error) {
	var marker diagnosticRegistrationMarker
	dir, err := instanceDir(id)
	if err != nil {
		return Registration{}, marker, err
	}
	path := filepath.Join(dir, "registration.json")
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return Registration{}, marker, err
	}
	defer file.Close()
	info, err := file.Stat()
	const bound = 64 * 1024
	if err != nil || !info.Mode().IsRegular() || info.Size() > bound {
		return Registration{}, marker, fmt.Errorf("registration unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(file, bound+1))
	after, statErr := file.Stat()
	current, pathErr := os.Lstat(path)
	if err != nil || statErr != nil || pathErr != nil || len(data) > bound || int64(len(data)) != info.Size() || info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) || !os.SameFile(info, current) || !current.Mode().IsRegular() || info.Size() != current.Size() || !info.ModTime().Equal(current.ModTime()) {
		return Registration{}, marker, fmt.Errorf("registration changed or unavailable")
	}
	reg, err := decodeRegistration(data, id)
	if err != nil {
		return Registration{}, marker, err
	}
	return reg, diagnosticRegistrationMarker{info: info, sum: sha256.Sum256(data)}, nil
}
