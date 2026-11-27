import { resolve } from "node:path";
import type { ExtensionAPI, ExtensionContext } from "@earendil-works/pi-coding-agent";
import { radarPackageVersion } from "./version.ts";

export const cleanupLimitations = [
 "exact_current_production_task_confirmed_manual_whole_ordered_bundle_no_subset",
 "scope_selects_task_including_related_resources_in_other_workspaces",
 "provider_reads_git_fetch_prune_and_transient_lease_probes_may_have_metadata_effects",
 "proposed_local_changes_branches_sandbox_state_terminal_processes_workspace_files_registration_and_completed_note_may_be_removed",
 "preview_ownership_and_lease_observations_are_not_full_execution_eligibility_or_reservations",
 "latest_60_second_one_use_native_plan_is_freshness_not_consent",
 "apply_requires_fresh_interactive_exact_plan_whole_bundle_destructive_confirmation",
 "native_last_moment_ownership_activity_alias_lease_and_provider_guards_still_apply",
 "30_second_cooperative_deadline_disconnect_is_not_forced_cancellation_or_rollback",
 "returned_prefix_and_dispatch_boundaries_are_not_verified_resource_outcomes",
 "partial_cancelled_or_unknown_outcomes_never_authorize_replay_retry_or_silent_replan",
 "current_daemon_operation_evidence_only_no_pre_restart_history",
 "private_paths_commands_messages_owners_and_native_operations_withheld",
];
type Params = {section?:string;scope?:string;parent_id?:string;cleanup_mode?:string;action?:string;plan_id?:string};
type Accepted = {workspace:string;scope:string;parent:string;view:string;expires:number};
const failure = () => new Error("Invalid or unavailable manual cleanup response; after dispatch the outcome may be unknown. Do not replay, automatically retry or silently replan.");
const object = (v:any,keys:string[]):v is Record<string,any> => !!v && typeof v==="object" && !Array.isArray(v) && Object.keys(v).every(k=>keys.includes(k));
const text = (v:any,n=512) => typeof v==="string" && v.length>0 && Buffer.byteLength(v,"utf8")<=n;
const integer = (v:any,min=0,max=Number.MAX_SAFE_INTEGER) => Number.isSafeInteger(v)&&v>=min&&v<=max;
const time = (v:any) => text(v,35)&&/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d{1,9})?Z$/.test(v)&&Number.isFinite(Date.parse(v))&&new Date(v).toISOString().slice(0,19)===v.slice(0,19);

