import { resolve } from "node:path";
import type { ExtensionAPI, ExtensionContext } from "@earendil-works/pi-coding-agent";
import { radarPackageVersion } from "./version.ts";

export const expiryLimitations = [
  "exact_registered_seven_day_native_expiry_whole_ordered_bundle_no_subset",
  "preview_native_provider_reads_may_refresh_metadata_not_cleanup_or_execution_admission",
  "destructive_expiry_discards_workspace_local_changes_unknown_files_sandbox_state_and_eligible_local_branches",
  "workspace_registration_removed_completed_canonical_note_may_be_archived_remote_repositories_preserved",
  "expiring_data_warnings_do_not_bypass_structural_ownership_activity_or_lease_guards",
  "safety_notice_counts_not_private_messages_zero_automatic_blockers_not_execution_eligibility",
  "registered_owners_including_stopped_block_issuance_unknown_ownership_refuses",
  "preview_has_no_held_lease_runtime_ownership_or_fresh_authored_lifecycle_reservation",
  "latest_per_task_60_second_one_use_native_plan_is_freshness_not_consent",
  "trusted_caller_must_obtain_separate_fresh_exact_target_whole_plan_destructive_consent",
  "original_native_expiry_mode_policy_time_and_fresh_lifecycle_resource_provider_guards_remain",
  "30_second_cooperative_deadline_disconnect_not_forced_cancellation_or_rollback",
  "returned_prefix_dispatch_and_runtime_flags_not_independent_resource_outcomes_or_safe_retry",
  "partial_cancelled_failed_or_unknown_outcomes_never_authorize_replay_retry_or_silent_replan",
  "current_daemon_exact_workspace_operation_and_resource_invalidation_not_complete_history",
  "private_paths_runtime_names_branches_messages_operations_and_errors_withheld"
];
export const expiryDataLoss = ["workspace_local_changes_and_unknown_files","sandbox_local_state_and_runtime_processes","eligible_local_branches_including_unpublished_commits","workspace_registration"];
type Params = { section?: string; scope?: string; workspace_id?: string; cleanup_mode?: string; action?: string; plan_id?: string };
type Accepted = { workspace: string; session: string; scope: string; parent: string; disclosure: string; expires: number };
const failure = () => new Error("Invalid or unavailable native expiry response; the outcome may be unknown. Do not replay, automatically retry or silently replan.");
const object = (v: any, required: string[], optional: string[] = []): boolean => !!v && typeof v === "object" && !Array.isArray(v) && required.every(k => Object.hasOwn(v, k)) && Object.keys(v).every(k => required.includes(k) || optional.includes(k));
const text = (v: any, n = 512): boolean => typeof v === "string" && v.length > 0 && Buffer.byteLength(v, "utf8") <= n;
const integer = (v: any, max = Number.MAX_SAFE_INTEGER): boolean => Number.isSafeInteger(v) && v >= 0 && v <= max;
const time = (v: any): boolean => text(v, 35) && /^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d{1,9})?Z$/.test(v) && !v.startsWith("0000-") && Number.isFinite(Date.parse(v)) && new Date(v).toISOString().slice(0, 19) === v.slice(0, 19);

import { parseRecreationResponse as parseExpiryResponse } from "./recreation-preview.ts";

