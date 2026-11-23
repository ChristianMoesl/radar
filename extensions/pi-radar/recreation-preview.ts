// Only a finite exact-workspace proposal may reach model text/details. Native
// plan IDs, private reasons/recipes and execution authority are never returned.
export const recreationPreviewLimitations = [
  "explicit_exact_registered_workspace_native_recreation_proposal_only",
  "native_preflight_runtime_ports_secret_and_policy_metadata_reads",
  "native_local_mount_kit_and_env_file_readability_checks_not_contents",
  "sbx_read_commands_may_auto_start_its_daemon_not_a_sandbox",
  "private_registration_and_recipe_drift_checks_not_atomic_reservation",
  "ownership_activity_and_leases_require_fresh_execution_guards",
  "proposed_recreation_discards_sandbox_local_state_and_interrupts_work",
  "native_workspace_files_registration_mounts_ports_and_prior_state_intended_preserved",
  "blocked_reasons_and_private_names_paths_payloads_and_errors_withheld",
  "preview_identity_is_not_native_apply_plan_or_consent",
  "no_recreation_cleanup_login_explicit_start_or_automatic_retry",
  "30_second_cooperative_budget_not_backend_health_or_resource_outcome",
];

export function validateRecreationPreview(data: any, params: {scope?: string; parent_id?: string}): void {
  const fail = (): never => { throw new Error("Invalid Radar recreation proposal evidence"); };
  const object = (v: any, keys: string[]): boolean => v !== null && typeof v === "object" && !Array.isArray(v) && Object.keys(v).every(key => keys.includes(key));
  const text = (v: any, max = 512): boolean => typeof v === "string" && v.length > 0 && Buffer.byteLength(v, "utf8") <= max;
  const integer = (v: any): boolean => Number.isSafeInteger(v) && v >= 0;
  const scope = params.scope ?? "origin";
  if (!object(data, ["diagnostics_schema", "section", "scope", "available", "feedback_workspace_id", "origin_workspace_id", "evidence", "reason", "inventory"]) || data.diagnostics_schema !== 32 || data.section !== "recreation_preview" || data.scope !== scope || !text(data.feedback_workspace_id) || (data.origin_workspace_id !== undefined && (scope !== "origin" || !text(data.origin_workspace_id)))) fail();
  if (data.available === false) {
    if (data.evidence !== "unavailable" || data.inventory !== undefined || !["origin_not_registered", "daemon_unavailable", "snapshot_changed_restart_pagination"].includes(data.reason) || (data.reason === "origin_not_registered" && (scope !== "origin" || data.origin_workspace_id !== undefined))) fail();
    return;
  }
  const evidence = "explicit_native_exact_workspace_recreation_proposal";
  const page = data.inventory;
  if (data.available !== true || data.reason !== undefined || data.evidence !== evidence || (scope === "origin" && data.origin_workspace_id !== params.parent_id) || (scope === "feedback" && data.feedback_workspace_id !== params.parent_id) || !object(page, ["diagnostics_schema", "section", "evidence", "observed_at", "revision", "snapshot", "total", "items", "next_cursor", "pagination_complete", "resource_refresh_pending", "limitations"])) fail();
  if (page.diagnostics_schema !== 32 || page.section !== "recreation_preview" || page.evidence !== evidence || !text(page.observed_at, 35) || !/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d{1,9})?Z$/.test(page.observed_at) || page.observed_at.startsWith("0000-") || !Number.isFinite(Date.parse(page.observed_at)) || new Date(page.observed_at).toISOString().slice(0, 19) !== page.observed_at.slice(0, 19) || !integer(page.revision) || !text(page.snapshot, 64) || !/^[a-f0-9]{64}$/.test(page.snapshot) || page.resource_refresh_pending !== false || page.total !== 1 || !Array.isArray(page.items) || page.items.length !== 1 || page.pagination_complete !== true || page.next_cursor !== undefined || !Array.isArray(page.limitations) || page.limitations.length !== recreationPreviewLimitations.length || new Set(page.limitations).size !== recreationPreviewLimitations.length || page.limitations.some((v: any) => !recreationPreviewLimitations.includes(v))) fail();
  const row = page.items[0], view = row?.recreation_preview;
  if (!object(row, ["id", "recreation_preview"]) || row.id !== params.parent_id || !object(view, ["id", "preview_id", "status", "previous_state", "runtime_identity_observed", "execution_eligibility"]) || view.id !== params.parent_id || typeof view.preview_id !== "string" || !/^recreation-preview:[a-f0-9]{64}$/.test(view.preview_id) || !["recreate", "blocked", "skipped"].includes(view.status) || !["running", "stopped", "unknown"].includes(view.previous_state) || typeof view.runtime_identity_observed !== "boolean" || view.execution_eligibility !== "unknown_fresh_execution_guards_not_run") fail();
  if ((!view.runtime_identity_observed && view.previous_state !== "unknown") || (view.status === "recreate" && (!view.runtime_identity_observed || view.previous_state === "unknown")) || (view.status === "skipped" && (view.runtime_identity_observed || view.previous_state !== "unknown"))) fail();
}
