import assert from "node:assert/strict";
import { test } from "node:test";
import { registerFeedbackSession } from "../../extensions/pi-radar/feedback.ts";

const limitations = ["installation_wide_all_registered_collection_sources_no_subset","native_collection_reconciliation_may_update_notes_bindings_lifecycle_and_deliver_events","provider_reads_may_use_existing_authentication_and_refresh_git_metadata","no_workspace_gc_cleanup_login_setup_or_runtime_start_requested","latest_preview_only_expires_after_60_seconds_or_revision_drift","plan_identity_is_freshness_not_user_consent","apply_requires_fresh_exact_installation_target_confirmation","consumed_plan_never_reexecutes_even_after_disconnect","30_second_cooperative_native_deadline_not_rollback","client_cancellation_after_dispatch_may_leave_partial_changes","outcomes_are_returned_collection_attempts_not_reconciliation_or_resource_verification","missing_or_cancelled_attempts_unverified_not_success_or_absence","resource_refresh_pending_is_separate_from_remote_provider_success","no_pre_restart_history_or_automatic_retry","raw_errors_paths_notes_and_provider_payloads_withheld"];
const planID = "a".repeat(64), targetID = "integration:"+"b".repeat(64);
function fixture(mode="preview", expires=Date.now()+59_000): any {
  const apply=mode==="apply";
  return {diagnostics_schema:32,section:"refresh",scope:"installation",available:true,feedback_workspace_id:"feedback",evidence:"native_collection_only_refresh",maintenance:{diagnostics_schema:32,operation:"refresh",mode,scope:"installation",evidence:"native_collection_only_refresh",status:apply?"returned":"preview",problem:"none",plan_id:planID,expires_at:new Date(expires).toISOString(),revision:1,...(apply?{operation_id:"native:1"}:{}),targets:[{id:targetID,outcome:apply?"complete":"unknown_not_attempted"}],counts:{complete:apply?1:0,incomplete:0,skipped:0,unverified:apply?0:1},resource_refresh_pending:true,limitations:[...limitations]}};
}
function harness() {
  const tools=new Map<string,any>([["mcp__linear__save_issue",{name:"mcp__linear__save_issue"}]]);
  let active=["read","mcp__linear__save_issue"];
  const calls:any[]=[], confirmations:string[]=[], approvals:boolean[]=[];
  const expires=Date.now()+59_000;
  const h:any={tools,calls,confirmations,approvals,expires,previewMutation:undefined,applyMutation:undefined};
  h.ctx={cwd:"/work/feedback",hasUI:true,ui:{setStatus:()=>{},confirm:async(title:string,body:string)=>{confirmations.push(title+"\n"+body);return approvals.shift()??false;}}};
  h.pi={getAllTools:()=>[...tools.values()],getActiveTools:()=>active,setActiveTools:(v:string[])=>{active=v;},registerTool:(v:any)=>tools.set(v.name,v),on:()=>{},exec:async(binary:string,args:string[],options:any)=>{
    calls.push({binary,args,options});const mode=args[args.indexOf("--action")+1];const data=fixture(mode,h.expires);(mode==="apply"?h.applyMutation:h.previewMutation)?.(data);return {code:0,stdout:JSON.stringify(data),stderr:"",killed:false};
  }};
  h.session=registerFeedbackSession(h.pi);h.session.update({feedback:true},h.ctx);
  h.run=(params:any,signal?:AbortSignal)=>tools.get("radar_diagnostics").execute("call",params,signal,undefined,h.ctx);
  h.preview=()=>h.run({section:"refresh",scope:"installation"});
  h.apply=(signal?:AbortSignal)=>h.run({section:"refresh",scope:"installation",action:"apply",plan_id:planID},signal);
  return h;
}

test("refresh preview is default, headless, observation-only and uses only the dedicated CLI section",async()=>{
  const h=harness();h.ctx.hasUI=false;
  const result=await h.preview();assert.equal(result.details.maintenance.status,"preview");assert.deepEqual(h.confirmations,[]);
  assert.deepEqual(h.calls[0].args,["feedback-diagnostics","--workspace","/work/feedback","--section","refresh","--json","--scope","installation","--action","preview"]);
  assert.equal(h.calls[0].options.timeout,15000);
});

