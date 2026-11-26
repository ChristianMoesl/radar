package agentservice

import (
	"bytes"
	"encoding/json"

	"radar/internal/protocol"
)

// Both cached and live diagnostic reads use the same finite native projection.
// Pointers distinguish missing/null evidence from valid zero counts or false
// attention. Private snapshot content remains ignored, never copied to output.
// This does not make a cached checkpoint a live observation or a history record.
type diagnosticRunnerSnapshot struct {
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

func (snapshot *diagnosticRunnerSnapshot) UnmarshalJSON(raw []byte) error {
	members, err := agentResponseObject(raw, "protocol", "instanceId", "definitionVersion", "pending", "problem", "presentation")
	if err != nil {
		return err
	}
	if presentation, ok := members["presentation"]; ok && !bytes.Equal(bytes.TrimSpace(presentation), []byte("null")) {
		if _, err := agentResponseObject(presentation, "attention", "revision"); err != nil {
			return err
		}
	}
	type plain diagnosticRunnerSnapshot
	var value plain
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	*snapshot = diagnosticRunnerSnapshot(value)
	return nil
}

func (snapshot diagnosticRunnerSnapshot) projection(expected string) *protocol.DiagnosticAgentSnapshot {
	validCount := func(value *int) bool {
		return value != nil && *value >= 0 && int64(*value) <= 9007199254740991
	}
	if !runnerIdentityMatches(snapshot.Protocol, snapshot.InstanceID, expected) || !validCount(snapshot.DefinitionVersion) || *snapshot.DefinitionVersion < 1 || !validCount(snapshot.Pending) || !validCount(snapshot.Presentation.Revision) || snapshot.Presentation.Attention == nil {
		return nil
	}
	return &protocol.DiagnosticAgentSnapshot{DefinitionVersion: *snapshot.DefinitionVersion, Pending: *snapshot.Pending, Revision: *snapshot.Presentation.Revision, Attention: *snapshot.Presentation.Attention, Problem: snapshot.Problem != ""}
}
