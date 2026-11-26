import { resolve } from "node:path";
import type { ExtensionAPI, ExtensionContext } from "@earendil-works/pi-coding-agent";
import { radarPackageVersion } from "./version.ts";

export const recreationLimitations = [
  "exact_registered_workspace_native_recreation_no_subset",
  "preview_native_preflight_runtime_metadata_recipe_accessibility_and_existing_lease_probes",
  "sbx_reads_may_auto_start_its_daemon_not_a_sandbox",
  "apply_discards_sandbox_local_state_and_interrupts_commands_and_services",
  "native_workspace_files_registration_recorded_mounts_ports_and_prior_state_intended_preserved",
  "registered_owner_and_deletion_lease_observations_not_runtime_ownership_or_native_writer_admission",
  "latest_per_workspace_60_second_one_use_plan_is_freshness_not_consent",
  "apply_requires_separate_fresh_interactive_exact_target_destructive_confirmation",
  "native_full_plan_registration_recipe_runtime_deletion_writer_lifetime_and_restoration_guards_remain",
  "30_second_cooperative_deadline_disconnect_not_forced_cancellation_or_rollback",
  "returned_native_restoration_checks_not_independent_live_resource_verification",
  "partial_cancelled_failed_or_unknown_outcomes_never_authorize_replay_retry_or_silent_replan",
  "current_daemon_workspace_operation_and_resource_invalidation_not_complete_history",
  "private_native_plan_paths_runtime_names_recipes_reasons_and_errors_withheld",
];
type Params = { section?: string; scope?: string; parent_id?: string; action?: string; plan_id?: string };
type Accepted = { workspace: string; session: string; scope: string; parent: string; disclosure: string; expires: number };
const failure = () => new Error("Invalid or unavailable native recreation response; the outcome may be unknown. Do not replay, automatically retry or silently replan.");
const object = (v: any, required: string[], optional: string[] = []): boolean => !!v && typeof v === "object" && !Array.isArray(v) && required.every(k => Object.hasOwn(v, k)) && Object.keys(v).every(k => required.includes(k) || optional.includes(k));
const text = (v: any, n = 512): boolean => typeof v === "string" && v.length > 0 && Buffer.byteLength(v, "utf8") <= n;
const integer = (v: any, max = Number.MAX_SAFE_INTEGER): boolean => Number.isSafeInteger(v) && v >= 0 && v <= max;
const time = (v: any): boolean => text(v, 35) && /^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d{1,9})?Z$/.test(v) && !v.startsWith("0000-") && Number.isFinite(Date.parse(v)) && new Date(v).toISOString().slice(0, 19) === v.slice(0, 19);

// JSON.parse alone loses duplicate/escaped/folded keys. Check the entire bounded
// frame before parsing or projecting anything, including unavailable replies.
export function parseRecreationResponse(raw: string): any {
  if (Buffer.byteLength(raw, "utf8") > 36 * 1024) throw failure();
  let i = 0;
  const whitespace = () => { while (/[\x20\t\r\n]/.test(raw[i] ?? "x")) i++; };
  const string = (): string => {
    const start = i++;
    for (; i < raw.length; i++) {
      if (raw[i] === "\\") { i++; continue; }
      if (raw[i] === '"') return JSON.parse(raw.slice(start, ++i));
    }
    throw failure();
  };
  const value = (depth: number): void => {
    if (depth > 16) throw failure();
    whitespace();
    if (raw[i] === "{") {
      i++; whitespace(); const seen = new Set<string>();
      if (raw[i] === "}") { i++; return; }
      while (i < raw.length) {
        if (raw[i] !== '"') throw failure();
        const key = string();
        if (key !== key.toLowerCase() || seen.has(key)) throw failure();
        seen.add(key); whitespace(); if (raw[i++] !== ":") throw failure(); value(depth + 1); whitespace();
        const end = raw[i++]; if (end === "}") return; if (end !== ",") throw failure(); whitespace();
      }
    } else if (raw[i] === "[") {
      i++; whitespace(); if (raw[i] === "]") { i++; return; }
      while (i < raw.length) { value(depth + 1); whitespace(); const end = raw[i++]; if (end === "]") return; if (end !== ",") throw failure(); }
    } else if (raw[i] === '"') { string(); return; }
    else {
      const token = /^(?:true|false|null|-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?)/.exec(raw.slice(i));
      if (token) { i += token[0].length; return; }
    }
    throw failure();
  };
  try { value(0); whitespace(); if (i !== raw.length) throw failure(); return JSON.parse(raw); } catch { throw failure(); }
}

