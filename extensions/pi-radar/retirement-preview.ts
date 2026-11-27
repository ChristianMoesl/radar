import { resolve } from "node:path";
import type { ExtensionAPI, ExtensionContext } from "@earendil-works/pi-coding-agent";
import { radarPackageVersion } from "./version.ts";

export const retirementLimitations = [
  "exact_registered_workspace_native_runtime_retirement_whole_ordered_bundle_no_subset",
  "preview_native_provider_reads_may_refresh_metadata_not_cleanup_or_execution_admission",
  "workspace_files_worktrees_branches_sandbox_local_state_registration_preserved",
  "sandbox_stop_interrupts_commands_services_terminal_removal_loses_processes_and_contents",
  "safety_notice_counts_not_private_messages_zero_automatic_blockers_not_execution_eligibility",
  "registered_owners_including_stopped_block_issuance_unknown_ownership_refuses",
  "preview_has_no_held_lease_runtime_ownership_or_fresh_authored_lifecycle_reservation",
  "latest_per_task_60_second_one_use_native_plan_is_freshness_not_consent",
  "apply_requires_separate_fresh_interactive_exact_target_whole_plan_confirmation",
  "original_native_retirement_mode_policy_time_and_fresh_lifecycle_resource_provider_guards_remain",
  "30_second_cooperative_deadline_disconnect_not_forced_cancellation_or_rollback",
  "returned_prefix_dispatch_and_runtime_flags_not_independent_resource_outcomes_or_safe_retry",
  "partial_cancelled_failed_or_unknown_outcomes_never_authorize_replay_retry_or_silent_replan",
  "current_daemon_exact_workspace_operation_and_resource_invalidation_not_complete_history",
  "private_paths_runtime_names_messages_operations_and_errors_withheld"
];
type Params = { section?: string; scope?: string; parent_id?: string; action?: string; plan_id?: string };
type Accepted = { workspace: string; session: string; scope: string; parent: string; disclosure: string; expires: number };
const failure = () => new Error("Invalid or unavailable native retirement response; the outcome may be unknown. Do not replay, automatically retry or silently replan.");
const object = (v: any, required: string[], optional: string[] = []): boolean => !!v && typeof v === "object" && !Array.isArray(v) && required.every(k => Object.hasOwn(v, k)) && Object.keys(v).every(k => required.includes(k) || optional.includes(k));
const text = (v: any, n = 512): boolean => typeof v === "string" && v.length > 0 && Buffer.byteLength(v, "utf8") <= n;
const integer = (v: any, max = Number.MAX_SAFE_INTEGER): boolean => Number.isSafeInteger(v) && v >= 0 && v <= max;
const time = (v: any): boolean => text(v, 35) && /^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d{1,9})?Z$/.test(v) && !v.startsWith("0000-") && Number.isFinite(Date.parse(v)) && new Date(v).toISOString().slice(0, 19) === v.slice(0, 19);

import { parseRecreationResponse as parseRetirementResponse } from "./recreation-preview.ts";

