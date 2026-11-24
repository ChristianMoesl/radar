package client

import (
	"errors"

	"radar/internal/protocol"
)

func decodeDiagnosticRecreation(raw []byte) (protocol.Response, error) {
	fail := func() (protocol.Response, error) {
		return protocol.Response{}, errors.New("invalid native recreation response; outcome may be unknown, do not replay or automatically retry")
	}
	var wire struct {
		OK         bool                           `json:"ok"`
		Error      string                         `json:"error,omitempty"`
		Recreation *protocol.DiagnosticRecreation `json:"diagnostic_recreation,omitempty"`
	}
	if !decodeDiagnosticProjection(raw, &wire) || wire.OK != (wire.Recreation != nil) || wire.OK && wire.Error != "" || !wire.OK && wire.Error == "" {
		return fail()
	}
	if wire.Recreation != nil {
		v := wire.Recreation
		req := protocol.DiagnosticMaintenanceRequest{Schema: protocol.DiagnosticsVersion, Operation: "recreate", Mode: v.Mode, Scope: v.Scope, ParentID: v.Proposal.ID}
		if v.Mode == "apply" {
			req.PlanID = v.PlanID
		}
		if !protocol.ValidDiagnosticRecreation(*v, req) {
			return fail()
		}
	}
	if !wire.OK {
		wire.Error = "native recreation unavailable or changed; outcome may be unknown, do not replay or automatically retry"
	}
	return protocol.Response{OK: wire.OK, Error: wire.Error, DiagnosticRecreation: wire.Recreation}, nil
}