export function validateRecreationPreview(data: any, params: Params, accepted?: Accepted): void {
  const fail = (): never => { throw failure(); }, scope = params.scope ?? "origin", mode = params.action ?? "preview";
  if (!["origin", "feedback", "installation"].includes(scope) || !text(params.parent_id) || !object(data, ["diagnostics_schema", "section", "scope", "available", "feedback_workspace_id", "evidence"], ["origin_workspace_id", "reason", "recreation"]) || data.diagnostics_schema !== 36 || data.section !== "recreation_preview" || data.scope !== scope || !text(data.feedback_workspace_id) || (data.origin_workspace_id !== undefined && (scope !== "origin" || !text(data.origin_workspace_id)))) fail();
  if (data.available === false) {
    if (data.evidence !== "unavailable" || data.recreation !== undefined || !["origin_not_registered", "daemon_unavailable", "recreation_outcome_unknown_do_not_replay"].includes(data.reason) || (data.reason === "origin_not_registered" ? scope !== "origin" || data.origin_workspace_id !== undefined : scope === "origin" && !text(data.origin_workspace_id))) fail();
    if (mode === "apply") {
      if (!accepted) fail();
      const original = JSON.parse(accepted!.disclosure);
      if (data.feedback_workspace_id !== original.feedback_workspace_id || (data.reason !== "origin_not_registered" && data.origin_workspace_id !== original.origin_workspace_id)) fail();
    }
    return;
  }
  const v = data.recreation;
  if (data.available !== true || data.reason !== undefined || data.evidence !== "native_exact_workspace_recreation_plan_not_consent" || !object(v, ["diagnostics_schema", "operation", "mode", "scope", "feedback_workspace_id", "evidence", "plan_id", "expires_at", "revision", "proposal", "problem", "outcome", "resource_refresh_pending", "limitations"], ["scope_workspace_id", "operation_id"]) || v.diagnostics_schema !== 36 || v.operation !== "recreate" || v.mode !== mode || v.scope !== scope || v.feedback_workspace_id !== data.feedback_workspace_id || v.evidence !== data.evidence || !text(v.plan_id, 64) || !/^[a-f0-9]{64}$/.test(v.plan_id) || !time(v.expires_at) || !integer(v.revision) || JSON.stringify(v.limitations) !== JSON.stringify(recreationLimitations) || Buffer.byteLength(JSON.stringify(v), "utf8") > 16 * 1024) fail();
  if (scope === "installation" ? v.scope_workspace_id !== undefined : v.scope_workspace_id !== params.parent_id) fail();
  if (scope === "feedback" && data.feedback_workspace_id !== params.parent_id) fail();
  if (scope === "origin" ? data.origin_workspace_id !== v.scope_workspace_id : data.origin_workspace_id !== undefined) fail();
  const p = v.proposal;
  if (!object(p, ["id", "preview_id", "status", "previous_state", "runtime_identity_observed", "execution_eligibility", "ownership_evidence", "lease_evidence"], ["agent_owner_count"]) || p.id !== params.parent_id || typeof p.preview_id !== "string" || !/^recreation-preview:[a-f0-9]{64}$/.test(p.preview_id) || !["recreate", "blocked", "skipped"].includes(p.status) || !["running", "stopped", "unknown"].includes(p.previous_state) || typeof p.runtime_identity_observed !== "boolean" || p.execution_eligibility !== "unknown_fresh_execution_guards_not_run" || (p.ownership_evidence === "fresh_bounded_registration" ? !integer(p.agent_owner_count, 1024) : p.ownership_evidence !== "unavailable" || p.agent_owner_count !== undefined) || !["unknown_no_lock_file", "exclusive_available_at_probe", "contended_at_probe", "unavailable"].includes(p.lease_evidence) || (!p.runtime_identity_observed && p.previous_state !== "unknown") || (p.status === "recreate" && (!p.runtime_identity_observed || p.previous_state === "unknown")) || (p.status === "skipped" && (p.runtime_identity_observed || p.previous_state !== "unknown"))) fail();
  if (mode === "preview") {
    if (params.plan_id !== undefined || v.outcome !== "not_attempted" || v.problem !== "none" || v.operation_id !== undefined || v.resource_refresh_pending !== false || Date.parse(v.expires_at) <= Date.now() || Date.parse(v.expires_at) > Date.now() + 65_000) fail();
  } else {
    if (mode !== "apply" || !accepted || v.plan_id !== params.plan_id || p.status !== "recreate" || !text(v.operation_id) || v.resource_refresh_pending !== true) fail();
    if (v.outcome === "native_restoration_checks_returned" ? v.problem !== "none" : v.outcome !== "unverified_may_be_partial" || !["native_error", "invalid_or_incomplete_native_result", "cancelled_or_timed_out"].includes(v.problem)) fail();
    const original = JSON.parse(accepted!.disclosure);
    if (["feedback_workspace_id", "origin_workspace_id", "scope"].some(k => data[k] !== original[k]) || ["plan_id", "expires_at", "revision", "scope_workspace_id", "feedback_workspace_id", "scope"].some(k => v[k] !== original.recreation[k]) || JSON.stringify(p) !== JSON.stringify(original.recreation.proposal)) fail();
  }
}

