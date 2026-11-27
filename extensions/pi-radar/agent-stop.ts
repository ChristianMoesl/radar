import { resolve } from "node:path";
import type { ExtensionAPI, ExtensionContext } from "@earendil-works/pi-coding-agent";
import { parseRecreationResponse } from "./recreation-preview.ts";
import { radarPackageVersion } from "./version.ts";

export const agentStopLimitations = [
  "one_exact_current_task_agent_ref_and_registered_feedback_scope_no_subset",
  "preview_reads_native_registration_workspace_and_runner_owner_evidence_without_startup_or_shutdown",
  "running_and_desired_before_are_capture_facts_not_post_apply_state",
  "apply_sets_desired_false_and_uses_existing_native_shutdown_wait_may_interrupt_work",
  "stopped_registration_still_pins_workspace_data_sandbox_and_ownership_preserved",
  "latest_per_task_shared_maintenance_plan_one_use_original_native_30_second_expiry",
  "plan_identity_is_freshness_not_user_consent",
  "trusted_caller_requires_separate_fresh_interactive_exact_target_interruption_consent",
  "captured_final_guard_rechecks_scope_revision_publication_and_native_identity_before_effects",
  "point_in_time_guards_not_atomic_filesystem_or_collection_transactions_or_peer_pid_attestation",
  "desired_state_write_may_precede_failure_cancellation_or_lost_reply_no_rollback_or_safe_retry",
  "native_callback_and_handler_return_not_completed_work_process_exit_lease_release_or_resource_absence",
  "resource_refresh_pending_requires_independent_recollection_not_verified_resource_outcomes",
  "no_automatic_replay_retry_silent_replan_start_restart_remove_or_sandbox_stop",
  "current_daemon_managed_handler_evidence_not_complete_or_durable_history",
  "private_paths_pids_runtime_names_checkpoints_tokens_errors_and_provider_payloads_withheld",
];
type Params = { section?: string; scope?: string; parent_id?: string; agent_id?: string; action?: string; plan_id?: string };
type Accepted = { workspace: string; session: string; scope: string; parent: string; agent: string; disclosure: string; expires: number };
const failure = () => new Error("Invalid or unavailable native agent-stop response; outcome may be unknown. Do not replay, automatically retry or silently replan.");
const object = (v: any, required: string[], optional: string[] = []): boolean => !!v && typeof v === "object" && !Array.isArray(v) && required.every(k => Object.hasOwn(v, k)) && Object.keys(v).every(k => required.includes(k) || optional.includes(k));
const text = (v: any, n = 512): boolean => typeof v === "string" && v.length > 0 && Buffer.byteLength(v, "utf8") <= n && !/[\x00-\x1f\x7f]/.test(v);
const integer = (v: any): boolean => Number.isSafeInteger(v) && v >= 0;
const time = (v: any): boolean => text(v, 35) && /^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d{1,9})?Z$/.test(v) && !v.startsWith("0000-") && Number.isFinite(Date.parse(v)) && new Date(v).toISOString().slice(0, 19) === v.slice(0, 19);
const boundFields = ["plan_id", "expires_at", "revision", "scope", "record_id", "task_id", "agent_id", "feedback_workspace_id", "scope_workspace_id", "agent_workspace_id", "desired_before", "running_before"];

