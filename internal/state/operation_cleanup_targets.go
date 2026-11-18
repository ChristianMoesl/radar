package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"

	"radar/internal/protocol"
)

// Preserve only the first bounded execution-order summary. Native input outside
// the budget withholds the entire summary, never a plausible incomplete capture.
func cleanupReturnedTargets(id string, result protocol.ManagedOperation, targets []protocol.CleanupTarget) ([]protocol.OperationCleanupTarget, bool) {
	n, completed := len(targets), result.CompletedTargets
	if n < 1 || n > 512 || result.TotalTargets != n || completed < 0 || completed > n ||
		(result.Status == "succeeded" && completed != n) ||
		(result.Status == "failed" && completed != 0) ||
		(result.Status == "partial" && (completed == 0 || completed == n)) {
		return nil, false
	}
	bytes := 0
	for _, target := range targets {
		if !protocol.ManagedCleanupTargetKind(target.Source, target.Kind) || target.ResourceID == "" || len(target.ResourceID) > 4096 || len(target.Path) > 4096 {
			return nil, false
		}
		bytes += len(target.ResourceID) + len(target.Path)
		if bytes > 1024*1024 {
			return nil, false
		}
	}
	views := make([]protocol.OperationCleanupTarget, 0, min(n, protocol.DiagnosticChildLimit))
	for i, target := range targets[:min(n, protocol.DiagnosticChildLimit)] {
		// Operation identity and ordinal make duplicate resources distinguishable
		// without claiming stable cross-operation/runtime identity. Do not hash
		// titles, errors, safety messages or provider execution payloads.
		raw, _ := json.Marshal([]string{id, strconv.Itoa(i), target.Source, target.Kind, target.ResourceID, target.Path})
		sum := sha256.Sum256(raw)
		outcome := "not_completed_may_be_partial"
		if i < completed {
			outcome = "returned_success_unverified"
		}
		views = append(views, protocol.OperationCleanupTarget{Index: i, ID: "cleanup-result:" + hex.EncodeToString(sum[:]), Provider: target.Source, Kind: target.Kind, Outcome: outcome})
	}
	return views, true
}
