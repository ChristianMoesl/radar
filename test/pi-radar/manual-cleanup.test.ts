import assert from "node:assert/strict";
import {test} from "node:test";
import {registerFeedbackSession} from "../../extensions/pi-radar/feedback.ts";
import {cleanupLimitations} from "../../extensions/pi-radar/manual-cleanup.ts";

function fixture(scope="origin"):any {
 const targets=["a","b"].map((c,i)=>({id:"cleanup-target:"+c.repeat(64),record_id:"record",task_id:7,provider:i===0?"git":"workspace",effect:i===0?"remove_worktree_and_local_changes":"remove_workspace_local_files_registration_and_archive_completed_note",...(i===0?{branch_effect:"delete_local_branch"}:{}),workspace_id:i===0?"origin":"other",cleanup_mode:"manual",preserved:["remote_repositories"],safety:i===0?[{kind:"local_changes",blocks_automatic:true,expires:true,count:1}]:[],safety_notice_count:i===0?1:0,automatic_blocker_count:i===0?1:0,ownership_evidence:"fresh_bounded_registration",agent_owner_count:0,lease_evidence:"unknown_no_lock_file",execution_eligibility:"unknown_fresh_execution_guards_not_run"}));
 return {diagnostics_schema:42,section:"cleanup_preview",scope,available:true,feedback_workspace_id:"feedback",...(scope==="origin"?{origin_workspace_id:"origin"}:{}),evidence:"native_exact_manual_cleanup_plan_not_consent",cleanup:{diagnostics_schema:42,operation:"cleanup",mode:"preview",scope,record_id:"record",task_id:7,feedback_workspace_id:"feedback",...(scope!=="installation"?{scope_workspace_id:scope==="origin"?"origin":"feedback"}:{}),evidence:"native_exact_manual_cleanup_plan_not_consent",status:"preview",problem:"none",plan_id:"a".repeat(64),expires_at:new Date(Date.now()+60_000).toISOString(),revision:1,targets,resource_refresh_pending:false,limitations:[...cleanupLimitations]}};
}
function harness(data=fixture()){
 const tools=new Map<string,any>([["mcp__linear__save_issue",{name:"mcp__linear__save_issue"}]]);let active=["mcp__linear__save_issue"];
 const calls:any[]=[],prompts:string[]=[],approvals:unknown[]=[];let response:any=data;
 const ctx:any={cwd:"/work/feedback",hasUI:true,ui:{setStatus:()=>{},confirm:async(title:string,body:string)=>{prompts.push(title+"\n"+body);return approvals.shift()??false;}}};
 const pi:any={getAllTools:()=>[...tools.values()],getActiveTools:()=>active,setActiveTools:(names:string[])=>{active=names;},registerTool:(tool:any)=>tools.set(tool.name,tool),on:()=>{},exec:async(binary:string,args:string[],options:any)=>{calls.push({binary,args,options});return {code:0,stdout:JSON.stringify(response),stderr:"",killed:false};}};
 const session=registerFeedbackSession(pi);session.update({feedback:true,feedback_origin:"/work/origin"},ctx);
 const run=(params:any={section:"cleanup_preview",parent_id:"record"},signal?:AbortSignal)=>tools.get("radar_diagnostics").execute("call",params,signal,undefined,ctx);
 return {tools,ctx,pi,session,calls,prompts,approvals,run,setResponse:(v:any)=>{response=v;},data};
}
const apply=(scope="origin",parent="record")=>({section:"cleanup_preview",scope,parent_id:parent,action:"apply",plan_id:"a".repeat(64)});
function returned(data:any,completed=2,stage="completed",attempted=2):any{
 const out=structuredClone(data);Object.assign(out.cleanup,{mode:"apply",status:"returned",problem:completed===2?"none":"native_error",operation_id:"operation:1",completed_targets:completed,cleanup_execution:{stage,attempted_targets:attempted},resource_refresh_pending:true});return out;
}
for(const scope of ["origin","feedback","installation"])for(const parent of ["record","7"])test(`manual cleanup requires separate complete destructive consent ${scope}/${parent}`,async()=>{
 const h=harness(fixture(scope));h.approvals.push(true,true);const params={section:"cleanup_preview",scope,parent_id:parent};
 await h.run(params);assert.equal(h.calls.length,1);h.setResponse(returned(h.data));const result=await h.run(apply(scope,parent));
 assert.equal(result.details.cleanup.operation_id,"operation:1");assert.equal(h.calls.length,2);assert.equal(h.prompts.length,2);
 for(const t of h.data.cleanup.targets)assert.ok(h.prompts[1].includes(JSON.stringify(t)),"all ordered target/effect/safety fields must be disclosed");
 for(const text of [parent,scope,"other workspaces","delete worktrees/branches","sandbox/local state","terminal sessions/processes","workspace files/registration","completed note","Stopped registered agents","not full last-moment eligibility","not forced cancellation or rollback","Never replay","not later maintenance"])assert.ok(h.prompts[1].includes(text),text);
 assert.deepEqual(h.calls[1].args,["feedback-diagnostics","--workspace","/work/feedback","--section","cleanup_preview","--json","--scope",scope,"--parent-id",parent,"--action","apply","--plan-id","a".repeat(64)]);
 await assert.rejects(h.run(apply(scope,parent)));assert.equal(h.calls.length,2);
});
for(const kind of ["headless","denied","nonboolean","pre_aborted","approval_aborted","lost_mcp","workspace_changed","origin_changed","ui_lost","expired_during_confirmation"])test(`manual apply cannot inherit preview/check/headless approval: ${kind}`,async()=>{
 const h=harness();h.approvals.push(true);await h.run();const abort=new AbortController();
 if(kind==="headless")h.ctx.hasUI=false;if(kind==="pre_aborted")abort.abort();
 const originalNow=Date.now;
 h.ctx.ui.confirm=async()=>{if(kind==="approval_aborted")abort.abort();if(kind==="lost_mcp")h.tools.delete("mcp__linear__save_issue");if(kind==="ui_lost")h.ctx.hasUI=false;if(kind==="workspace_changed")h.ctx.cwd="/work/other";if(kind==="origin_changed")h.session.update({feedback:true,feedback_origin:"/work/changed"},h.ctx);if(kind==="expired_during_confirmation")Date.now=()=>originalNow()+120_000;return kind==="denied"?false:kind==="nonboolean"?"yes":true;};
 try{if(kind==="denied"||kind==="nonboolean")assert.equal((await h.run(apply(),abort.signal)).details.cancelled,true);else await assert.rejects(h.run(apply(),abort.signal));}finally{Date.now=originalNow;}
 assert.equal(h.calls.length,1);h.ctx.hasUI=true;h.ctx.cwd="/work/feedback";await assert.rejects(h.run(apply()));assert.equal(h.calls.length,1);
});
for(const kind of ["no_preview","wrong_plan","wrong_parent","wrong_scope","newer_preview","returned_object_mutation"])test(`manual apply binds exact latest immutable UI plan: ${kind}`,async()=>{
 const h=harness();if(kind!=="no_preview"){h.approvals.push(true);const out=await h.run();if(kind==="returned_object_mutation")out.details.cleanup.targets[0].effect="private-injected";}
 const params=apply();if(kind==="wrong_plan")params.plan_id="b".repeat(64);if(kind==="wrong_parent")params.parent_id="7";if(kind==="wrong_scope")params.scope="installation";
 if(kind==="newer_preview"){const newer=fixture();newer.cleanup.plan_id="b".repeat(64);h.setResponse(newer);h.approvals.push(true);await h.run();}
 h.setResponse(returned(h.data));h.approvals.push(true);
 if(kind==="returned_object_mutation"){await h.run(params);assert.ok(!h.prompts[1].includes("private-injected"));}else await assert.rejects(h.run(params));
 assert.equal(h.calls.length,kind==="no_preview"?0:kind==="newer_preview"?2:kind==="returned_object_mutation"?2:1);
});
for(const key of ["id","limit","cursor","state","workspace_id","targets","command","workspace","arbitrary"])test(`manual plan rejects subset/unsafe selector ${key} before approval`,async()=>{
 const h=harness();await assert.rejects(h.run({section:"cleanup_preview",parent_id:"record",[key]:key==="limit"?20:"private"}));assert.equal(h.calls.length,0);assert.equal(h.prompts.length,0);
});
const mutations:Record<string,(v:any)=>void>={
 plan:v=>v.cleanup.plan_id="b".repeat(64),revision:v=>v.cleanup.revision++,expiry:v=>v.cleanup.expires_at=new Date(Date.now()+120_000).toISOString(),order:v=>v.cleanup.targets.reverse(),subset:v=>v.cleanup.targets.pop(),effect:v=>v.cleanup.targets[0].effect="remove_sandbox_and_local_state",safety:v=>{v.cleanup.targets[0].safety=[];v.cleanup.targets[0].safety_notice_count=0;v.cleanup.targets[0].automatic_blocker_count=0;},private_native:v=>v.cleanup.raw_target={token:"private"},private_envelope:v=>v.tasks="private",private_target:v=>v.cleanup.targets[0].path="private",private_dispatch:v=>v.cleanup.cleanup_execution.secret="private",unknown_problem:v=>v.cleanup.problem="private",missing_operation:v=>delete v.cleanup.operation_id,pending:v=>v.cleanup.resource_refresh_pending=false,unsafe_count:v=>v.cleanup.completed_targets=3,attempted:v=>v.cleanup.cleanup_execution.attempted_targets=0,wrong_stage:v=>v.cleanup.cleanup_execution.stage="provider_call",wrong_scope:v=>v.cleanup.scope="installation",feedback:v=>v.cleanup.feedback_workspace_id="other",old_schema:v=>v.cleanup.diagnostics_schema=32,private_unavailable:v=>{v.available=false;v.reason="cleanup_outcome_unknown_do_not_replay";v.evidence="unavailable";delete v.cleanup;v.private="private";},
};
for(const [kind,mutate]of Object.entries(mutations))test(`manual apply rejects changed/private/malformed result without replay ${kind}`,async()=>{
 const h=harness();h.approvals.push(true);await h.run();const result=returned(h.data);mutate(result);h.setResponse(result);h.approvals.push(true);await assert.rejects(h.run(apply()),e=>!String(e).includes("private"));assert.equal(h.calls.length,2);await assert.rejects(h.run(apply()));assert.equal(h.calls.length,2);
});
for(const [completed,stage,attempted]of [[0,"admission",0],[0,"ownership_and_leases",0],[0,"bundle_preflight",0],[0,"provider_call",1],[1,"provider_call",2],[1,"target_dispatch",1],[2,"completed",2]] as const)test(`manual cleanup preserves native return boundary ${completed}/${stage}/${attempted}`,async()=>{
 const h=harness();h.approvals.push(true);await h.run();h.setResponse(returned(h.data,completed,stage,attempted));h.approvals.push(true);const out=await h.run(apply());assert.deepEqual(out.details.cleanup.cleanup_execution,{stage,attempted_targets:attempted});assert.equal(out.details.cleanup.completed_targets,completed);
});
for(const kind of ["transport","bad_json","oversized","nonzero","aborted_after_dispatch","mcp_lost_after_dispatch"])test(`manual unknown dispatch cannot replay ${kind}`,async()=>{
 const h=harness();h.approvals.push(true);await h.run();const abort=new AbortController();h.approvals.push(true);
 h.pi.exec=async(...args:any[])=>{h.calls.push(args);if(kind==="transport")throw new Error("private-provider");if(kind==="aborted_after_dispatch")abort.abort();if(kind==="mcp_lost_after_dispatch")h.tools.delete("mcp__linear__save_issue");return {code:kind==="nonzero"?1:0,stdout:kind==="oversized"?"x".repeat(40*1024):kind==="bad_json"?"{":JSON.stringify(returned(h.data)),stderr:"private-provider"};};
 await assert.rejects(h.run(apply(),abort.signal),e=>!String(e).includes("private-provider"));assert.equal(h.calls.length,2);await assert.rejects(h.run(apply()));assert.equal(h.calls.length,2);
});