// Only an immutable UI disclosure binding, not another native plan cache or
// execution path. Any new request/session update consumes the previous binding.
export class RecreationSession {
  private preview: Accepted | undefined;
  private generation = 0;
  clear() { this.preview = undefined; this.generation++; }
  async execute(pi: ExtensionAPI, ctx: ExtensionContext, request: Params, signal: AbortSignal | undefined, available: () => boolean) {
    const accepted = this.preview; this.clear(); const generation = this.generation;
    const params = { ...request }, workspace = resolve(ctx.cwd), scope = params.scope ?? "origin", action = params.action ?? "preview";
    if (Object.keys(params).some(k => !["section", "scope", "parent_id", "action", "plan_id"].includes(k)) || params.section !== "recreation_preview" || !text(params.parent_id) || !["origin", "feedback", "installation"].includes(scope) || !["preview", "apply"].includes(action) || (action === "preview" ? params.plan_id !== undefined : typeof params.plan_id !== "string" || !/^[a-f0-9]{64}$/.test(params.plan_id))) throw new Error("Recreation requires one exact registered workspace and preview or exact-plan apply; no page/subset selectors");
    signal?.throwIfAborted();
    if (!available() || !ctx.hasUI) throw new Error("Recreation requires a feedback workspace and fresh interactive confirmation");
    const session = ctx.sessionManager.getSessionId();
    if (!text(session)) throw new Error("Recreation requires an identified interactive Pi session");
    if (action === "apply" && (!accepted || accepted.workspace !== workspace || accepted.session !== session || accepted.scope !== scope || accepted.parent !== params.parent_id || accepted.expires <= Date.now() || JSON.parse(accepted.disclosure).recreation.plan_id !== params.plan_id || JSON.parse(accepted.disclosure).recreation.proposal.status !== "recreate")) throw new Error("Recreation requires the latest complete unexpired recreate preview in this feedback workspace and Pi session; a plan is not consent");
    const target = `Exact registered workspace ${JSON.stringify(params.parent_id)}, scope ${JSON.stringify(scope)}, feedback workspace ${JSON.stringify(workspace)}.`;
    const body = action === "apply"
      ? `${target} SEPARATE fresh destructive approval for this complete immutable disclosure: ${accepted!.disclosure}. Applying discards sandbox-local state and interrupts commands/services. Native recreation intends to preserve workspace files/registration and restore recorded mounts/ports and the prior running/stopped state. The private native full-plan comparison, registration/recipe/runtime checks, deletion/exclusive writer admission, pinned lifetime and restoration guards remain authoritative. Registered owner counts (including stopped registrations) and deletion-lease observations are not runtime ownership, holder identity, writer admission or removal policy for recreation. A preview or plan ID is freshness, never consent or full eligibility. Up to 30 seconds cooperatively; cancellation/disconnect is not forced cancellation or rollback, may leave partial effects, and returned native restoration checks are not independent live-resource verification. No automatic retry or replay, and no silent replan. Approve only this exact destructive plan, not later maintenance.`
      : `${target} Run the existing native recreation planner once with a cooperative native budget of 30 seconds. Preview uses native preflight, SBX runtime/port/secret/policy metadata and recorded recipe accessibility checks; env-file readability only, never read its contents. Read commands may auto-start the SBX daemon, never a sandbox. Bounded agent-registration scans count owners including stopped registrations and twice briefly probe/release the existing deletion-lease inode without creating coordination files/directories. Ownership/lease fingerprint drift invalidates the preview. Observations are not runtime ownership, holder identity, writer admission or full eligibility; fresh native guards remain. Proposed later recreation discards sandbox-local state and interrupts commands/services while intending to preserve workspace files/registration and restore recorded mounts/ports and prior state. No recreation, cleanup, ordinary refresh/GC, login or explicit sandbox start is requested by preview. An opaque one-use plan expires after 60 seconds or drift, never supplies consent. Apply requires SEPARATE fresh exact-target destructive confirmation; this approval is not later maintenance. No automatic retry or replay.`;
    const approved = await ctx.ui.confirm(action === "apply" ? "Recreate exact sandbox and discard its local state?" : "Preview exact native sandbox recreation?", body);
    signal?.throwIfAborted();
    if (approved !== true) return { content: [{ type: "text" as const, text: "Recreation cancelled" }], details: { cancelled: true } };
    const live = () => available() && ctx.hasUI && resolve(ctx.cwd) === workspace && ctx.sessionManager.getSessionId() === session && generation === this.generation;
    if (!live()) throw new Error("Feedback workspace or Pi session changed during confirmation; no recreation requested");
    if (action === "apply" && accepted!.expires <= Date.now()) throw new Error("Recreation preview expired during confirmation; preview and separately confirm again");
    const args = ["feedback-diagnostics", "--workspace", workspace, "--section", "recreation_preview", "--json", "--scope", scope, "--parent-id", params.parent_id!, "--action", action];
    if (params.plan_id !== undefined) args.push("--plan-id", params.plan_id);
    let data: any;
    try {
      const result = await pi.exec(process.env.RADAR_BINARY?.trim() || "radar", args, { signal, timeout: 35_000 });
      signal?.throwIfAborted();
      if (!live() || result.code !== 0) throw failure();
      data = parseRecreationResponse(result.stdout);
      validateRecreationPreview(data, params, action === "apply" ? accepted : undefined);
    } catch { throw failure(); }
    if (action === "preview" && data.available === true) this.preview = { workspace, session, scope, parent: params.parent_id!, disclosure: JSON.stringify(data), expires: Date.parse(data.recreation.expires_at) };
    data.loaded_pi_radar_version = radarPackageVersion;
    return { content: [{ type: "text" as const, text: JSON.stringify(data) }], details: data };
  }
}
