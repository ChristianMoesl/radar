package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"radar/internal/collector"
	"radar/internal/operationlock"
	"radar/internal/protocol"
)

type diagnosticRefreshPlan struct {
	view     protocol.DiagnosticMaintenance
	expires  time.Time // monotonic expiry; wall timestamp is display evidence only
	consumed bool
}

// Shared transport fields must not silently become selectors on the other
// operation, even when supplied as empty strings or null. The common request
// boundary rejects duplicate/folded members before this exact-key allowlist.
func diagnosticMaintenanceFields(raw []byte, operation string) bool {
	var frame map[string]json.RawMessage
	if json.Unmarshal(raw, &frame) != nil || len(frame) != 2 {
		return false
	}
	if _, ok := frame["method"]; !ok {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(frame["maintenance"], &fields) != nil {
		return false
	}
	for name := range fields {
		switch name {
		case "diagnostics_schema", "operation", "mode", "scope":
		case "plan_id":
			if operation != "refresh" && operation != "cleanup" {
				return false
			}
			if operation == "cleanup" && string(fields["mode"]) != `"apply"` {
				return false
			}
		case "parent_id", "workspace":
			if operation != "cleanup_check" && operation != "cleanup" {
				return false
			}
		case "feedback_workspace":
			if operation != "cleanup" {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// SetDiagnosticRefresh MUST receive the collection-only taskservice adapter, not
// the ordinary daemon refresh callback: ordinary refresh also runs workspace GC.
func (s *Server) SetDiagnosticRefresh(refresh func(context.Context) (collector.Result, error)) *Server {
	s.diagnosticRefresh = refresh
	return s
}

func (s *Server) refreshTargets() ([]protocol.DiagnosticRefreshTarget, error) {
	sources := s.integrations.Sources()
	if len(sources) == 0 || len(sources) > 32 {
		return nil, errors.New("bounded native collection targets unavailable")
	}
	seen := map[string]bool{}
	targets := make([]protocol.DiagnosticRefreshTarget, 0, len(sources))
	for _, source := range sources {
		name := source.Descriptor().Name
		if name == "" || len(name) > 512 || seen[name] {
			return nil, errors.New("native collection target identity unavailable")
		}
		seen[name] = true
		sum := sha256.Sum256([]byte(name))
		targets = append(targets, protocol.DiagnosticRefreshTarget{ID: "integration:" + hex.EncodeToString(sum[:]), Outcome: "unknown_not_attempted"})
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].ID < targets[j].ID })
	return targets, nil
}

func copyRefreshView(view protocol.DiagnosticMaintenance) protocol.DiagnosticMaintenance {
	view.Targets = append([]protocol.DiagnosticRefreshTarget{}, view.Targets...)
	view.Limitations = append([]string{}, view.Limitations...)
	return view
}

func (s *Server) maintainDiagnostics(ctx context.Context, req protocol.DiagnosticMaintenanceRequest) (protocol.DiagnosticMaintenance, error) {
	fail := func(message string) (protocol.DiagnosticMaintenance, error) {
		return protocol.DiagnosticMaintenance{}, errors.New(message)
	}
	if req.Schema != protocol.DiagnosticsVersion || req.Operation != "refresh" || req.ParentID != "" || req.Workspace != "" || req.FeedbackWorkspace != "" || req.Scope != "installation" || (req.Mode != "preview" && req.Mode != "apply") || (req.Mode == "preview" && req.PlanID != "") || (req.Mode == "apply" && (len(req.PlanID) != 64 || !hexPlanID(req.PlanID))) {
		return fail("maintenance requires explicit installation refresh preview or exact-plan apply")
	}
	if err := ctx.Err(); err != nil {
		return fail("maintenance cancelled before admission")
	}
	if s.diagnosticRefresh == nil || s.resourceSnapshot == nil {
		return fail("collection-only maintenance adapter unavailable")
	}
	if !s.maintenanceMu.TryLock() {
		return fail("maintenance admission busy")
	}
	if req.Mode == "apply" && s.refreshPlan != nil && s.refreshPlan.view.PlanID == req.PlanID && s.refreshPlan.consumed {
		// Lost replies must not replay accepted work. This point-in-time result is
		// volatile, not a restart-safe audit log; running remains unknown.
		view := copyRefreshView(s.refreshPlan.view)
		s.maintenanceMu.Unlock()
		return view, nil
	}
	targets, err := s.refreshTargets()
	if err != nil {
		s.maintenanceMu.Unlock()
		return protocol.DiagnosticMaintenance{}, err
	}
	// Target discovery can outlive the caller. Refuse before issuing a new
	// preview or consuming an apply plan and creating its managed operation.
	if err := ctx.Err(); err != nil {
		s.maintenanceMu.Unlock()
		return fail("maintenance cancelled during admission")
	}
	if req.Mode == "preview" {
		if s.refreshPlan != nil && s.refreshPlan.consumed && s.refreshPlan.view.Status == "running" {
			s.maintenanceMu.Unlock()
			return fail("diagnostic refresh still running")
		}
		now := time.Now()
		var token [32]byte
		if _, err := rand.Read(token[:]); err != nil {
			s.maintenanceMu.Unlock()
			return fail("maintenance preview identity unavailable")
		}
		pending := s.resourceSnapshot != nil && s.resourceSnapshot().ResourceRefreshPending
		// Publication reads can also outlive a preview caller. Preserve an
		// earlier plan rather than superseding it after observed cancellation.
		if err := ctx.Err(); err != nil {
			s.maintenanceMu.Unlock()
			return fail("maintenance cancelled during admission")
		}
		view := protocol.DiagnosticMaintenance{Schema: protocol.DiagnosticsVersion, Operation: "refresh", Mode: "preview", Scope: "installation", Evidence: "native_collection_only_refresh", Status: "preview", Problem: "none", PlanID: hex.EncodeToString(token[:]), ExpiresAt: now.Add(time.Minute).UTC().Format(time.RFC3339Nano), Revision: s.store.Revision(), Targets: targets, Counts: protocol.DiagnosticRefreshCounts{Unverified: len(targets)}, ResourceRefreshPending: pending, Limitations: protocol.DiagnosticRefreshLimitations()}
		s.refreshPlan = &diagnosticRefreshPlan{view: view, expires: now.Add(time.Minute)}
		s.maintenanceMu.Unlock()
		return copyRefreshView(view), nil
	}
	plan := s.refreshPlan
	valid := plan != nil && !plan.consumed && plan.view.PlanID == req.PlanID && time.Now().Before(plan.expires) && plan.view.Revision == s.store.Revision() && len(targets) == len(plan.view.Targets)
	if valid {
		for i := range targets {
			if targets[i].ID != plan.view.Targets[i].ID {
				valid = false
				break
			}
		}
	}
	if !valid {
		s.maintenanceMu.Unlock()
		return fail("maintenance preview expired or changed; preview again and obtain fresh consent")
	}
	plan.consumed = true
	plan.view.Mode, plan.view.Status = "apply", "running"
	plan.view.OperationID = s.store.BeginOperation("refresh", 0, "")
	s.maintenanceMu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result := collector.Result{}
	release, executeErr := operationlock.Acquire(false)
	if executeErr == nil {
		result, executeErr = s.diagnosticRefresh(ctx)
		release()
	}
	view := copyRefreshView(plan.view)
	view.Status = "returned"
	view.Counts = protocol.DiagnosticRefreshCounts{}
	for i := range view.Targets {
		outcome := "unknown_not_attempted"
		for name, attempt := range result.Attempts {
			sum := sha256.Sum256([]byte(name))
			if "integration:"+hex.EncodeToString(sum[:]) == view.Targets[i].ID {
				switch attempt.Outcome {
				case "complete", "incomplete", "skipped_before_collection", "cancelled_or_timed_out":
					outcome = attempt.Outcome
				}
			}
		}
		view.Targets[i].Outcome = outcome
		switch outcome {
		case "complete":
			view.Counts.Complete++
		case "incomplete":
			view.Counts.Incomplete++
		case "skipped_before_collection":
			view.Counts.Skipped++
		default:
			view.Counts.Unverified++
		}
	}
	view.Problem = "none"
	if view.Counts.Incomplete > 0 || view.Counts.Unverified > 0 {
		view.Problem = "incomplete_or_unverified"
	}
	if executeErr != nil {
		view.Problem = "native_error"
	}
	if ctx.Err() != nil {
		view.Problem = "cancelled_or_timed_out"
	}
	view.ResourceRefreshPending = s.resourceSnapshot != nil && s.resourceSnapshot().ResourceRefreshPending
	status := "succeeded"
	if view.Problem != "none" {
		status = "failed"
		if view.Counts.Complete > 0 {
			status = "partial"
		}
	}
	s.store.FinishOperation(view.OperationID, protocol.ManagedOperation{Status: status})
	s.maintenanceMu.Lock()
	plan.view = copyRefreshView(view)
	s.maintenanceMu.Unlock()
	return view, nil
}

func hexPlanID(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