export function validateRetirementPreview(data: any, params: Params, accepted?: Accepted): void {
  const fail = (): never => { throw failure(); }, scope = params.scope ?? "origin", mode = params.action ?? "preview";
  if (!["origin", "feedback", "installation"].includes(scope) || !text(params.parent_id) || !object(data, ["diagnostics_schema", "section", "scope", "available", "feedback_workspace_id", "evidence"], ["origin_workspace_id", "reason", "retirement"]) || data.diagnostics_schema !== 41 || data.section !== "retirement_preview" || data.scope !== scope || !text(data.feedback_workspace_id) || (data.origin_workspace_id !== undefined && (scope !== "origin" || !text(data.origin_workspace_id)))) fail();
  if (data.available === false) {
    if (data.evidence !== "unavailable" || data.retirement !== undefined || !["origin_not_registered", "daemon_unavailable", "retirement_outcome_unknown_do_not_replay"].includes(data.reason) || (data.reason === "origin_not_registered" ? scope !== "origin" || data.origin_workspace_id !== undefined : scope === "origin" && !text(data.origin_workspace_id))) fail();
    if (mode === "apply") {
      if (!accepted) fail();
      const original = JSON.parse(accepted!.disclosure);
      if (data.feedback_workspace_id !== original.feedback_workspace_id || (data.reason !== "origin_not_registered" && data.origin_workspace_id !== original.origin_workspace_id)) fail();
    }
    return;
  }
  const v = data.retirement;
  if (data.available !== true || data.reason !== undefined || data.evidence !== "native_exact_workspace_retirement_plan_not_consent" || !object(v, ["diagnostics_schema", "operation", "mode", "scope", "feedback_workspace_id", "evidence", "plan_id", "expires_at", "revision", "proposal", "problem", "outcome", "resource_refresh_pending", "limitations"], ["scope_workspace_id", "operation_id", "completed_targets", "cleanup_execution"]) || v.diagnostics_schema !== 41 || v.operation !== "retire" || v.mode !== mode || v.scope !== scope || v.feedback_workspace_id !== data.feedback_workspace_id || v.evidence !== data.evidence || !text(v.plan_id, 64) || !/^[a-f0-9]{64}$/.test(v.plan_id) || !time(v.expires_at) || !integer(v.revision) || JSON.stringify(v.limitations) !== JSON.stringify(retirementLimitations) || Buffer.byteLength(JSON.stringify(v), "utf8") > 16 * 1024) fail();
  if (scope === "installation" ? v.scope_workspace_id !== undefined : v.scope_workspace_id !== params.parent_id) fail();
  if (scope === "feedback" && data.feedback_workspace_id !== params.parent_id) fail();
  if (scope === "origin" ? data.origin_workspace_id !== v.scope_workspace_id : data.origin_workspace_id !== undefined) fail();
  const p = v.proposal;
  if (!object(p,["id","record_id","task_id","stage","targets","preserved","execution_eligibility","ownership_evidence","agent_owner_count","lease_evidence"])||p.id!==params.parent_id||!text(p.record_id)||!integer(p.task_id)||p.task_id<1||p.stage!=="runtime_retirement"||!Array.isArray(p.targets)||p.targets.length<1||p.targets.length>3||JSON.stringify(p.preserved)!==JSON.stringify(["workspace_files","worktrees_and_branches","sandbox_and_local_state","registration"])||p.execution_eligibility!=="unknown_fresh_execution_guards_not_run"||p.ownership_evidence!=="fresh_bounded_registration"||p.agent_owner_count!==0||p.lease_evidence!=="unknown_not_checked")fail();
  const seen=new Set<string>(),ids=new Set<string>();
  for(const [i,t] of p.targets.entries()){
   if(!object(t,["id","provider","effect","safety_notice_count","automatic_blocker_count"])||typeof t.id!=="string"||!/^retirement-target:[a-f0-9]{64}$/.test(t.id)||seen.has(t.provider)||ids.has(t.id)||!integer(t.safety_notice_count,1024)||t.automatic_blocker_count!==0)fail();seen.add(t.provider);ids.add(t.id);
   const effects:Record<string,string>={tmux:"remove_terminal_session_and_processes",sbx:"stop_sandbox_commands_and_services",workspace:"retain_workspace_files_and_registration"};
   if(effects[t.provider]!==t.effect||t.provider==="workspace"&&i!==p.targets.length-1)fail();
  }
  if(!seen.has("workspace"))fail();
  if (mode === "preview") {
    if (params.plan_id !== undefined || v.outcome !== "not_attempted" || v.problem !== "none" || v.operation_id !== undefined || v.completed_targets!==undefined || v.cleanup_execution!==undefined || v.resource_refresh_pending !== false || Date.parse(v.expires_at) <= Date.now() || Date.parse(v.expires_at) > Date.now() + 65_000) fail();
  } else {
    if (mode !== "apply" || !accepted || v.plan_id !== params.plan_id || !text(v.operation_id) || v.resource_refresh_pending !== true) fail();
    if(!integer(v.completed_targets,p.targets.length))fail();
    if(v.outcome==="native_bundle_returned_unverified"?v.problem!=="none"||v.completed_targets!==p.targets.length:v.outcome!=="unverified_may_be_partial"||!["native_error","cancelled_or_timed_out"].includes(v.problem))fail();
    if(v.cleanup_execution!==undefined){const e=v.cleanup_execution,c=v.completed_targets,total=p.targets.length;if(!object(e,["stage","attempted_targets"])||!integer(e.attempted_targets,total)||e.attempted_targets<c)fail();const valid=e.stage==="completed"?c===total&&e.attempted_targets===total:e.stage==="provider_call"?c<total&&e.attempted_targets===c+1:e.stage==="target_dispatch"?c<total&&e.attempted_targets===c:["admission","ownership_and_leases","bundle_preflight"].includes(e.stage)&&c===0&&e.attempted_targets===0;if(!valid)fail();}
    const original = JSON.parse(accepted!.disclosure);
    if (["feedback_workspace_id", "origin_workspace_id", "scope"].some(k => data[k] !== original[k]) || ["plan_id", "expires_at", "revision", "scope_workspace_id", "feedback_workspace_id", "scope"].some(k => v[k] !== original.retirement[k]) || JSON.stringify(p) !== JSON.stringify(original.retirement.proposal)) fail();
  }
}