export function validateManualCleanup(data:any,params:Params,accepted?:Accepted):void {
 const fail=():never=>{throw failure();},scope=params.scope??"origin",mode=params.action??"preview";
 if(!object(data,["diagnostics_schema","section","scope","available","feedback_workspace_id","origin_workspace_id","evidence","reason","cleanup"])||data.diagnostics_schema!==39||data.section!=="cleanup_preview"||data.scope!==scope||!text(data.feedback_workspace_id)||(data.origin_workspace_id!==undefined&&!text(data.origin_workspace_id)))fail();
 if(scope!=="origin"&&data.origin_workspace_id!==undefined)fail();
 if(data.available===false){if(data.evidence!=="unavailable"||data.cleanup!==undefined||!["origin_not_registered","daemon_unavailable","cleanup_outcome_unknown_do_not_replay"].includes(data.reason)||(data.reason==="origin_not_registered"?(scope!=="origin"||data.origin_workspace_id!==undefined):(scope==="origin"&&!text(data.origin_workspace_id))))fail();return;}
 const v=data.cleanup;
 if(data.available!==true||data.reason!==undefined||data.evidence!=="native_exact_manual_cleanup_plan_not_consent"||!object(v,["diagnostics_schema","operation","mode","scope","record_id","task_id","feedback_workspace_id","scope_workspace_id","evidence","status","problem","plan_id","expires_at","revision","targets","operation_id","completed_targets","cleanup_execution","resource_refresh_pending","limitations"])||v.diagnostics_schema!==39||v.operation!=="cleanup"||v.mode!==mode||v.scope!==scope||!text(v.record_id)||!integer(v.task_id,1)||(params.parent_id!==v.record_id&&params.parent_id!==String(v.task_id))||v.feedback_workspace_id!==data.feedback_workspace_id||v.evidence!==data.evidence||!text(v.plan_id,64)||!/^[a-f0-9]{64}$/.test(v.plan_id)||!time(v.expires_at)||!integer(v.revision)||!Array.isArray(v.targets)||v.targets.length<1||v.targets.length>512||JSON.stringify(v.limitations)!==JSON.stringify(cleanupLimitations)||Buffer.byteLength(JSON.stringify(v),"utf8")>32*1024)fail();
 if(scope==="installation"?v.scope_workspace_id!==undefined:!text(v.scope_workspace_id))fail();
 if(scope==="feedback"&&v.scope_workspace_id!==data.feedback_workspace_id)fail();
 if(scope==="origin"?data.origin_workspace_id!==v.scope_workspace_id:data.origin_workspace_id!==undefined)fail();
 const seen=new Set<string>();
 for(const t of v.targets){
  if(!object(t,["lease_evidence","cleanup_mode","workspace_id","ownership_evidence","agent_owner_count","id","record_id","task_id","provider","effect","branch_effect","preserved","safety","safety_notice_count","automatic_blocker_count","execution_eligibility"])||!text(t.id,79)||!/^cleanup-target:[a-f0-9]{64}$/.test(t.id)||seen.has(t.id)||t.record_id!==v.record_id||t.task_id!==v.task_id||t.cleanup_mode!=="manual"||(t.workspace_id!==undefined&&!text(t.workspace_id))||t.execution_eligibility!=="unknown_fresh_execution_guards_not_run"||JSON.stringify(t.preserved)!==JSON.stringify(["remote_repositories"])||!Array.isArray(t.safety)||t.safety.length>28||!integer(t.safety_notice_count,0,1024)||!integer(t.automatic_blocker_count,0,t.safety_notice_count))fail();
  seen.add(t.id);
  const effects:Record<string,string[]>={git:["remove_worktree_and_local_changes"],sbx:["remove_sandbox_and_local_state"],tmux:["remove_terminal_session_and_processes"],workspace:["remove_workspace_local_files_registration_and_archive_completed_note","forget_missing_worktree_registration"]};
  if(!effects[t.provider]?.includes(t.effect))fail();
  if(t.provider==="git"?!["retain_local_branch","delete_local_branch"].includes(t.branch_effect):t.effect==="forget_missing_worktree_registration"?t.branch_effect!=="retain_local_branch":t.branch_effect!==undefined)fail();
  if(t.ownership_evidence==="fresh_bounded_registration"){
   if(t.agent_owner_count!==0||!["exclusive_available_at_probe","unknown_no_lock_file","unavailable"].includes(t.lease_evidence))fail();
  }else if(t.ownership_evidence!=="not_checked_no_target_path"||t.agent_owner_count!==undefined||t.lease_evidence!=="not_checked_no_target_path")fail();
  let total=0,blocking=0;const notices=new Set<string>();
  for(const n of t.safety){
   if(!object(n,["kind","blocks_automatic","expires","count"])||!["local_changes","unpublished_data","safety_check_unavailable","deletes_local_data","unknown_entries","disposable_data","other_notice"].includes(n.kind)||typeof n.blocks_automatic!=="boolean"||typeof n.expires!=="boolean"||!integer(n.count,1,1024)||(n.expires&&!["local_changes","unpublished_data","safety_check_unavailable","unknown_entries"].includes(n.kind)))fail();
   const key=JSON.stringify([n.kind,n.blocks_automatic,n.expires]);if(notices.has(key))fail();notices.add(key);total+=n.count;if(n.blocks_automatic)blocking+=n.count;
  }
  if(total!==t.safety_notice_count||blocking!==t.automatic_blocker_count)fail();
 }
 if(mode==="preview"){
  if(v.status!=="preview"||v.problem!=="none"||v.operation_id!==undefined||v.completed_targets!==undefined||v.cleanup_execution!==undefined||v.resource_refresh_pending!==false||Date.parse(v.expires_at)<=Date.now()||Date.parse(v.expires_at)>Date.now()+65_000)fail();
 }else{
  if(!accepted)throw failure();const original=JSON.parse(accepted.view);
  if(v.status!=="returned"||!text(v.operation_id)||!integer(v.completed_targets,0,v.targets.length)||v.resource_refresh_pending!==true||!["none","native_error","cancelled_or_timed_out"].includes(v.problem)||(v.problem==="none"&&v.completed_targets!==v.targets.length)||(v.problem==="native_error"&&v.completed_targets===v.targets.length)||v.plan_id!==params.plan_id||["plan_id","expires_at","revision","scope","record_id","task_id","feedback_workspace_id","scope_workspace_id"].some(k=>v[k]!==original[k])||JSON.stringify(v.targets)!==JSON.stringify(original.targets))fail();
  if(v.cleanup_execution!==undefined){
   const e=v.cleanup_execution,c=v.completed_targets,total=v.targets.length;
   if(!object(e,["stage","attempted_targets"])||!integer(e.attempted_targets,c,total))fail();
   const valid=e.stage==="completed"?c===total&&e.attempted_targets===total:e.stage==="provider_call"?c<total&&e.attempted_targets===c+1:e.stage==="target_dispatch"?c<total&&e.attempted_targets===c:["admission","ownership_and_leases","bundle_preflight"].includes(e.stage)&&c===0&&e.attempted_targets===0;
   if(!valid)fail();
  }
 }
}