export function validateAgentStop(data: any, params: Params, accepted?: Accepted): void {
  const fail = (): never => { throw failure(); }, scope = params.scope === undefined ? "origin" : params.scope, mode = params.action === undefined ? "preview" : params.action;
  if (!object(data, ["diagnostics_schema", "section", "scope", "available", "feedback_workspace_id", "evidence"], ["origin_workspace_id", "reason", "agent_stop"]) || data.diagnostics_schema !== 39 || data.section !== "agent_stop" || data.scope !== scope || !text(data.feedback_workspace_id) || (data.origin_workspace_id !== undefined && (scope !== "origin" || !text(data.origin_workspace_id)))) fail();
  if (data.available === false) {
    if (data.evidence !== "unavailable" || data.agent_stop !== undefined || !["origin_not_registered", "daemon_unavailable", "agent_stop_outcome_unknown_do_not_replay"].includes(data.reason) || (data.reason === "origin_not_registered" ? scope !== "origin" || data.origin_workspace_id !== undefined : scope === "origin" && !text(data.origin_workspace_id))) fail();
    if (mode === "apply") {
      if (!accepted) fail();
      const original = JSON.parse(accepted!.disclosure);
      if (data.feedback_workspace_id !== original.feedback_workspace_id || (data.reason !== "origin_not_registered" && data.origin_workspace_id !== original.origin_workspace_id)) fail();
    }
    return;
  }
  const v = data.agent_stop;
  if (data.available !== true || data.reason !== undefined || data.evidence !== "native_exact_agent_stop_plan_not_consent" || !object(v, ["diagnostics_schema", "operation", "mode", "scope", "record_id", "task_id", "agent_id", "feedback_workspace_id", "agent_workspace_id", "evidence", "plan_id", "expires_at", "revision", "desired_before", "running_before", "dispatch_evidence", "problem", "outcome", "resource_refresh_pending", "limitations"], ["scope_workspace_id", "operation_id"]) || v.diagnostics_schema !== 39 || v.operation !== "agent_stop" || v.mode !== mode || v.scope !== scope || !text(v.record_id) || !integer(v.task_id) || v.task_id < 1 || (params.parent_id !== v.record_id && params.parent_id !== String(v.task_id)) || v.agent_id !== params.agent_id || typeof v.agent_id !== "string" || !/^[a-z0-9][a-z0-9_-]{0,63}$/.test(v.agent_id) || v.feedback_workspace_id !== data.feedback_workspace_id || !text(v.agent_workspace_id) || v.evidence !== data.evidence || typeof v.plan_id !== "string" || !/^[a-f0-9]{64}$/.test(v.plan_id) || !time(v.expires_at) || !integer(v.revision) || typeof v.desired_before !== "boolean" || typeof v.running_before !== "boolean" || JSON.stringify(v.limitations) !== JSON.stringify(agentStopLimitations) || Buffer.byteLength(JSON.stringify(v), "utf8") > 32 * 1024) fail();
  if (scope === "installation" ? v.scope_workspace_id !== undefined : !text(v.scope_workspace_id) || v.scope_workspace_id !== v.agent_workspace_id) fail();
  if (scope === "feedback" && v.scope_workspace_id !== data.feedback_workspace_id) fail();
  if (scope === "origin" ? data.origin_workspace_id !== v.scope_workspace_id : data.origin_workspace_id !== undefined) fail();
  if (mode === "preview") {
    if (params.plan_id !== undefined || v.outcome !== "not_attempted" || v.problem !== "none" || v.dispatch_evidence !== "not_dispatched" || v.operation_id !== undefined || v.resource_refresh_pending !== false || Date.parse(v.expires_at) <= Date.now() || Date.parse(v.expires_at) > Date.now() + 35_000) fail();
  } else {
    if (mode !== "apply" || !accepted || v.plan_id !== params.plan_id || !text(v.operation_id) || v.resource_refresh_pending !== true || v.dispatch_evidence !== "native_stop_callback_entered_not_process_outcome") fail();
    if (v.outcome === "native_handler_returned_unverified" ? v.problem !== "none" : v.outcome !== "unverified_may_be_partial" || !["native_error", "cancelled_or_timed_out"].includes(v.problem)) fail();
    const original = JSON.parse(accepted!.disclosure);
    if (["scope", "feedback_workspace_id", "origin_workspace_id"].some(k => data[k] !== original[k]) || boundFields.some(k => v[k] !== original.agent_stop[k])) fail();
  }
}