// Only an immutable UI disclosure binding, not another native plan cache or
// execution path. Any new request/session update consumes the previous binding.
export class RetirementSession {
  private preview: Accepted | undefined;
  private generation = 0;
  clear() { this.preview = undefined; this.generation++; }
  async execute(pi: ExtensionAPI, ctx: ExtensionContext, request: Params, signal: AbortSignal | undefined, available: () => boolean) {
    const accepted = this.preview; this.clear(); const generation = this.generation;
    const params = { ...request }, workspace = resolve(ctx.cwd), scope = params.scope ?? "origin", action = params.action ?? "preview";
    if (Object.keys(params).some(k => !["section", "scope", "parent_id", "action", "plan_id"].includes(k)) || params.section !== "retirement_preview" || !text(params.parent_id) || !["origin", "feedback", "installation"].includes(scope) || !["preview", "apply"].includes(action) || (action === "preview" ? params.plan_id !== undefined : typeof params.plan_id !== "string" || !/^[a-f0-9]{64}$/.test(params.plan_id))) throw new Error("Retirement requires one exact registered workspace and preview or exact-plan apply; no page/subset selectors");
    signal?.throwIfAborted();
    if (!available() || !ctx.hasUI) throw new Error("Retirement requires a feedback workspace and fresh interactive confirmation");
    const session = ctx.sessionManager.getSessionId();
    if (!text(session)) throw new Error("Retirement requires an identified interactive Pi session");
    if (action === "apply" && (!accepted || accepted.workspace !== workspace || accepted.session !== session || accepted.scope !== scope || accepted.parent !== params.parent_id || accepted.expires <= Date.now() || JSON.parse(accepted.disclosure).retirement.plan_id !== params.plan_id)) throw new Error("Retirement requires the latest complete unexpired retirement preview in this feedback workspace and Pi session; a plan is not consent");
    const target = `Exact registered workspace ${JSON.stringify(params.parent_id)}, scope ${JSON.stringify(scope)}, feedback workspace ${JSON.stringify(workspace)}.`;
    const body = action === "apply"
      ? `${target} SEPARATE fresh destructive approval for this complete immutable ordered target/effect/safety-count disclosure: ${accepted!.disclosure}. Stop sandbox commands/services and remove terminal sessions/processes/contents only as proposed; preserve workspace files, worktrees/branches, sandbox/local state and registration. Private safety messages are withheld; zero automatic blockers is not full eligibility. Native fresh authored-lifecycle, ownership/activity/lease/provider guards remain authoritative. Original retirement policy/time/mode are retained, never changed to manual cleanup or destructive expiry. Up to 30 seconds cooperatively; cancellation/disconnect is not forced cancellation or rollback and may leave partial effects. Returned prefix/dispatch evidence is not independent resource verification or safe retry. No automatic retry or replay and no silent replan. Approve only this exact disruptive plan, not later maintenance.`
      : `${target} Run the shared exact native runtime-retirement planner once, never the installation-wide GC sweep, with a 30 second cooperative budget. Native provider reads may inspect registered runtimes and refresh metadata; no provider Cleanup or execution admission runs. Registered owner counts include stopped owners, which block issuance; unknown ownership refuses. No held lease, runtime ownership or fresh authored-lifecycle reservation; native final execution guards still apply. Disclose the COMPLETE selected ordered bundle, proposed sandbox stop/session removal, safety notice counts (private messages withheld) and preserved files/worktrees/branches/sandbox-local state/registration. This is not the registration-only inspection. A one-use latest-per-task plan expires after 60 seconds or drift and is freshness, never consent. No ordinary refresh/GC, cleanup, login or explicit runtime start is requested. Apply requires SEPARATE fresh exact-target whole-plan destructive confirmation. No automatic retry or replay.`;
    const approved = await ctx.ui.confirm(action === "apply" ? "Retire exact workspace runtimes?" : "Preview exact native runtime retirement?", body);
    signal?.throwIfAborted();
    if (approved !== true) return { content: [{ type: "text" as const, text: "Retirement cancelled" }], details: { cancelled: true } };
    const live = () => available() && ctx.hasUI && resolve(ctx.cwd) === workspace && ctx.sessionManager.getSessionId() === session && generation === this.generation;
    if (!live()) throw new Error("Feedback workspace or Pi session changed during confirmation; no retirement requested");
    if (action === "apply" && accepted!.expires <= Date.now()) throw new Error("Retirement preview expired during confirmation; preview and separately confirm again");
    const args = ["feedback-diagnostics", "--workspace", workspace, "--section", "retirement_preview", "--json", "--scope", scope, "--parent-id", params.parent_id!, "--action", action];
    if (params.plan_id !== undefined) args.push("--plan-id", params.plan_id);
    let data: any;
    try {
      const result = await pi.exec(process.env.RADAR_BINARY?.trim() || "radar", args, { signal, timeout: 35_000 });
      signal?.throwIfAborted();
      if (!live() || result.code !== 0) throw failure();
      data = parseRetirementResponse(result.stdout);
      validateRetirementPreview(data, params, action === "apply" ? accepted : undefined);
    } catch { throw failure(); }
    if (action === "preview" && data.available === true) this.preview = { workspace, session, scope, parent: params.parent_id!, disclosure: JSON.stringify(data), expires: Date.parse(data.retirement.expires_at) };
    data.loaded_pi_radar_version = radarPackageVersion;
    return { content: [{ type: "text" as const, text: JSON.stringify(data) }], details: data };
  }
}
