package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"radar/internal/agentservice"
	"radar/internal/cleanup"
	"radar/internal/phasetiming"
	"radar/internal/protocol"
	"radar/internal/workspacegc"
)

// Attached to the existing bounded per-task nativeCleanupPlan, not a second
// cache. Native private fields and closures never enter transport or JSON.
type diagnosticExpiryBinding struct {
	Request      protocol.DiagnosticMaintenanceRequest
	Registration string // exact registered identities, including private runtime names
	View         protocol.DiagnosticExpiry
}
type diagnosticExpiryPlan struct {
	native       workspacegc.ExpiryPlan
	binding      diagnosticExpiryBinding
	scope        diagnosticRecreationScope
	admission    *diagnosticExpiryAdmission
	privateBytes int // Native candidate/task bytes also count toward the existing plan bound.
}
type diagnosticExpiryAdmission struct {
	operation string
	expires   time.Time
}

func validDiagnosticExpiryRequest(q protocol.DiagnosticMaintenanceRequest) bool {
	if q.Operation != "expire" {
		return false
	}
	q.Operation = "recreate"
	return validDiagnosticRecreationRequest(q)
}
func (s *Server) diagnosticExpiry(ctx context.Context, q protocol.DiagnosticMaintenanceRequest) (protocol.DiagnosticExpiry, error) {
	fail := func() (protocol.DiagnosticExpiry, error) {
		return protocol.DiagnosticExpiry{}, errors.New("exact native expiry unavailable or changed; outcome may be unknown, do not replay or automatically retry")
	}
	if !validDiagnosticExpiryRequest(q) || !s.maintenanceMu.TryLock() {
		return fail()
	}
	defer s.maintenanceMu.Unlock()
	if s.refreshPlan != nil && s.refreshPlan.view.Status == "running" {
		return fail()
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var retained diagnosticExpiryPlan
	var expires time.Time
	if q.Mode == "apply" {
		s.cleanupPlanMu.Lock()
		found := false
		for id, p := range s.cleanupPlans {
			if p.id == q.PlanID {
				delete(s.cleanupPlans, id)
				if p.expiry != nil && len(p.diagnostic) > 0 && time.Now().Before(p.expires) && p.revision == s.store.Revision() && json.Unmarshal(p.diagnostic, &retained.binding) == nil && retained.binding.Request == cleanupBindingRequest(q) {
					retained.native = p.expiry.native
					retained.scope = p.expiry.scope
					retained.admission = p.expiry.admission
					expires = p.expires
					found = true
				}
				break
			}
		}
		s.cleanupPlanMu.Unlock()
		if !found || !s.diagnosticCleanupInputsLive(ctx, expires, retained.scope.revision) {
			return fail()
		}
	}
	if ctx.Err() != nil || s.gcGuard == nil || s.markResourcesPending == nil || s.resourceSnapshot == nil {
		return fail()
	}
	provider, err := s.integrations.WorkspaceDiagnostics()
	if err != nil {
		return fail()
	}
	before, err := s.diagnosticRecreationScope(ctx, q, provider)
	if err != nil || !s.diagnosticRecreationInputsLive(ctx, before.revision, time.Time{}) {
		return fail()
	}
	if q.Mode == "apply" {
		saved := retained.scope
		if saved != before || retained.binding.Registration != before.registration || !s.diagnosticCleanupInputsLive(ctx, expires, before.revision) {
			return fail()
		}
		preview := retained.native.Preview()
		view := retained.binding.View
		if !protocol.ValidDiagnosticExpiry(view, protocol.DiagnosticMaintenanceRequest{Schema: protocol.DiagnosticsVersion, Operation: "expire", Mode: "preview", Scope: q.Scope, ParentID: q.ParentID}) {
			return fail()
		}
		// The retained original callback performs scope/publication checks and fresh
		// authored lifecycle admission. This wrapper starts managed evidence only
		// inside it, after admission, immediately before native candidate execution.
		native := retained.native
		result, _, executeErr := native.Execute(phasetiming.WithLogger(ctx, s.logger), s.store, s.cleanupService)
		op := retained.admission.operation
		if op == "" {
			return fail()
		}
		status := "succeeded"
		view.Mode, view.OperationID, view.Problem, view.Outcome = "apply", op, "none", "native_bundle_returned_unverified"
		if executeErr != nil {
			status = "failed"
			if len(result.Targets) > 0 {
				status = "partial"
			}
			view.Problem, view.Outcome = "native_error", "unverified_may_be_partial"
		}
		if ctx.Err() != nil {
			view.Problem, view.Outcome = "cancelled_or_timed_out", "unverified_may_be_partial"
			status = "failed"
			if len(result.Targets) > 0 {
				status = "partial"
			}
		}
		completed := len(result.Targets)
		view.CompletedTargets = &completed
		if result.ExecutionObservation != nil && protocol.ValidCleanupExecution(*result.ExecutionObservation, status, completed, len(preview.Targets)) {
			copy := *result.ExecutionObservation
			view.Execution = &copy
		}
		s.markResourcesPending()
		view.ResourceRefreshPending = true
		s.store.FinishCleanupOperation(op, protocol.ManagedOperation{Status: status, TaskID: preview.TaskID, WorkspaceID: before.workspaceID, CompletedTargets: completed, TotalTargets: len(preview.Targets)}, preview.Targets, result.ExecutionObservation)
		if !protocol.ValidDiagnosticExpiry(view, q) {
			return fail()
		}
		return view, nil
	}
	manager, err := s.integrations.WorkspaceManager()
	if err != nil {
		return fail()
	}
	root, err := manager.DefaultRoot()
	if err != nil || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return fail()
	}
	now := time.Now()
	plan, err := workspacegc.BuildPlan(s.store, now, workspacegc.Options{WorkspaceRoot: root})
	if err != nil {
		return fail()
	}
	var candidate workspacegc.Candidate
	matches := 0
	for _, c := range plan.Candidates {
		if c.WorkspaceID == before.workspaceID {
			candidate = c
			matches++
		}
	}
	if matches != 1 || candidate.Path != before.workspacePath {
		return fail()
	}
	nativePayload, e := json.Marshal(candidate)
	if e != nil || len(nativePayload) > maxCleanupPlanBytes {
		return fail()
	}
	providers := s.integrations.CleanupProviders()
	seen := map[string]bool{}
	if len(providers) < 1 || len(providers) > 4 {
		return fail()
	}
	for _, p := range providers {
		name := p.Descriptor().Name
		if seen[name] || (name != "git" && name != "sbx" && name != "tmux" && name != "workspace") {
			return fail()
		}
		seen[name] = true
	}
	guard := s.gcGuard
	admission := &diagnosticExpiryAdmission{}
	options := workspacegc.Options{WorkspaceRoot: root}
	// Captures only daemon-owned scope/options. No future user request can replace
	// the policy/time/guard or selected native targets. Preparation never calls it.
	options.GuardExecution = func(c context.Context, task protocol.Task, execute func() error) error {
		return guard(c, task, func() error {
			fresh, e := s.diagnosticRecreationScope(c, q, provider)
			if e != nil || fresh != before || !s.diagnosticRecreationInputsLive(c, before.revision, admission.expires) {
				return errors.New("expiry scope changed before execution")
			}
			admission.operation = s.store.BeginOperation("gc", task.ID, before.workspaceID)
			return execute()
		})
	}
	native, err := workspacegc.PrepareExpiry(ctx, s.store, s.cleanupService, candidate, now, options)
	if err != nil {
		return fail()
	}
	preview := native.Preview()
	proposal, registration, err := s.discloseDiagnosticExpiry(ctx, preview, candidate, before)
	if err != nil {
		return fail()
	}
	after, err := s.diagnosticRecreationScope(ctx, q, provider)
	if err != nil || after != before || !s.diagnosticRecreationInputsLive(ctx, before.revision, time.Time{}) {
		return fail()
	}
	view := protocol.DiagnosticExpiry{Schema: protocol.DiagnosticsVersion, Operation: "expire", Mode: "preview", Scope: q.Scope, FeedbackWorkspaceID: before.feedbackID, Evidence: "native_exact_workspace_expiry_plan_not_consent", Revision: before.revision, Proposal: proposal, Problem: "none", Outcome: "not_attempted", Limitations: protocol.DiagnosticExpiryLimitations()}
	if q.Scope != "installation" {
		view.ScopeWorkspaceID = before.workspaceID
	}
	binding := diagnosticExpiryBinding{Request: cleanupBindingRequest(q), Registration: registration, View: view}
	issued, err := s.issueBoundNativeCleanupPlan(ctx, preview, before.revision, nil, nil, &diagnosticExpiryPlan{native: native, binding: binding, scope: before, admission: admission, privateBytes: len(nativePayload)})
	if err != nil {
		return fail()
	}
	s.cleanupPlanMu.Lock()
	p, ok := s.cleanupPlans[issued.TaskID]
	if ok && p.id == issued.PlanID {
		err = json.Unmarshal(p.diagnostic, &binding)
	} else {
		err = errors.New("superseded")
	}
	s.cleanupPlanMu.Unlock()
	if err != nil || !protocol.ValidDiagnosticExpiry(binding.View, q) {
		return fail()
	}
	return binding.View, nil
}