// This is a UI freshness binding, never an executable plan store or consent.
// Execution always uses the existing daemon-owned native one-use plan.
export class ManualCleanupSession {
 private preview:Accepted|undefined;
 private generation=0;
 clear(){this.preview=undefined;this.generation++;}
 async execute(pi:ExtensionAPI,ctx:ExtensionContext,request:Params,signal:AbortSignal|undefined,available:()=>boolean){
  const params={...request},workspace=resolve(ctx.cwd),scope=params.scope??"origin",action=params.action??"preview";
  if(Object.keys(params).some(k=>!["section","scope","parent_id","cleanup_mode","action","plan_id"].includes(k))||params.section!=="cleanup_preview"||!text(params.parent_id)||!["origin","feedback","installation"].includes(scope)||(params.cleanup_mode!==undefined&&params.cleanup_mode!=="manual")||!["preview","apply"].includes(action)||(action==="preview"?params.plan_id!==undefined:typeof params.plan_id!=="string"||!/^[a-f0-9]{64}$/.test(params.plan_id)))throw new Error("Manual cleanup requires one exact retained task, complete plan and no subset/page selectors");
  const accepted=this.preview;this.preview=undefined;const generation=++this.generation;
  signal?.throwIfAborted();if(!available()||!ctx.hasUI)throw new Error("Manual cleanup requires a feedback workspace and fresh interactive confirmation");
  if(action==="apply"&&(!accepted||accepted.workspace!==workspace||accepted.scope!==scope||accepted.parent!==params.parent_id||JSON.parse(accepted.view).plan_id!==params.plan_id||accepted.expires<=Date.now()))throw new Error("Manual cleanup requires the latest complete unexpired preview in this workspace; a plan is not consent");
  const body=action==="apply"?`Exact task ${JSON.stringify(params.parent_id)}, scope ${JSON.stringify(scope)}, feedback workspace ${JSON.stringify(workspace)}, one-use plan ${JSON.stringify(params.plan_id)}. Approve this COMPLETE ordered target/effect/safety bundle, including other workspaces, never a subset: ${JSON.stringify(JSON.parse(accepted!.view).targets)}. Targets without workspace_id have unknown workspace association, not absence. Cleanup may discard local changes, delete worktrees/branches, sandbox/local state, terminal sessions/processes, workspace files/registration and archive a completed note where proposed. Native coordination files/directories, runtime/protected-tree checks and admission may run. Stopped registered agents still block removal; preview/check success is not full last-moment eligibility, a reservation or deletion consent. Native guards remain. Up to 30 seconds cooperatively; cancellation/disconnect after dispatch is not forced cancellation or rollback and may leave partial effects. Returned prefix/dispatch evidence is not independent resource verification or safe retry. Never replay, automatically retry or silently replan an unknown/partial result. Approve only this exact destructive plan, not later maintenance.`:`Run the native confirmed-manual planner for exact retained task ${JSON.stringify(params.parent_id)}, scope ${JSON.stringify(scope)}, feedback workspace ${JSON.stringify(workspace)}. Up to 30 seconds cooperatively. Scope selects the task; disclose its entire ordered resource bundle including other workspaces, at most 256 active references, 512 targets and 32 KiB, or refuse the plan rather than truncate/page. Existing authenticated provider reads and Git fetch --prune may refresh metadata. Brief nonblocking exclusive locks on existing lease files may transiently contend. No lease file/directory is created; no contents are read and no lock remains reserved. Lease contention blocks the whole native bundle; missing/unavailable lease evidence is unknown, not no active work. Registered-agent owner counts include stopped owners, which block the whole bundle; unknown ownership is not zero. Runtime ownership/activity and fresh execution guards remain unknown. No provider Cleanup, collection, login, setup or runtime startup is requested by this preview. No execution permission or consent to apply; apply needs a separate fresh complete-plan destructive confirmation. Do not automatically retry ambiguous preview reads.`;
  const approved=await ctx.ui.confirm(action==="apply"?"Apply exact native manual cleanup?":"Preview complete native manual cleanup plan?",body);
  signal?.throwIfAborted();if(approved!==true)return {content:[{type:"text" as const,text:"Manual cleanup cancelled"}],details:{cancelled:true}};
  if(!available()||!ctx.hasUI||resolve(ctx.cwd)!==workspace||generation!==this.generation)throw new Error("Feedback availability or workspace changed during confirmation; no cleanup requested");
  if(action==="apply"&&accepted!.expires<=Date.now())throw new Error("Cleanup preview expired during confirmation; preview and confirm again");
  const args=["feedback-diagnostics","--workspace",workspace,"--section","cleanup_preview","--json","--scope",scope,"--parent-id",params.parent_id!,"--action",action];
  if(params.plan_id!==undefined)args.push("--plan-id",params.plan_id);
  let data:any;
  try{
   const result=await pi.exec(process.env.RADAR_BINARY?.trim()||"radar",args,{signal,timeout:35_000});
   signal?.throwIfAborted();if(!available()||resolve(ctx.cwd)!==workspace||generation!==this.generation)throw failure();if(result.code!==0||Buffer.byteLength(result.stdout,"utf8")>36*1024)throw failure();
   data=JSON.parse(result.stdout);validateManualCleanup(data,params,action==="apply"?accepted:undefined);
  }catch{throw failure();}
  if(action==="preview"&&data.available===true)this.preview={workspace,scope,parent:params.parent_id!,view:JSON.stringify(data.cleanup),expires:Date.parse(data.cleanup.expires_at)};
  data.loaded_pi_radar_version=radarPackageVersion;
  return {content:[{type:"text" as const,text:JSON.stringify(data)}],details:data};
 }
}
