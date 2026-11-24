package client

import (
	"errors"

	"radar/internal/protocol"
)

func decodeDiagnosticCleanup(raw []byte) (protocol.Response, error) {
	fail := func() (protocol.Response, error) {
		return protocol.Response{}, errors.New("invalid native manual cleanup response; outcome may be unknown, do not replay")
	}
	var wire struct {
		OK      bool                        `json:"ok"`
		Error   string                      `json:"error,omitempty"`
		Cleanup *protocol.DiagnosticCleanup `json:"diagnostic_cleanup,omitempty"`
	}
	if !decodeDiagnosticProjection(raw, &wire) || wire.OK != (wire.Cleanup != nil) || wire.OK && wire.Error != "" || !wire.OK && wire.Error == "" {
		return fail()
	}
	if wire.Cleanup != nil {
		v := wire.Cleanup
		req := protocol.DiagnosticMaintenanceRequest{Schema: protocol.DiagnosticsVersion, Operation: "cleanup", Mode: v.Mode, Scope: v.Scope, ParentID: v.RecordID}
		if v.Mode == "apply" {
			req.PlanID = v.PlanID
		}
		if !protocol.ValidDiagnosticCleanup(*v, req) {
			return fail()
		}
	}
	if !wire.OK {
		wire.Error = "native manual cleanup unavailable; outcome may be unknown, do not replay or automatically retry"
	}
	return protocol.Response{OK: wire.OK, Error: wire.Error, DiagnosticCleanup: wire.Cleanup}, nil
}
