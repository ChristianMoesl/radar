package client

import (
	"errors"

	"radar/internal/protocol"
)

func decodeDiagnosticAgentStop(raw []byte) (protocol.Response, error) {
	fail := func() (protocol.Response, error) {
		return protocol.Response{}, errors.New("invalid native agent-stop response; outcome may be unknown, do not replay or automatically retry")
	}
	var wire struct {
		OK    bool                          `json:"ok"`
		Error string                        `json:"error,omitempty"`
		Stop  *protocol.DiagnosticAgentStop `json:"diagnostic_agent_stop,omitempty"`
	}
	if !decodeDiagnosticProjection(raw, &wire) || wire.OK != (wire.Stop != nil) || wire.OK && wire.Error != "" || !wire.OK && wire.Error == "" {
		return fail()
	}
	if wire.Stop != nil {
		v := wire.Stop
		q := protocol.DiagnosticMaintenanceRequest{Schema: protocol.DiagnosticsVersion, Operation: "agent_stop", Mode: v.Mode, Scope: v.Scope, ParentID: v.RecordID, AgentID: v.AgentID}
		if q.Mode == "apply" {
			q.PlanID = v.PlanID
		}
		if !protocol.ValidDiagnosticAgentStop(*v, q) {
			return fail()
		}
	}
	if !wire.OK {
		wire.Error = "native agent stop unavailable or changed; outcome may be unknown, do not replay or automatically retry"
	}
	return protocol.Response{OK: wire.OK, Error: wire.Error, DiagnosticAgentStop: wire.Stop}, nil
}