test("refresh apply binds the exact latest plan and installation targets with fresh user consent",async()=>{
  const h=harness();await h.preview();h.approvals.push(true);const result=await h.apply();
  assert.equal(result.details.maintenance.counts.complete,1);assert.equal(h.calls.length,2);assert.equal(h.calls[1].options.timeout,35000);
  assert.deepEqual(h.calls[1].args,["feedback-diagnostics","--workspace","/work/feedback","--section","refresh","--json","--scope","installation","--action","apply","--plan-id",planID]);
  for(const text of ["entire Radar installation",planID,targetID,"not scoped","notes, bindings and task lifecycle","collection events","No workspace GC, cleanup, login, setup or runtime start","not successful reconciliation","Cancellation/disconnect","not rollback","never automatically replay","not user consent"])assert.ok(h.confirmations[0].includes(text),text);
  await assert.rejects(h.apply());assert.equal(h.calls.length,2);assert.equal(h.confirmations.length,1);
});

for(const params of [
  {section:"refresh"},{section:"refresh",scope:"origin"},{section:"refresh",scope:"feedback"},
  {section:"refresh",scope:"installation",action:"cleanup"},{section:"refresh",scope:"installation",plan_id:planID},
  {section:"refresh",scope:"installation",action:"apply"},{section:"refresh",scope:"installation",action:"apply",plan_id:"A".repeat(64)},
  ...["id","parent_id","limit","cursor","state","workspace_id","cleanup_mode","since","operation","arbitrary"].map(key=>({section:"refresh",scope:"installation",[key]:key==="limit"?20:"value"})),
  {section:"tasks",action:"preview"},{section:"logs",plan_id:planID},
])test(`refresh rejects implicit scope and unrelated selectors before any host call: ${JSON.stringify(params)}`,async()=>{
  const h=harness();await assert.rejects(h.run(params));assert.deepEqual(h.calls,[]);assert.deepEqual(h.confirmations,[]);
});

for(const mode of ["no-preview","headless","cancelled","pre-aborted","approval-aborted","lost-MCP","workspace-changed","session-disabled"])test(`refresh apply fails safely before dispatch: ${mode}`,async()=>{
  const h=harness(),abort=new AbortController();if(mode!=="no-preview")await h.preview();const before=h.calls.length;
  if(mode==="headless")h.ctx.hasUI=false;if(mode==="pre-aborted")abort.abort();if(mode==="workspace-changed")h.ctx.cwd="/work/other";if(mode==="session-disabled")h.session.update({feedback:false},h.ctx);
  h.ctx.ui.confirm=async()=>{if(mode==="approval-aborted")abort.abort();if(mode==="lost-MCP")h.tools.delete("mcp__linear__save_issue");return mode!=="cancelled";};
  if(mode==="cancelled")assert.equal((await h.apply()).details.cancelled,true);else await assert.rejects(h.apply(abort.signal));assert.equal(h.calls.length,before);
});

test("refresh approval binds immutable plan, scope and feedback workspace despite mutable caller context",async()=>{
  const h=harness();await h.preview();const params={section:"refresh",scope:"installation",action:"apply",plan_id:planID};
  h.ctx.ui.confirm=async()=>{params.plan_id="c".repeat(64);params.scope="origin";params.action="preview";h.ctx.cwd="/work/other";return true;};
  await h.run(params);assert.ok(h.calls[1].args.includes(planID));assert.ok(!h.calls[1].args.includes("/work/other"));assert.ok(h.calls[1].args.includes("apply"));
});

test("refresh expires during approval and never dispatches late",async()=>{
  const h=harness();h.expires=Date.now()+250;await h.preview();h.ctx.ui.confirm=async()=>{await new Promise(resolve=>setTimeout(resolve,300));return true;};
  await assert.rejects(h.apply(),/expired/);assert.equal(h.calls.length,1);
});

for(const [outcome,problem,status] of [["complete","none","returned"],["incomplete","incomplete_or_unverified","returned"],["skipped_before_collection","none","returned"],["cancelled_or_timed_out","cancelled_or_timed_out","returned"],["unknown_not_attempted","native_error","returned"],["unknown_not_attempted","none","running"]])test(`refresh exposes native ${outcome}/${status} without resource-success claims`,async()=>{
  const h=harness();await h.preview();h.approvals.push(true);h.applyMutation=(d:any)=>{const v=d.maintenance;v.status=status;v.problem=problem;v.targets[0].outcome=outcome;v.counts={complete:outcome==="complete"?1:0,incomplete:outcome==="incomplete"?1:0,skipped:outcome==="skipped_before_collection"?1:0,unverified:["cancelled_or_timed_out","unknown_not_attempted"].includes(outcome)?1:0};};
  assert.equal((await h.apply()).details.maintenance.targets[0].outcome,outcome);
});