// Disclose every selected native target, in its original order, without paths,
// branches or operation maps. Each registered member must be covered exactly
// once, by worktree removal or targeted missing-member registration cleanup.
// Refuse unsupported/incomplete bundles instead of proposing a partial plan.
func (s *Server) discloseDiagnosticExpiry(ctx context.Context, preview protocol.CleanupPreview, c workspacegc.Candidate, scope diagnosticRecreationScope) (protocol.DiagnosticExpiryPreview, string, error) {
	fail := errors.New("complete native expiry disclosure unavailable")
	var groups []protocol.DiagnosticWorkspace
	if json.Unmarshal([]byte(scope.registration), &groups) != nil {
		return protocol.DiagnosticExpiryPreview{}, "", fail
	}
	var selected protocol.DiagnosticWorkspace
	for _, g := range groups {
		if g.ID == scope.workspaceID {
			selected = g
		}
	}
	deadline, ok := workspacegc.ExpiryAt(c.Task)
	if !ok || deadline.After(time.Now()) || selected.ID != c.WorkspaceID || selected.Path != c.Path || selected.Truncated || preview.TaskID != c.TaskID || len(selected.Members) > 512 {
		return protocol.DiagnosticExpiryPreview{}, "", fail
	}
	members := map[string]protocol.DiagnosticMember{}
	for _, m := range selected.Members {
		rel, err := filepath.Rel(c.Path, m.Path)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || !filepath.IsAbs(m.Path) || filepath.Clean(m.Path) != m.Path || !filepath.IsAbs(m.Repository) || filepath.Clean(m.Repository) != m.Repository || members[m.Path].Path != "" {
			return protocol.DiagnosticExpiryPreview{}, "", fail
		}
		members[m.Path] = m
	}
	workspaceRef := ""
	for _, r := range c.Task.SourceRefs {
		if r.Source == "workspace" && r.Kind == "workspace" && r.WorkspaceID == c.WorkspaceID && r.Path == c.Path && r.ProvidesWorkspace {
			if workspaceRef != "" || r.ID == "" {
				return protocol.DiagnosticExpiryPreview{}, "", fail
			}
			workspaceRef = r.ID
		}
	}
	if workspaceRef == "" {
		return protocol.DiagnosticExpiryPreview{}, "", fail
	}
	owners, fingerprint, err := agentservice.DiagnosticWorkspaceRemovalOwners(ctx, []string{scope.workspacePath})
	ownerCount, ownerPresent := owners[scope.workspacePath]
	if err != nil || !ownerPresent || ownerCount != 0 {
		return protocol.DiagnosticExpiryPreview{}, "", fail
	}
	count := 0
	view := protocol.DiagnosticExpiryPreview{ID: c.WorkspaceID, RecordID: c.RecordID, TaskID: c.TaskID, Stage: "expired_cleanup", ExpiryAt: deadline.UTC().Format(time.RFC3339Nano), Targets: []protocol.DiagnosticExpiryTarget{}, Preserved: []string{"remote_repositories"}, DataLoss: protocol.DiagnosticExpiryDataLoss(), ExecutionEligibility: "unknown_fresh_execution_guards_not_run", OwnershipEvidence: "fresh_bounded_registration", AgentOwnerCount: &count, LeaseEvidence: "unknown_not_checked"}
	if len(preview.Targets) < 1 || len(preview.Targets) > 512 {
		return view, "", fail
	}
	seen, covered := map[string]bool{}, map[string]bool{}
	for i, t := range preview.Targets {
		if (t.WorkspaceID != c.WorkspaceID && !(t.WorkspaceID == "" && t.Path == c.Path)) || len(t.Safety) > 1024 || len(cleanup.AutomaticBlockingMessages([]protocol.CleanupTarget{t}, true)) > 0 {
			return view, "", fail
		}
		effect, branch := "", ""
		switch {
		case t.Source == "git" && t.Kind == "worktree":
			m, member := members[t.Path]
			matchedRef := false
			for _, r := range c.Task.SourceRefs {
				matchedRef = matchedRef || (r.ID == t.SourceRefID && r.Source == "git" && r.Kind == "worktree" && r.WorkspaceID == c.WorkspaceID && r.Path == t.Path && r.Branch == t.Branch)
			}
			if !member || covered[t.Path] || !matchedRef || t.ResourceID != t.Path || t.Operation["repository"] != m.Repository || t.ProvidesWorkspace {
				return view, "", fail
			}
			covered[t.Path] = true
			effect, branch = "remove_worktree_and_local_changes", "retain_local_branch"
			if deletion := t.Operation["delete_branch"]; deletion != "" {
				if deletion != t.Branch {
					return view, "", fail
				}
				branch = "delete_local_branch"
			}
		case t.Source == "workspace" && t.Kind == "missing_member":
			if members[t.Path].Path == "" || covered[t.Path] || t.ResourceID != t.Path || t.SourceRefID != workspaceRef || t.ProvidesWorkspace {
				return view, "", fail
			}
			covered[t.Path] = true
			effect, branch = "forget_missing_worktree_registration", "retain_local_branch"
		default:
			if seen[t.Source] || t.Path != c.Path {
				return view, "", fail
			}
			seen[t.Source] = true
			expected, kind := "", ""
			switch t.Source {
			case "workspace":
				expected, kind, effect = selected.ID, "workspace", "remove_workspace_local_files_registration_and_archive_completed_note"
				if i != len(preview.Targets)-1 || t.SourceRefID != workspaceRef {
					return view, "", fail
				}
			case "tmux":
				expected, kind, effect = selected.SessionName, "session", "remove_terminal_session_and_processes"
			case "sbx":
				expected, kind, effect = selected.SandboxName, "sandbox", "remove_sandbox_and_local_state"
			default:
				return view, "", fail
			}
			if expected == "" || t.ResourceID != expected || t.Kind != kind || t.ProvidesWorkspace != (t.Source == "workspace") {
				return view, "", fail
			}
		}
		raw, e := json.Marshal(t)
		if e != nil || len(raw) > 64*1024 {
			return view, "", fail
		}
		hash := sha256.Sum256(append([]byte(strconv.Itoa(i)+":"), raw...))
		view.Targets = append(view.Targets, protocol.DiagnosticExpiryTarget{ID: "expiry-target:" + hex.EncodeToString(hash[:]), Provider: t.Source, Effect: effect, BranchEffect: branch, SafetyNoticeCount: len(t.Safety)})
	}
	if !seen["workspace"] || seen["tmux"] != (selected.SessionName != "") || seen["sbx"] != (selected.SandboxName != "") || len(covered) != len(members) {
		return view, "", fail
	}
	fresh, stamp, e := agentservice.DiagnosticWorkspaceRemovalOwners(ctx, []string{scope.workspacePath})
	if e != nil || stamp != fingerprint || !reflect.DeepEqual(fresh, owners) || ctx.Err() != nil {
		return view, "", fail
	}
	return view, scope.registration, nil
}
