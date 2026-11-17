// Validate the exact privacy allowlist, not a generic job/status payload.
export function validateOperations(data: Record<string, unknown>, params: { scope?: string; id?: string; limit?: number; cursor?: string }): void {
  function fail(): never { throw new Error("Invalid Radar managed-operation diagnostics"); }
  const object = (v: unknown): v is Record<string, any> => !!v && typeof v === "object" && !Array.isArray(v);
  const text = (v: unknown, max = 512): v is string => typeof v === "string" && v.length > 0 && Buffer.byteLength(v, "utf8") <= max;
  const integer = (v: unknown): v is number => Number.isSafeInteger(v) && (v as number) >= 0;
  const keys = (v: Record<string, any>, allowed: string[]) => Object.keys(v).every(key => allowed.includes(key));
  const scope = params.scope ?? "origin";
  if (!keys(data, ["diagnostics_schema", "section", "scope", "available", "feedback_workspace_id", "origin_workspace_id", "evidence", "reason", "inventory"]) || data.diagnostics_schema !== 21 || data.section !== "operations" || data.scope !== scope || !text(data.feedback_workspace_id) || (data.origin_workspace_id !== undefined && !text(data.origin_workspace_id))) fail();
  if (data.available === false) {
    if (data.evidence !== "unavailable" || data.inventory !== undefined || !["origin_not_registered", "daemon_unavailable", "snapshot_changed_restart_pagination"].includes(String(data.reason)) || (data.reason === "origin_not_registered" && scope !== "origin")) fail();
    return;
  }
  const evidence = "volatile_current_daemon_managed_operation_observations";
  const page = data.inventory;
  const limitations = ["volatile_current_daemon_only_no_pre_restart_history", "active_plus_latest_64_terminal_or_reported_observations_not_complete_history", "observed_at_is_snapshot_read_time_not_operation_time", "workspace_scope_uses_cached_retained_refs_not_verified_ownership", "task_scope_can_include_effects_in_other_workspaces", "client_reports_are_unverified_not_daemon_execution", "handler_success_is_not_resource_verification_or_reconciliation", "cleanup_counts_are_successful_prefix_not_failed_or_remaining_target_identities", "running_counts_timings_retries_and_lock_waits_unknown", "untracked_gc_runtime_and_remote_operations_not_covered", "no_replay_refresh_probe_or_maintenance", "raw_errors_paths_payloads_and_target_details_withheld"];
  if (data.available !== true || data.reason !== undefined || data.evidence !== evidence || (scope === "origin" && !text(data.origin_workspace_id)) || !object(page)) fail();
  if (!keys(page, ["diagnostics_schema", "section", "evidence", "observed_at", "revision", "snapshot", "total", "items", "next_cursor", "pagination_complete", "resource_refresh_pending", "limitations"]) || Buffer.byteLength(JSON.stringify(page), "utf8") > 32 * 1024 || page.diagnostics_schema !== 21 || page.section !== "operations" || page.evidence !== evidence || !text(page.observed_at, 35) || !/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d{1,9})?Z$/.test(page.observed_at) || !Number.isFinite(Date.parse(page.observed_at)) || new Date(page.observed_at).toISOString().slice(0, 19) !== page.observed_at.slice(0, 19) || !integer(page.revision) || !text(page.snapshot, 64) || !/^[0-9a-f]{64}$/.test(page.snapshot) || typeof page.resource_refresh_pending !== "boolean" || !integer(page.total) || !Array.isArray(page.items) || page.items.length > (params.limit ?? 20) || page.items.length > page.total || typeof page.pagination_complete !== "boolean") fail();
  if (!Array.isArray(page.limitations) || page.limitations.length !== limitations.length || new Set(page.limitations).size !== limitations.length || page.limitations.some((v: unknown) => typeof v !== "string" || !limitations.includes(v))) fail();
  if (page.pagination_complete ? page.next_cursor !== undefined : !text(page.next_cursor)) fail();
  if ((page.total > 0 && page.items.length === 0) || (!params.cursor && (page.pagination_complete ? page.items.length !== page.total : page.items.length >= page.total)) || (params.id !== undefined && page.total > 1)) fail();
  const kinds = ["unknown", "cleanup", "workspace", "task-create", "task-done", "task-reopen", "task-priority", "task-mute", "task-unmute", "task-title", "task-note", "task-delete"];
  const states: Record<string, string> = { unknown: "unknown_not_captured", running: "daemon_handler_running", reported: "unverified_client_report_not_daemon_execution", succeeded: "daemon_handler_result_not_resource_verification", failed: "daemon_handler_result_not_resource_verification", partial: "daemon_handler_result_not_resource_verification" };
  const seen = new Set<string>();
  for (const row of page.items) {
    if (!object(row) || !keys(row, ["id", "operation"]) || !text(row.id) || seen.has(row.id) || (params.id !== undefined && row.id !== params.id) || !object(row.operation)) fail();
    seen.add(row.id);
    const op = row.operation;
    if (!keys(op, ["id", "kind", "status", "task_id", "workspace_id", "execution_evidence", "target_evidence", "completed_targets", "total_targets"]) || op.id !== row.id || !kinds.includes(op.kind) || !Object.hasOwn(states, op.status) || op.execution_evidence !== states[op.status] || (op.task_id !== undefined && (!integer(op.task_id) || op.task_id < 1)) || (op.workspace_id !== undefined && !text(op.workspace_id))) fail();
    if ((op.status === "reported") !== (op.kind === "workspace")) fail();
    if (op.target_evidence === "native_cleanup_successful_prefix_only") {
      if (op.kind !== "cleanup" || !["succeeded", "failed", "partial"].includes(op.status) || !integer(op.completed_targets) || !integer(op.total_targets) || op.completed_targets > op.total_targets) fail();
    } else if (op.target_evidence !== "unknown_not_captured" || op.completed_targets !== undefined || op.total_targets !== undefined) fail();
  }
}
