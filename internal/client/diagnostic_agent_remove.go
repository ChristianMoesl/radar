package client

import (
	"errors"

	"radar/internal/protocol"
)

func decodeDiagnosticAgentRemove(raw []byte) (protocol.Response, error) {
	fail := func() (protocol.Response, error) {
		return protocol.Response{}, errors.New("invalid native agent-removal response; outcome may be unknown, do not replay or automatically retry")
	}
	var wire struct {
		OK      bool                            `json:"ok"`
		Error   string                          `json:"error,omitempty"`
		Removal *protocol.DiagnosticAgentRemove `json:"diagnostic_agent_remove,omitempty"`
	}
	if !decodeDiagnosticProjection(raw, &wire) || wire.OK != (wire.Removal != nil) || wire.OK && wire.Error != "" || !wire.OK && wire.Error == "" {
		return fail()
	}
	if wire.Removal != nil {
		v := wire.Removal
		q := protocol.DiagnosticMaintenanceRequest{Schema: protocol.DiagnosticsVersion, Operation: "agent_remove", Mode: v.Mode, Scope: v.Scope, ParentID: v.RecordID, AgentID: v.AgentID}
		if q.Mode == "apply" {
			q.PlanID = v.PlanID
		}
		if !protocol.ValidDiagnosticAgentRemove(*v, q) {
			return fail()
		}
	}
	if !wire.OK {
		wire.Error = "native agent removal unavailable or changed; outcome may be unknown, do not replay or automatically retry"
	}
	return protocol.Response{OK: wire.OK, Error: wire.Error, DiagnosticAgentRemove: wire.Removal}, nil
}
