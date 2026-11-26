package client

import (
	"errors"

	"radar/internal/protocol"
)

func decodeDiagnosticExpiry(raw []byte) (protocol.Response, error) {
	fail := func() (protocol.Response, error) {
		return protocol.Response{}, errors.New("invalid native expiry response; outcome may be unknown, do not replay or automatically retry")
	}
	var wire struct {
		OK     bool                       `json:"ok"`
		Error  string                     `json:"error,omitempty"`
		Expiry *protocol.DiagnosticExpiry `json:"diagnostic_expiry,omitempty"`
	}
	if !decodeDiagnosticProjection(raw, &wire) || wire.OK != (wire.Expiry != nil) || wire.OK && wire.Error != "" || !wire.OK && wire.Error == "" {
		return fail()
	}
	if wire.Expiry != nil {
		v := wire.Expiry
		req := protocol.DiagnosticMaintenanceRequest{Schema: protocol.DiagnosticsVersion, Operation: "expire", Mode: v.Mode, Scope: v.Scope, ParentID: v.Proposal.ID}
		if v.Mode == "apply" {
			req.PlanID = v.PlanID
		}
		if !protocol.ValidDiagnosticExpiry(*v, req) {
			return fail()
		}
	}
	if !wire.OK {
		wire.Error = "native expiry unavailable or changed; outcome may be unknown, do not replay or automatically retry"
	}
	return protocol.Response{OK: wire.OK, Error: wire.Error, DiagnosticExpiry: wire.Expiry}, nil
}