export function validateExpiryPreview(data: any, params: Params, accepted?: Accepted): void {
  const fail = (): never => { throw failure(); }, scope = params.scope ?? "origin", mode = params.action ?? "preview";
  if (!["origin", "feedback", "installation"].includes(scope) || !text(params.workspace_id) || !object(data, ["diagnostics_schema", "section", "scope", "available", "feedback_workspace_id", "evidence"], ["origin_workspace_id", "reason", "expiry"]) || data.diagnostics_schema !== 37 || data.section !== "cleanup_preview" || data.scope !== scope || !text(data.feedback_workspace_id) || (data.origin_workspace_id !== undefined && (scope !== "origin" || !text(data.origin_workspace_id)))) fail();
  if (data.available === false) {
    if (data.evidence !== "unavailable" || data.expiry !== undefined || !["origin_not_registered", "daemon_unavailable", "expiry_outcome_unknown_do_not_replay"].includes(data.reason) || (data.reason === "origin_not_registered" ? scope !== "origin" || data.origin_workspace_id !== undefined : scope === "origin" && !text(data.origin_workspace_id))) fail();
    if (mode === "apply") {
      if (!accepted) fail();
      const original = JSON.parse(accepted!.disclosure);
      if (data.feedback_workspace_id !== original.feedback_workspace_id || (data.reason !== "origin_not_registered" && data.origin_workspace_id !== original.origin_workspace_id)) fail();
    }
    return;
  }
  const v = data.expiry;
  if (data.available !== true || data.reason !== undefined || data.evidence !== "native_exact_workspace_expiry_plan_not_consent" || !object(v, ["diagnostics_schema", "operation", "mode", "scope", "feedback_workspace_id", "evidence", "plan_id", "expires_at", "revision", "proposal", "problem", "outcome", "resource_refresh_pending", "limitations"], ["scope_workspace_id", "operation_id", "completed_targets", "cleanup_execution"]) || v.diagnostics_schema !== 37 || v.operation !== "expire" || v.mode !== mode || v.scope !== scope || v.feedback_workspace_id !== data.feedback_workspace_id || v.evidence !== data.evidence || !text(v.plan_id, 64) || !/^[a-f0-9]{64}$/.test(v.plan_id) || !time(v.expires_at) || !integer(v.revision) || JSON.stringify(v.limitations) !== JSON.stringify(expiryLimitations) || Buffer.byteLength(JSON.stringify(v), "utf8") > 32 * 1024) fail();
  if (scope === "installation" ? v.scope_workspace_id !== undefined : v.scope_workspace_id !== params.workspace_id) fail();
  if (scope === "feedback" && data.feedback_workspace_id !== params.workspace_id) fail();
  if (scope === "origin" ? data.origin_workspace_id !== v.scope_workspace_id : data.origin_workspace_id !== undefined) fail();
  const p = v.proposal;
  if (!object(p,["id","record_id","task_id","stage","expiry_at","targets","preserved","data_loss","execution_eligibility","ownership_evidence","agent_owner_count","lease_evidence"])||p.id!==params.workspace_id||!text(p.record_id)||!integer(p.task_id)||p.task_id<1||p.stage!=="expired_cleanup"||!time(p.expiry_at)||Date.parse(p.expiry_at)>Date.now()||!Array.isArray(p.targets)||p.targets.length<1||p.targets.length>512||JSON.stringify(p.preserved)!==JSON.stringify(["remote_repositories"])||JSON.stringify(p.data_loss)!==JSON.stringify(expiryDataLoss)||p.execution_eligibility!=="unknown_fresh_execution_guards_not_run"||p.ownership_evidence!=="fresh_bounded_registration"||p.agent_owner_count!==0||p.lease_evidence!=="unknown_not_checked")fail();
  const seen=new Set<string>(),ids=new Set<string>(); let lastOrder=-1;
  for(const [i,t] of p.targets.entries()){
    const order:Record<string,number>={tmux:0,sbx:1,git:2,workspace:3};
    if(!object(t,["id","provider","effect","safety_notice_count","automatic_blocker_count"],["branch_effect"])||typeof t.id!=="string"||!/^expiry-target:[a-f0-9]{64}$/.test(t.id)||!Object.hasOwn(order,t.provider)||order[t.provider]<lastOrder||ids.has(t.id)||!integer(t.safety_notice_count,1024)||t.automatic_blocker_count!==0)fail();
    lastOrder=order[t.provider];ids.add(t.id);
    if(t.provider!=="git"&&t.effect!=="forget_missing_worktree_registration"){if(seen.has(t.provider))fail();seen.add(t.provider);}
    if(t.provider==="workspace"&&t.effect==="forget_missing_worktree_registration"){if(t.branch_effect!=="retain_local_branch")fail();continue;}
    const effects:Record<string,string>={tmux:"remove_terminal_session_and_processes",sbx:"remove_sandbox_and_local_state",git:"remove_worktree_and_local_changes",workspace:"remove_workspace_local_files_registration_and_archive_completed_note"};
    if(effects[t.provider]!==t.effect||t.provider==="workspace"&&i!==p.targets.length-1)fail();
    if(t.provider==="git"?!["retain_local_branch","delete_local_branch"].includes(t.branch_effect):t.branch_effect!==undefined)fail();
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
    if (["feedback_workspace_id", "origin_workspace_id", "scope"].some(k => data[k] !== original[k]) || ["plan_id", "expires_at", "revision", "scope_workspace_id", "feedback_workspace_id", "scope"].some(k => v[k] !== original.expiry[k]) || JSON.stringify(p) !== JSON.stringify(original.expiry.proposal)) fail();
  }
}

