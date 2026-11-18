// Validate the exact privacy allowlist, not a generic job/status payload.
export function validateOperations(data: Record<string, unknown>, params: { scope?: string; id?: string; limit?: number; cursor?: string }): void {
  function fail(): never { throw new Error("Invalid Radar managed-operation diagnostics"); }
  const object = (v: unknown): v is Record<string, any> => !!v && typeof v === "object" && !Array.isArray(v);
  const text = (v: unknown, max = 512): v is string => typeof v === "string" && v.length > 0 && Buffer.byteLength(v, "utf8") <= max;
  const integer = (v: unknown): v is number => Number.isSafeInteger(v) && (v as number) >= 0;
  const keys = (v: Record<string, any>, allowed: string[]) => Object.keys(v).every(key => allowed.includes(key));
  const scope = params.scope ?? "origin";
  if (!keys(data, ["diagnostics_schema", "section", "scope", "available", "feedback_workspace_id", "origin_workspace_id", "evidence", "reason", "inventory"]) || data.diagnostics_schema !== 27 || data.section !== "operations" || data.scope !== scope || !text(data.feedback_workspace_id) || (data.origin_workspace_id !== undefined && !text(data.origin_workspace_id))) fail();
  if (data.available === false) {
    if (data.evidence !== "unavailable" || data.inventory !== undefined || !["origin_not_registered", "daemon_unavailable", "snapshot_changed_restart_pagination"].includes(String(data.reason)) || (data.reason === "origin_not_registered" && scope !== "origin")) fail();
    return;
  }
  const evidence = "volatile_current_daemon_managed_operation_observations";
  const page = data.inventory;
  const limitations = ["volatile_current_daemon_only_no_pre_restart_history", "active_plus_latest_64_terminal_or_reported_observations_not_complete_history", "observed_at_is_snapshot_read_time_not_operation_time", "workspace_scope_uses_cached_retained_refs_not_verified_ownership", "task_scope_can_include_effects_in_other_workspaces", "client_reports_are_unverified_not_daemon_execution", "handler_success_is_not_resource_verification_or_reconciliation", "cleanup_target_summary_is_first_16_original_ordinals_not_failure_pinpointing", "incomplete_target_may_be_partially_changed_or_not_attempted", "opaque_target_ids_are_operation_local_not_preview_or_replay_tokens", "running_counts_retries_and_lock_waits_unknown", "completed_cleanup_phases_only_no_active_phase_progress", "phase_aggregates_overlap_and_sum_individually_truncated_milliseconds", "missing_phase_evidence_is_unknown_not_zero_or_complete_coverage", "handler_timing_excludes_pre_start_queue_and_post_return_reconciliation", "wall_timestamps_may_jump_elapsed_uses_monotonic_clock", "missing_or_invalid_timing_is_unknown_not_zero", "untracked_gc_runtime_and_remote_operations_not_covered", "no_replay_refresh_probe_or_maintenance", "raw_errors_paths_payloads_and_target_details_withheld"];
  if (data.available !== true || data.reason !== undefined || data.evidence !== evidence || (scope === "origin" && !text(data.origin_workspace_id)) || !object(page)) fail();
  if (!keys(page, ["diagnostics_schema", "section", "evidence", "observed_at", "revision", "snapshot", "total", "items", "next_cursor", "pagination_complete", "resource_refresh_pending", "limitations"]) || Buffer.byteLength(JSON.stringify(page), "utf8") > 32 * 1024 || page.diagnostics_schema !== 27 || page.section !== "operations" || page.evidence !== evidence || !text(page.observed_at, 35) || !/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d{1,9})?Z$/.test(page.observed_at) || !Number.isFinite(Date.parse(page.observed_at)) || new Date(page.observed_at).toISOString().slice(0, 19) !== page.observed_at.slice(0, 19) || !integer(page.revision) || !text(page.snapshot, 64) || !/^[0-9a-f]{64}$/.test(page.snapshot) || typeof page.resource_refresh_pending !== "boolean" || !integer(page.total) || !Array.isArray(page.items) || page.items.length > (params.limit ?? 20) || page.items.length > page.total || typeof page.pagination_complete !== "boolean") fail();
  if (!Array.isArray(page.limitations) || page.limitations.length !== limitations.length || new Set(page.limitations).size !== limitations.length || page.limitations.some((v: unknown) => typeof v !== "string" || !limitations.includes(v))) fail();
  if (page.pagination_complete ? page.next_cursor !== undefined : !text(page.next_cursor)) fail();
  if ((page.total > 0 && page.items.length === 0) || (!params.cursor && (page.pagination_complete ? page.items.length !== page.total : page.items.length >= page.total)) || (params.id !== undefined && page.total > 1)) fail();
  const kinds = ["refresh", "unknown", "cleanup", "workspace", "task-create", "task-done", "task-reopen", "task-priority", "task-mute", "task-unmute", "task-title", "task-note", "task-delete"];
  const states: Record<string, string> = { unknown: "unknown_not_captured", running: "daemon_handler_running", reported: "unverified_client_report_not_daemon_execution", succeeded: "daemon_handler_result_not_resource_verification", failed: "daemon_handler_result_not_resource_verification", partial: "daemon_handler_result_not_resource_verification" };
  const time = (v: unknown): v is string => text(v, 35) && /^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d{1,9})?Z$/.test(v) && !v.startsWith("0000-") && !/^0001-01-01T00:00:00(?:\.0{1,9})?Z$/.test(v) && Number.isFinite(Date.parse(v)) && new Date(v).toISOString().slice(0, 19) === v.slice(0, 19);
  const seen = new Set<string>();
  for (const row of page.items) {
    if (!object(row) || !keys(row, ["id", "operation"]) || !text(row.id) || seen.has(row.id) || (params.id !== undefined && row.id !== params.id) || !object(row.operation)) fail();
    seen.add(row.id);
    const op = row.operation;
    if (!keys(op, ["id", "kind", "status", "task_id", "workspace_id", "execution_evidence", "target_evidence", "completed_targets", "total_targets", "timing_evidence", "started_at", "finished_at", "elapsed_milliseconds", "phase_timing_evidence", "phase_timings", "cleanup_target_evidence", "cleanup_targets", "cleanup_targets_truncated"]) || op.id !== row.id || !kinds.includes(op.kind) || !Object.hasOwn(states, op.status) || op.execution_evidence !== states[op.status] || (op.task_id !== undefined && (!integer(op.task_id) || op.task_id < 1)) || (op.workspace_id !== undefined && !text(op.workspace_id))) fail();
    if ((op.status === "reported") !== (op.kind === "workspace")) fail();
    if (op.timing_evidence === "native_handler_start_only") {
      if (op.status !== "running" || !time(op.started_at) || op.finished_at !== undefined || op.elapsed_milliseconds !== undefined) fail();
    } else if (op.timing_evidence === "native_handler_monotonic_elapsed") {
      if (!["succeeded", "failed", "partial"].includes(op.status) || !time(op.started_at) || !time(op.finished_at) || !integer(op.elapsed_milliseconds) || op.elapsed_milliseconds > 9223372036854) fail();
      // Wall clocks may jump; do not infer duration or success from their order.
    } else if (op.timing_evidence !== "unknown_not_captured" || op.started_at !== undefined || op.finished_at !== undefined || op.elapsed_milliseconds !== undefined) fail();
    if (op.phase_timing_evidence === "native_completed_phase_aggregates" || op.phase_timing_evidence === "unavailable_invalid_or_overflow") {
      if (op.kind !== "cleanup" || !["running", "succeeded", "failed", "partial"].includes(op.status)) fail();
      if (op.phase_timing_evidence === "unavailable_invalid_or_overflow") {
        if (op.phase_timings !== undefined) fail();
      } else {
        const names = ["cleanup_execution", "cleanup_bundle_preflight", "cleanup_resource", "cleanup_worktree", "cleanup_runtime", "cleanup_anchor", "cleanup_protected_scan", "cleanup_member_scan", "managed_worktree_removal", "worktree_removal", "branch_publication_verification"];
        if (!Array.isArray(op.phase_timings) || op.phase_timings.length < 1 || op.phase_timings.length > names.length) fail();
        let previous = "";
        for (const phase of op.phase_timings) {
          if (!object(phase) || !keys(phase, ["phase", "completed_intervals", "elapsed_milliseconds"]) || !names.includes(phase.phase) || phase.phase <= previous || !integer(phase.completed_intervals) || phase.completed_intervals < 1 || phase.completed_intervals > 1_000_000 || !integer(phase.elapsed_milliseconds) || phase.elapsed_milliseconds > 9223372036854) fail();
          previous = phase.phase;
        }
      }
    } else if (op.phase_timing_evidence !== "unknown_not_captured" || op.phase_timings !== undefined) fail();
    if (op.target_evidence === "native_cleanup_successful_prefix_only") {
      if (op.kind !== "cleanup" || !["succeeded", "failed", "partial"].includes(op.status) || !integer(op.completed_targets) || !integer(op.total_targets) || op.completed_targets > op.total_targets) fail();
    } else if (op.target_evidence !== "unknown_not_captured" || op.completed_targets !== undefined || op.total_targets !== undefined) fail();
    if (op.cleanup_target_evidence === "native_returned_target_prefix_not_resource_verification" || op.cleanup_target_evidence === "unavailable_invalid_or_over_budget") {
      if (op.kind !== "cleanup" || !["succeeded", "failed", "partial"].includes(op.status)) fail();
      if (op.cleanup_target_evidence === "unavailable_invalid_or_over_budget") {
        if (op.cleanup_targets !== undefined || op.cleanup_targets_truncated !== undefined) fail();
      } else {
        if (op.target_evidence !== "native_cleanup_successful_prefix_only" || op.total_targets < 1 || op.total_targets > 512 ||
          (op.status === "succeeded" && op.completed_targets !== op.total_targets) ||
          (op.status === "failed" && op.completed_targets !== 0) ||
          (op.status === "partial" && (op.completed_targets === 0 || op.completed_targets === op.total_targets)) ||
          !Array.isArray(op.cleanup_targets) || op.cleanup_targets.length !== Math.min(op.total_targets, 16) ||
          op.cleanup_targets_truncated !== (op.total_targets > 16)) fail();
        const targetIDs = new Set<string>();
        for (const [index, target] of op.cleanup_targets.entries()) {
          if (!object(target) || !keys(target, ["index", "id", "provider", "kind", "outcome"]) || target.index !== index ||
            typeof target.id !== "string" || !/^cleanup-result:[a-f0-9]{64}$/.test(target.id) || targetIDs.has(target.id) ||
            !(target.provider === "git" && target.kind === "worktree" || target.provider === "sbx" && target.kind === "sandbox" || target.provider === "tmux" && target.kind === "session" || target.provider === "workspace" && ["workspace", "missing_member"].includes(target.kind)) ||
            target.outcome !== (index < op.completed_targets ? "returned_success_unverified" : "not_completed_may_be_partial")) fail();
          targetIDs.add(target.id);
        }
      }
    } else if (op.cleanup_target_evidence !== "unknown_not_captured" || op.cleanup_targets !== undefined || op.cleanup_targets_truncated !== undefined) fail();
  }
}