test("an older in-flight preview cannot replace a newer accepted whole plan",async()=>{
 const h=harness();h.approvals.push(true,true);const next=fixture();next.cleanup.plan_id="b".repeat(64);let release!:(value:any)=>void;let entered!:()=>void;const started=new Promise<void>(r=>{entered=r;});
 h.pi.exec=async(...args:any[])=>{h.calls.push(args);if(h.calls.length===1){entered();return new Promise(r=>{release=r;});}return {code:0,stdout:JSON.stringify(next),stderr:""};};
 const first=h.run();await started;await h.run();release({code:0,stdout:JSON.stringify(fixture()),stderr:""});await assert.rejects(first);
 const original=next;h.pi.exec=async(...args:any[])=>{h.calls.push(args);return {code:0,stdout:JSON.stringify(returned(original)),stderr:""};};h.approvals.push(true);
 await h.run({...apply(),plan_id:"b".repeat(64)});assert.equal(h.calls.length,3);
});
test("a newer preview invalidates an older pending destructive confirmation before dispatch",async()=>{
 const h=harness();h.approvals.push(true);await h.run();let approve!:(value:boolean)=>void;let entered!:()=>void;const started=new Promise<void>(r=>{entered=r;});
 h.ctx.ui.confirm=async(title:string)=>{if(title.startsWith("Apply")){entered();return new Promise<boolean>(r=>{approve=r;});}return true;};
 const first=h.run(apply());await started;const next=fixture();next.cleanup.plan_id="b".repeat(64);h.setResponse(next);await h.run();approve(true);await assert.rejects(first);assert.equal(h.calls.length,2);
});