const mutations:Record<string,(d:any)=>void>={
  "old-schema":d=>{d.diagnostics_schema=25;},"old-native-schema":d=>{d.maintenance.diagnostics_schema=25;},
  "scope":d=>{d.scope="origin";},"native-scope":d=>{d.maintenance.scope="feedback";},"operation":d=>{d.maintenance.operation="cleanup";},
  "envelope-private":d=>{d.secret="private";},"native-private":d=>{d.maintenance.raw_error="private";},"target-private":d=>{d.maintenance.targets[0].path="private";},
  "token":d=>{d.maintenance.plan_id="private";},"timestamp":d=>{d.maintenance.expires_at="2026-02-30T00:00:00Z";},"revision":d=>{d.maintenance.revision=-1;},
  "target-id":d=>{d.maintenance.targets[0].id="private";},"duplicate":d=>{d.maintenance.targets.push(d.maintenance.targets[0]);},"empty":d=>{d.maintenance.targets=[];},
  "too-many":d=>{d.maintenance.targets=Array.from({length:33},()=>d.maintenance.targets[0]);},"target-outcome":d=>{d.maintenance.targets[0].outcome="healthy";},
  "counts":d=>{d.maintenance.counts.complete=3;},"counts-extra":d=>{d.maintenance.counts.failed=0;},"counts-missing":d=>{delete d.maintenance.counts.skipped;},
  "pending":d=>{delete d.maintenance.resource_refresh_pending;},"limitations":d=>{d.maintenance.limitations[0]="private";},"duplicate-limitations":d=>{d.maintenance.limitations[0]=d.maintenance.limitations[1];},
  "false-unavailable":d=>{d.available=false;},"reason":d=>{d.reason="private";},"mode":d=>{d.maintenance.mode="invalid";},"status":d=>{d.maintenance.status="succeeded";},"problem":d=>{d.maintenance.problem="private";},
};
for(const [kind,mutate] of Object.entries(mutations))test(`refresh rejects malformed/private preview before caching a plan: ${kind}`,async()=>{
  const h=harness();h.previewMutation=mutate;await assert.rejects(h.preview());await assert.rejects(h.apply());assert.equal(h.calls.length,1);assert.deepEqual(h.confirmations,[]);
});
for(const [kind,mutate] of Object.entries({...mutations,"changed-plan":(d:any)=>{d.maintenance.plan_id="c".repeat(64);},"changed-target":(d:any)=>{d.maintenance.targets[0].id="integration:"+"c".repeat(64);},"changed-revision":(d:any)=>{d.maintenance.revision=2;},"changed-expiry":(d:any)=>{d.maintenance.expires_at=new Date(Date.now()+55_000).toISOString();},"changed-feedback":(d:any)=>{d.feedback_workspace_id="other";},"missing-operation":(d:any)=>{delete d.maintenance.operation_id;},"false-success":(d:any)=>{d.maintenance.targets[0].outcome="unknown_not_attempted";d.maintenance.counts={complete:0,incomplete:0,skipped:0,unverified:1};}}))test(`refresh rejects invalid apply evidence without replay: ${kind}`,async()=>{
  const h=harness();await h.preview();h.approvals.push(true);h.applyMutation=mutate;await assert.rejects(h.apply());await assert.rejects(h.apply());assert.equal(h.calls.length,2);assert.equal(h.confirmations.length,1);
});

for(const mode of ["transport","invalid-json","oversized","unknown-response"])test(`refresh ambiguous apply remains unknown and cannot be replayed: ${mode}`,async()=>{
  const h=harness();await h.preview();h.approvals.push(true);h.pi.exec=async(...args:any[])=>{h.calls.push(args);if(mode==="transport")throw new Error("transport lost");if(mode==="invalid-json")return {code:0,stdout:"bad-json"};if(mode==="oversized")return {code:0,stdout:"p".repeat(301*1024)};return {code:0,stdout:JSON.stringify({diagnostics_schema:32,section:"refresh",scope:"installation",available:false,feedback_workspace_id:"feedback",evidence:"unavailable",reason:"request_outcome_unknown_do_not_replay"})};};
  if(mode==="unknown-response")assert.equal((await h.apply()).details.available,false);else await assert.rejects(h.apply());await assert.rejects(h.apply());assert.equal(h.calls.length,2);
});
