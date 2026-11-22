export const refreshLimitations = [
  "installation_wide_all_registered_collection_sources_no_subset",
  "native_collection_reconciliation_may_update_notes_bindings_lifecycle_and_deliver_events",
  "provider_reads_may_use_existing_authentication_and_refresh_git_metadata",
  "no_workspace_gc_cleanup_login_setup_or_runtime_start_requested",
  "latest_preview_only_expires_after_60_seconds_or_revision_drift",
  "plan_identity_is_freshness_not_user_consent",
  "apply_requires_fresh_exact_installation_target_confirmation",
  "consumed_plan_never_reexecutes_even_after_disconnect",
  "30_second_cooperative_native_deadline_not_rollback",
  "client_cancellation_after_dispatch_may_leave_partial_changes",
  "outcomes_are_returned_collection_attempts_not_reconciliation_or_resource_verification",
  "missing_or_cancelled_attempts_unverified_not_success_or_absence",
  "resource_refresh_pending_is_separate_from_remote_provider_success",
  "no_pre_restart_history_or_automatic_retry",
  "raw_errors_paths_notes_and_provider_payloads_withheld",
];

export type RefreshPreview = { planID: string; expiresAt: number; revision: number; feedbackID: string; targets: string[]; workspace: string };

// Validate every field before storing a preview or exposing mutation outcomes.
// A lost/invalid response after dispatch is unknown, never permission to retry.
export function validateRefresh(data: Record<string, any>, params: {action?: string; plan_id?: string}, preview?: RefreshPreview): void {
  const fail = (): never => { throw new Error("Invalid Radar refresh response; after apply the outcome is unknown, do not replay"); };
  const object = (v: any, keys: string[]) => !!v && typeof v === "object" && !Array.isArray(v) && Object.keys(v).every(k => keys.includes(k));
  const text = (v: any, n = 512) => typeof v === "string" && v.length > 0 && Buffer.byteLength(v, "utf8") <= n;
  const integer = (v: any) => Number.isSafeInteger(v) && v >= 0;
  const time = (v: any) => text(v, 35) && /^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d{1,9})?Z$/.test(v) && Number.isFinite(Date.parse(v)) && new Date(v).toISOString().slice(0,19) === v.slice(0,19);
  const mode = params.action ?? "preview";
  if (!object(data,["diagnostics_schema","section","scope","available","feedback_workspace_id","evidence","reason","maintenance"]) || data.diagnostics_schema !== 30 || data.section !== "refresh" || data.scope !== "installation" || !text(data.feedback_workspace_id)) fail();
  if (data.available === false) {
    if (data.evidence !== "unavailable" || data.maintenance !== undefined || !(mode === "apply" ? ["request_outcome_unknown_do_not_replay","daemon_unavailable"] : ["daemon_unavailable"]).includes(data.reason)) fail();
    return;
  }
  const v = data.maintenance;
  if (data.available !== true || data.reason !== undefined || data.evidence !== "native_collection_only_refresh" || !object(v,["diagnostics_schema","operation","mode","scope","evidence","status","problem","plan_id","expires_at","revision","operation_id","targets","counts","resource_refresh_pending","limitations"]) || v.diagnostics_schema !== 30 || v.operation !== "refresh" || v.mode !== mode || v.scope !== "installation" || v.evidence !== data.evidence || !text(v.plan_id,64) || !/^[a-f0-9]{64}$/.test(v.plan_id) || !time(v.expires_at) || !integer(v.revision) || typeof v.resource_refresh_pending !== "boolean" || !Array.isArray(v.targets) || v.targets.length < 1 || v.targets.length > 32 || !Array.isArray(v.limitations) || v.limitations.length !== refreshLimitations.length || new Set(v.limitations).size !== refreshLimitations.length || v.limitations.some((x: any) => !refreshLimitations.includes(x)) || Buffer.byteLength(JSON.stringify(v),"utf8") > 32*1024) fail();
  let previous = "";
  const counts = {complete:0,incomplete:0,skipped:0,unverified:0};
  for (const target of v.targets) {
    if (!object(target,["id","outcome"]) || !text(target.id,76) || !/^integration:[a-f0-9]{64}$/.test(target.id) || target.id <= previous || !["complete","incomplete","skipped_before_collection","cancelled_or_timed_out","unknown_not_attempted"].includes(target.outcome)) fail();
    previous = target.id;
    switch (target.outcome) { case "complete": counts.complete++; break; case "incomplete": counts.incomplete++; break; case "skipped_before_collection": counts.skipped++; break; default: counts.unverified++; }
  }
  if (!object(v.counts,["complete","incomplete","skipped","unverified"]) || Object.keys(v.counts).length !== 4 || Object.entries(counts).some(([k,n]) => v.counts[k] !== n)) fail();
  if (mode === "preview") {
    if (v.status !== "preview" || v.problem !== "none" || v.operation_id !== undefined || counts.unverified !== v.targets.length || Date.parse(v.expires_at) <= Date.now() || Date.parse(v.expires_at) > Date.now()+65_000) fail();
  } else {
    if (!preview || data.feedback_workspace_id !== preview.feedbackID || v.revision !== preview.revision || Date.parse(v.expires_at) !== preview.expiresAt || v.plan_id !== params.plan_id || preview.planID !== params.plan_id || v.targets.length !== preview.targets.length || v.targets.some((target: any,i: number) => target.id !== preview.targets[i]) || !text(v.operation_id) || !["running","returned"].includes(v.status) || !["none","native_error","cancelled_or_timed_out","incomplete_or_unverified"].includes(v.problem)) fail();
    if (v.status === "running" && (v.problem !== "none" || counts.unverified !== v.targets.length)) fail();
    if (v.status === "returned" && ((v.problem === "none" && (counts.incomplete > 0 || counts.unverified > 0)) || (v.problem === "incomplete_or_unverified" && counts.incomplete + counts.unverified === 0))) fail();
  }
}