// Only the immutable disclosure shown in this interactive session, not another
// native plan cache. Every attempt consumes it before validation or approval.
export class AgentStopSession {
  private preview: Accepted | undefined;
  private generation = 0;
  clear() { this.preview = undefined; this.generation++; }
  async execute(pi: ExtensionAPI, ctx: ExtensionContext, request: Params, signal: AbortSignal | undefined, available: () => boolean) {
    const accepted = this.preview; this.clear(); const generation = this.generation;
    const params = { ...request }, workspace = resolve(ctx.cwd), scope = params.scope === undefined ? "origin" : params.scope, action = params.action === undefined ? "preview" : params.action;
    if (Object.keys(params).some(k => !["section", "scope", "parent_id", "agent_id", "action", "plan_id"].includes(k)) || params.section !== "agent_stop" || !text(params.parent_id) || typeof params.agent_id !== "string" || !/^[a-z0-9][a-z0-9_-]{0,63}$/.test(params.agent_id) || !["origin", "feedback", "installation"].includes(scope) || !["preview", "apply"].includes(action) || (action === "preview" ? params.plan_id !== undefined : typeof params.plan_id !== "string" || !/^[a-f0-9]{64}$/.test(params.plan_id))) throw new Error("Agent stop requires one exact retained task, registered agent, scope and preview or exact-plan apply; no page/subset or generic-control selectors");
    signal?.throwIfAborted();
    if (!available() || !ctx.hasUI) throw new Error("Agent stop requires a feedback workspace and fresh interactive confirmation");
    const session = ctx.sessionManager.getSessionId();
    if (!text(session)) throw new Error("Agent stop requires an identified interactive Pi session");
    if (action === "apply" && (!accepted || accepted.workspace !== workspace || accepted.session !== session || accepted.scope !== scope || accepted.parent !== params.parent_id || accepted.agent !== params.agent_id || accepted.expires <= Date.now() || JSON.parse(accepted.disclosure).agent_stop.plan_id !== params.plan_id)) throw new Error("Agent stop requires the latest complete unexpired preview in this feedback workspace and Pi session; a plan is not consent");
    const target = `Exact retained task ${JSON.stringify(params.parent_id)}, registered agent ${JSON.stringify(params.agent_id)}, scope ${JSON.stringify(scope)}, feedback workspace ${JSON.stringify(workspace)}.`;
    const body = action === "apply"
      ? `${target} SEPARATE fresh interruption approval for this complete immutable disclosure: ${accepted!.disclosure}. Set this exact agent's desired state false and invoke its existing native shutdown/wait path; this may interrupt unfinished work. Preserve registration, workspace data and sandbox. The stopped registration still pins workspace ownership; stop is not remove, start, restart or sandbox stop. Captured desired_before/running_before are before-execution facts, not current state. Native exact registration/runner-owner and final task/feedback/scope/revision/publication guards remain authoritative. A plan ID is freshness, never consent. Up to 30 seconds cooperatively; desired-state persistence may precede later failure, cancellation or a lost reply, with no rollback or safe retry. Handler return is not completed work, process exit, checkpoint success, released leases or resource absence; resource refresh requires independent recollection. No automatic retry or replay and no silent replan. Approve only this exact interruption, not later maintenance.`
      : `${target} Capture the exact native stop plan once, reading bounded registration, registered workspace, runner socket/snapshot and independent owner evidence. No daemon startup, desired-state write, shutdown, workspace preparation, provider refresh, login or maintenance is requested by preview. The private native guard checks point-in-time identity, not OS peer/PID-incarnation attestation or an atomic filesystem transaction. A latest-per-task shared one-use plan retains the original native monotonic 30-second expiry, not a reservation or consent. A later apply sets desired false and may interrupt unfinished work; preserve registration/data/sandbox and the stopped ownership hold. Applying requires SEPARATE fresh exact-target interactive interruption approval of the complete immutable latest disclosure. No automatic retry or replay.`;
    const approved = await ctx.ui.confirm(action === "apply" ? "Interrupt and stop this exact registered agent?" : "Preview exact native agent stop?", body);
    signal?.throwIfAborted();
    if (approved !== true) return { content: [{ type: "text" as const, text: "Agent stop cancelled" }], details: { cancelled: true } };
    const live = () => available() && ctx.hasUI && resolve(ctx.cwd) === workspace && ctx.sessionManager.getSessionId() === session && generation === this.generation;
    if (!live()) throw new Error("Feedback workspace or Pi session changed during confirmation; no agent stop requested");
    if (action === "apply" && accepted!.expires <= Date.now()) throw new Error("Agent-stop preview expired during confirmation; preview and separately confirm again");
    const args = ["feedback-diagnostics", "--workspace", workspace, "--section", "agent_stop", "--json", "--scope", scope, "--parent-id", params.parent_id!, "--agent-id", params.agent_id!, "--action", action];
    if (params.plan_id !== undefined) args.push("--plan-id", params.plan_id);
    let data: any;
    try {
      const result = await pi.exec(process.env.RADAR_BINARY?.trim() || "radar", args, { signal, timeout: 35_000 });
      signal?.throwIfAborted();
      if (!live() || result.code !== 0) throw failure();
      data = parseRecreationResponse(result.stdout);
      validateAgentStop(data, params, action === "apply" ? accepted : undefined);
    } catch { throw failure(); }
    if (action === "preview" && data.available === true) this.preview = { workspace, session, scope, parent: params.parent_id!, agent: params.agent_id!, disclosure: JSON.stringify(data), expires: Date.parse(data.agent_stop.expires_at) };
    data.loaded_pi_radar_version = radarPackageVersion;
    return { content: [{ type: "text" as const, text: JSON.stringify(data) }], details: data };
  }
}
