package client

import (
	"errors"

	"radar/internal/protocol"
)

func decodeDiagnosticRetirement(raw []byte) (protocol.Response, error) {
	fail := func() (protocol.Response, error) {
		return protocol.Response{}, errors.New("invalid native retirement response; outcome may be unknown, do not replay or automatically retry")
	}
	var wire struct {
		OK         bool                           `json:"ok"`
		Error      string                         `json:"error,omitempty"`
		Retirement *protocol.DiagnosticRetirement `json:"diagnostic_retirement,omitempty"`
	}
	if !decodeDiagnosticProjection(raw, &wire) || wire.OK != (wire.Retirement != nil) || wire.OK && wire.Error != "" || !wire.OK && wire.Error == "" {
		return fail()
	}
	if wire.Retirement != nil {
		v := wire.Retirement
		req := protocol.DiagnosticMaintenanceRequest{Schema: protocol.DiagnosticsVersion, Operation: "retire", Mode: v.Mode, Scope: v.Scope, ParentID: v.Proposal.ID}
		if v.Mode == "apply" {
			req.PlanID = v.PlanID
		}
		if !protocol.ValidDiagnosticRetirement(*v, req) {
			return fail()
		}
	}
	if !wire.OK {
		wire.Error = "native retirement unavailable or changed; outcome may be unknown, do not replay or automatically retry"
	}
	return protocol.Response{OK: wire.OK, Error: wire.Error, DiagnosticRetirement: wire.Retirement}, nil
}
