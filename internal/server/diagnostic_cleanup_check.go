package server

import (
	"context"
	"errors"

	"radar/internal/diagnostics"
	"radar/internal/protocol"
)

func (s *Server) checkDiagnosticCleanup(ctx context.Context, req protocol.DiagnosticMaintenanceRequest) (protocol.DiagnosticCleanupCheck, error) {
	if s.resourceSnapshot == nil || !s.maintenanceMu.TryLock() {
		return protocol.DiagnosticCleanupCheck{}, errors.New("native cleanup guard check admission unavailable")
	}
	defer s.maintenanceMu.Unlock()
	if s.refreshPlan != nil && s.refreshPlan.view.Status == "running" {
		return protocol.DiagnosticCleanupCheck{}, errors.New("native collection maintenance still running")
	}
	return diagnostics.CheckCleanupBundle(ctx, s.store, req, func() bool { return s.resourceSnapshot().ResourceRefreshPending }, s.integrations)
}