// Only an immutable UI disclosure binding, not another native plan cache or
// execution path. Any new request/session update consumes the previous binding.
export class ExpirySession {
  private preview: Accepted | undefined;
  private generation = 0;
  clear() { this.preview = undefined; this.generation++; }
  async execute(pi: ExtensionAPI, ctx: ExtensionContext, request: Params, signal: AbortSignal | undefined, available: () => boolean) {
    const accepted = this.preview; this.clear(); const generation = this.generation;
    const params = { ...request }, workspace = resolve(ctx.cwd), scope = params.scope ?? "origin", action = params.action ?? "preview";
    if (Object.keys(params).some(k => !["section", "scope", "workspace_id", "cleanup_mode", "action", "plan_id"].includes(k)) || params.section !== "cleanup_preview" || params.cleanup_mode !== "expired" || !text(params.workspace_id) || !["origin", "feedback", "installation"].includes(scope) || !["preview", "apply"].includes(action) || (action === "preview" ? params.plan_id !== undefined : typeof params.plan_id !== "string" || !/^[a-f0-9]{64}$/.test(params.plan_id))) throw new Error("Expiry requires one exact registered workspace and preview or exact-plan apply; no page/subset selectors");
    signal?.throwIfAborted();
    if (!available() || !ctx.hasUI) throw new Error("Expiry requires a feedback workspace and fresh interactive confirmation");
    const session = ctx.sessionManager.getSessionId();
    if (!text(session)) throw new Error("Expiry requires an identified interactive Pi session");
    if (action === "apply" && (!accepted || accepted.workspace !== workspace || accepted.session !== session || accepted.scope !== scope || accepted.parent !== params.workspace_id || accepted.expires <= Date.now() || JSON.parse(accepted.disclosure).expiry.plan_id !== params.plan_id)) throw new Error("Expiry requires the latest complete unexpired expiry preview in this feedback workspace and Pi session; a plan is not consent");
    const target = `Exact registered workspace ${JSON.stringify(params.workspace_id)}, scope ${JSON.stringify(scope)}, feedback workspace ${JSON.stringify(workspace)}.`;
    const body = action === "apply"
      ? `${target} SEPARATE fresh destructive data-loss approval for this complete immutable ordered target/effect/branch-effect/safety-count disclosure: ${accepted!.disclosure}. Permanently discard workspace-local changes and unknown files, worktrees, sandbox-local state and terminal processes/contents, and eligible local branches INCLUDING UNPUBLISHED COMMITS as proposed. Remove workspace registration; a completed canonical note may be archived. Missing-member cleanup retains its branch; remote repositories and native protected/shared branches remain protected. Private safety messages are withheld; zero automatic blockers and expired data warnings are not full eligibility. Native fresh authored-lifecycle, ownership/activity/alias/lease/provider guards remain authoritative. Original seven-day expiry policy/time/mode are retained, never changed to manual cleanup or an installation sweep. Up to 30 seconds cooperatively; cancellation/disconnect is not forced cancellation or rollback and may leave partial effects. Returned prefix/dispatch evidence is not independent resource verification or safe retry. No automatic retry or replay and no silent replan. Approve only this exact destructive whole plan, not later maintenance.`
      : `${target} Run the shared exact native seven-day expiry planner once, never the installation-wide GC sweep, with a 30 second cooperative budget. Scope bounds this exact registered candidate. The production cached expiry gate must pass. Provider reads inspect the whole owning-task bundle, including other workspaces, and may refresh Git reference metadata; no provider Cleanup or execution admission runs. Registered owner counts include stopped owners, which block issuance; unknown ownership refuses. No held lease, runtime ownership or fresh authored-lifecycle reservation; native final execution guards still apply. Disclose the COMPLETE selected ordered bundle, destructive data loss, branch effects, safety notice counts (private messages withheld), expiry deadline and preserved remote repositories. Local changes, unknown files, sandbox state and eligible local branches including unpublished commits may be permanently discarded by later apply. Complete disclosure must fit 32 KiB or no plan is issued; no page/subset approval. A one-use latest-per-task plan expires after 60 seconds or drift and is freshness, never consent. No ordinary refresh/GC, cleanup, login or explicit runtime start is requested. Apply requires SEPARATE fresh exact-target whole-plan destructive data-loss confirmation. No automatic retry or replay.`;
    const approved = await ctx.ui.confirm(action === "apply" ? "Permanently expire exact workspace data?" : "Preview exact native workspace expiry?", body);
    signal?.throwIfAborted();
    if (approved !== true) return { content: [{ type: "text" as const, text: "Expiry cancelled" }], details: { cancelled: true } };
    const live = () => available() && ctx.hasUI && resolve(ctx.cwd) === workspace && ctx.sessionManager.getSessionId() === session && generation === this.generation;
    if (!live()) throw new Error("Feedback workspace or Pi session changed during confirmation; no expiry requested");
    if (action === "apply" && accepted!.expires <= Date.now()) throw new Error("Expiry preview expired during confirmation; preview and separately confirm again");
    const args = ["feedback-diagnostics", "--workspace", workspace, "--section", "cleanup_preview", "--json", "--scope", scope, "--cleanup-mode", "expired", "--workspace-id", params.workspace_id!, "--action", action];
    if (params.plan_id !== undefined) args.push("--plan-id", params.plan_id);
    let data: any;
    try {
      const result = await pi.exec(process.env.RADAR_BINARY?.trim() || "radar", args, { signal, timeout: 35_000 });
      signal?.throwIfAborted();
      if (!live() || result.code !== 0) throw failure();
      data = parseExpiryResponse(result.stdout);
      validateExpiryPreview(data, params, action === "apply" ? accepted : undefined);
    } catch { throw failure(); }
    if (action === "preview" && data.available === true) this.preview = { workspace, session, scope, parent: params.workspace_id!, disclosure: JSON.stringify(data), expires: Date.parse(data.expiry.expires_at) };
    data.loaded_pi_radar_version = radarPackageVersion;
    return { content: [{ type: "text" as const, text: JSON.stringify(data) }], details: data };
  }
}
