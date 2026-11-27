import assert from "node:assert/strict";
import { test } from "node:test";
import { readFile } from "node:fs/promises";
import { registerFeedbackSession } from "../../extensions/pi-radar/feedback.ts";
import { agentStopLimitations } from "../../extensions/pi-radar/agent-stop.ts";
import { agentRemoveLimitations } from "../../extensions/pi-radar/agent-remove.ts";

function fixture(scope = "origin", desired = 3, running = 1): any {
 const target = scope === "feedback" ? "feedback" : "registered";
 return {diagnostics_schema:41,section:"agent_remove",scope,available:true,feedback_workspace_id:"feedback",...(scope==="origin"?{origin_workspace_id:target}:{}),evidence:"native_exact_agent_removal_plan_not_consent",agent_remove:{diagnostics_schema:41,operation:"agent_remove",mode:"preview",scope,record_id:"record",task_id:7,agent_id:"worker",feedback_workspace_id:"feedback",...(scope!=="installation"?{scope_workspace_id:target}:{}),agent_workspace_id:target,evidence:"native_exact_agent_removal_plan_not_consent",plan_id:"c".repeat(64),expires_at:new Date(Date.now()+30_000).toISOString(),revision:3,schedules_before:desired,enabled_schedules_before:running,dispatch_evidence:"not_dispatched",problem:"none",outcome:"not_attempted",resource_refresh_pending:false,limitations:[...agentRemoveLimitations]}};
}
function harness(data: any = fixture()) {
 const tools = new Map<string, any>(["mcp__linear__get_project","mcp__linear__save_issue"].map(name=>[name,{name,exposure:"direct"}]));let active=[...tools.keys()];
 const calls:any[]=[],prompts:string[]=[],approvals:boolean[]=[];
 const ctx:any={cwd:"/work/feedback",session:"session-one",sessionManager:{getSessionId:()=>ctx.session},hasUI:true,ui:{setStatus(){},confirm:async(title:string,text:string)=>{prompts.push(title+" "+text);return approvals.shift()??false;}}};
 const h:any={data,calls,prompts,approvals,ctx,tools,mutation:undefined};
 const pi:any={getAllTools:()=>[...tools.values()],getActiveTools:()=>active,setActiveTools:(names:string[])=>{active=names;},registerTool:(tool:any)=>tools.set(tool.name,tool),on(){},exec:async(binary:string,args:string[],options:any)=>{
  calls.push({binary,args,options});const response=structuredClone(h.data);
  if(args.includes("apply")&&response.available===true)Object.assign(response.agent_remove,{mode:"apply",outcome:"native_handler_returned_unverified",dispatch_evidence:"native_remove_callback_entered_not_resource_outcome",problem:"none",operation_id:"operation",resource_refresh_pending:true});
  h.mutation?.(response,args);return {code:0,stdout:JSON.stringify(response),stderr:"",killed:false};
 }};
 const session=registerFeedbackSession(pi);session.update({feedback:true},ctx);
 Object.assign(h,{pi,session,read:(params:any={},signal?:AbortSignal)=>tools.get("radar_diagnostics").execute("call",{section:"agent_remove",parent_id:"record",agent_id:"worker",...params},signal,undefined,ctx),preview:async(params:any={})=>{approvals.push(true);return h.read(params);},apply:async(params:any={})=>{approvals.push(true);return h.read({action:"apply",plan_id:h.data.agent_remove.plan_id,...params});}});
 return h;
}
for(const scope of ["origin","feedback","installation"])for(const desired of [1,3])for(const running of [0,1])for(const parent of ["record","7"])test(`exact removal preview and separate registration-removal consent ${scope}/${desired}/${running}/${parent}`,async()=>{
 const h=harness(fixture(scope,desired,running));const params={scope,parent_id:parent};const result=await h.preview(params);assert.deepEqual(result.details.agent_remove,h.data.agent_remove);
 assert.equal(h.calls.length,1);assert.equal(h.calls[0].options.timeout,35_000);assert.deepEqual(h.calls[0].args,["feedback-diagnostics","--workspace","/work/feedback","--section","agent_remove","--json","--scope",scope,"--parent-id",parent,"--agent-id","worker","--action","preview"]);
 for(const phrase of [parent,"worker",scope,"native stopped-registration removal plan once","No daemon startup","No automatic retry or replay","30-second expiry","SEPARATE fresh exact-target interactive registration-removal approval","not OS peer/PID-incarnation attestation"])assert.ok(h.prompts[0].includes(phrase),phrase);
 const denied=await h.read({...params,action:"apply",plan_id:h.data.agent_remove.plan_id});assert.equal(denied.details.cancelled,true);assert.equal(h.calls.length,1);await assert.rejects(h.apply(params));assert.equal(h.prompts.length,2);
 await h.preview(params);const applied=await h.apply(params);assert.equal(applied.details.agent_remove.outcome,"native_handler_returned_unverified");assert.equal(h.calls.length,3);
 for(const phrase of [h.data.agent_remove.plan_id,"SEPARATE fresh registration-removal approval","complete immutable disclosure","disable its retained schedules","unlink registration","releasing only this workspace association","Preserve durable data, ownership intent, workspace files, sandbox and canonical note","Other owners, activity, leases","before-execution capture counts","Stop consent is NOT removal consent","does not approve cleanup, sandbox deletion","final task/feedback/scope/revision/publication guards","across BOTH effects","not verified resource absence, cleanup eligibility","no rollback or safe retry","not later maintenance"])assert.ok(h.prompts.at(-1).includes(phrase),phrase);
 await assert.rejects(h.apply(params));assert.equal(h.calls.length,3);
});
for(const mode of ["denied","headless","pre_abort","approval_abort","mcp_lost","workspace_drift","session_drift","session_missing","context_drift","context_away_back","ui_lost","immutable_request"])test(`removal preview confirmation is exact and fail closed ${mode}`,async()=>{
 const h=harness(),abort=new AbortController(),params:any={section:"agent_remove",parent_id:"record",agent_id:"worker",scope:"origin"};
 if(mode==="headless")h.ctx.hasUI=false;if(mode==="pre_abort")abort.abort();if(mode==="session_missing")h.ctx.session="";
 h.ctx.ui.confirm=async()=>{if(mode==="approval_abort")abort.abort();if(mode==="mcp_lost")h.tools.clear();if(mode==="workspace_drift")h.ctx.cwd="/work/other";if(mode==="session_drift")h.ctx.session="other";if(mode==="context_drift")h.session.update({feedback:true,feedback_origin:"changed"},h.ctx);if(mode==="context_away_back"){h.session.update({feedback:false},h.ctx);h.session.update({feedback:true},h.ctx);}if(mode==="ui_lost")h.ctx.hasUI=false;if(mode==="immutable_request"){params.parent_id="other";params.agent_id="other";params.scope="installation";}return mode!=="denied";};
 const call=h.tools.get("radar_diagnostics").execute("call",params,abort.signal,undefined,h.ctx);
 if(mode==="denied")assert.equal((await call).details.cancelled,true);else if(mode==="immutable_request"){await call;assert.ok(h.calls[0].args.includes("record"));assert.ok(h.calls[0].args.includes("worker"));assert.ok(!h.calls[0].args.includes("other"));return;}else await assert.rejects(call);assert.equal(h.calls.length,0);
});
for(const params of [{parent_id:undefined},{parent_id:""},{parent_id:"x".repeat(513)},{parent_id:"a\n"},{agent_id:undefined},{agent_id:""},{agent_id:"Bad/id"},{agent_id:"x".repeat(65)},{scope:"all"},{id:""},{state:""},{cursor:""},{action:"restart"},{action:"apply"},{plan_id:"b".repeat(64)},{cleanup_mode:"manual"},{workspace_id:"registered"},{limit:20},{since:"2026-11-23 00:00:00"},{command:"PRIVATE"}])test(`removal rejects unrelated selectors before approval ${JSON.stringify(params)}`,async()=>{
 const h=harness();h.approvals.push(true);await assert.rejects(h.read(params));assert.deepEqual(h.calls,[]);assert.deepEqual(h.prompts,[]);
});
const mutations:Record<string,(d:any)=>void>={
 private_envelope:d=>d.raw="PRIVATE",private_view:d=>d.agent_remove.raw="PRIVATE",old_schema:d=>d.diagnostics_schema=38,old_view_schema:d=>d.agent_remove.diagnostics_schema=38,wrong_section:d=>d.section="agent_probe",wrong_scope:d=>d.scope="feedback",missing_origin:d=>delete d.origin_workspace_id,wrong_origin:d=>d.origin_workspace_id="other",wrong_parent:d=>{d.agent_remove.record_id="other";d.agent_remove.task_id=8;},wrong_agent:d=>d.agent_remove.agent_id="other",wrong_feedback:d=>d.agent_remove.feedback_workspace_id="other",wrong_anchor:d=>d.agent_remove.scope_workspace_id="other",bad_plan:d=>d.agent_remove.plan_id="PRIVATE",uppercase_plan:d=>d.agent_remove.plan_id="A".repeat(64),schedules:d=>d.agent_remove.schedules_before="false",enabled:d=>d.agent_remove.enabled_schedules_before=false,pending:d=>d.agent_remove.resource_refresh_pending=true,preview_operation:d=>d.agent_remove.operation_id="op",preview_outcome:d=>d.agent_remove.outcome="native_handler_returned_unverified",preview_dispatch:d=>d.agent_remove.dispatch_evidence="native_remove_callback_entered_not_resource_outcome",bad_time:d=>d.agent_remove.expires_at="2026-02-30T00:00:00Z",expired:d=>d.agent_remove.expires_at=new Date(Date.now()-1000).toISOString(),extended:d=>d.agent_remove.expires_at=new Date(Date.now()+60_000).toISOString(),bad_revision:d=>d.agent_remove.revision=-1,unsafe_revision:d=>d.agent_remove.revision=Number.MAX_SAFE_INTEGER+1,missing_limits:d=>d.agent_remove.limitations=[],duplicate_limits:d=>d.agent_remove.limitations[1]=d.agent_remove.limitations[0],private_limits:d=>d.agent_remove.limitations[0]="PRIVATE",evidence:d=>d.agent_remove.evidence="verified_outcome",reason:d=>d.reason="PRIVATE",null_view:d=>d.agent_remove=null,task_zero:d=>d.agent_remove.task_id=0,task_fraction:d=>d.agent_remove.task_id=0.5,private_callback:d=>d.agent_remove.callback="PRIVATE",private_registration:d=>d.agent_remove.registration={token:"PRIVATE"},private_pid:d=>d.agent_remove.pid=123,private_path:d=>d.agent_remove.path="PRIVATE",private_error:d=>d.agent_remove.problem="PRIVATE",wrong_operation:d=>d.agent_remove.operation="refresh",
};
for(const field of Object.keys(fixture().agent_remove))for(const kind of ["missing","null"])mutations[`${kind}_${field}`]=d=>{if(kind==="missing")delete d.agent_remove[field];else d.agent_remove[field]=null;};
for(const [name,mutate]of Object.entries(mutations))test(`removal refuses incomplete or private disclosure ${name}`,async()=>{
 const h=harness();mutate(h.data);h.approvals.push(true);await assert.rejects(h.read(),e=>e instanceof Error&&!e.message.includes("PRIVATE"));assert.equal(h.calls.length,1);await assert.rejects(h.read({action:"apply",plan_id:"c".repeat(64)}));assert.equal(h.calls.length,1);
});
for(const kind of ["transport","nonzero","json","oversized","duplicate","escaped_duplicate","folded","null","deep","trailing"])test(`removal frame failure is sanitized with no replay ${kind}`,async()=>{
 const h=harness();await h.preview();h.pi.exec=async()=>{h.calls.push({});if(kind==="transport")throw new Error("PRIVATE");let raw=JSON.stringify(h.data);if(kind==="json")raw="PRIVATE invalid";if(kind==="oversized")raw+=" ".repeat(36*1024);if(kind==="duplicate")raw=raw.replace('"scope":"origin"','"scope":"feedback","scope":"origin"');if(kind==="escaped_duplicate")raw=raw.replace('"scope":"origin"','"sc\\u006fpe":"origin","scope":"origin"');if(kind==="folded")raw=raw.replace('"scope":"origin"','"Scope":"origin"');if(kind==="null")raw="null";if(kind==="deep")raw="[".repeat(18)+raw+"]".repeat(18);if(kind==="trailing")raw+=' {"private":"PRIVATE"}';return {code:kind==="nonzero"?1:0,stdout:raw,stderr:"PRIVATE",killed:false};};
 await assert.rejects(h.apply(),e=>e instanceof Error&&!e.message.includes("PRIVATE")&&e.message.includes("automatically retry"));assert.equal(h.calls.length,2);await assert.rejects(h.apply());assert.equal(h.calls.length,2);
});
for(const mode of ["headless","abort","workspace","session","scope","target","agent","identity","expired","expire_during_approval","superseded","context","concurrent","availability","ui_lost","another_section"])test(`removal apply consumes latest session binding before dispatch ${mode}`,async()=>{
 const h=harness(),abort=new AbortController();if(mode==="expired")h.data.agent_remove.expires_at=new Date(Date.now()+1000).toISOString();await h.preview();let params:any={};
 if(mode==="headless")h.ctx.hasUI=false;if(mode==="abort")abort.abort();if(mode==="workspace")h.ctx.cwd="/work/other";if(mode==="session")h.ctx.session="other";if(mode==="scope")params.scope="installation";if(mode==="target")params.parent_id="other";if(mode==="agent")params.agent_id="other";if(mode==="identity")params.plan_id="d".repeat(64);if(mode==="superseded"){h.data.agent_remove.plan_id="d".repeat(64);await h.preview();params.plan_id="c".repeat(64);}if(mode==="context")h.session.update({feedback:true},h.ctx);if(mode==="another_section")await assert.rejects(h.read({section:"agents",agent_id:"worker"}));
 const before=h.calls.length,oldNow=Date.now;
 try{if(mode==="expired")Date.now=()=>oldNow()+2000;h.ctx.ui.confirm=async()=>{if(mode==="expire_during_approval")Date.now=()=>oldNow()+31_000;if(mode==="availability")h.tools.clear();if(mode==="ui_lost")h.ctx.hasUI=false;if(mode==="concurrent"){h.ctx.hasUI=false;await assert.rejects(h.read());h.ctx.hasUI=true;}return true;};await assert.rejects(h.read({action:"apply",plan_id:h.data.agent_remove.plan_id,...params},abort.signal));assert.equal(h.calls.length,before);}finally{Date.now=oldNow;}
 h.ctx.hasUI=true;h.ctx.cwd="/work/feedback";h.ctx.session="session-one";if(h.tools.has("radar_diagnostics"))await assert.rejects(h.apply());assert.equal(h.calls.length,before);
});
for(const problem of ["native_error","cancelled_or_timed_out"])test(`removal partial native evidence does not establish recovery or replay ${problem}`,async()=>{
 const h=harness();await h.preview();h.mutation=(d:any)=>Object.assign(d.agent_remove,{outcome:"unverified_may_be_partial",problem});const result=await h.apply();assert.equal(result.details.agent_remove.outcome,"unverified_may_be_partial");assert.equal(result.details.agent_remove.resource_refresh_pending,true);await assert.rejects(h.apply());assert.equal(h.calls.length,2);
});
for(const name of ["feedback","origin","scope","record","task","agent","workspace","schedules","enabled","plan","expiry","revision","pending","operation","outcome","problem","dispatch","private"])test(`removal apply response matches immutable accepted disclosure ${name}`,async()=>{
 const h=harness();const result=await h.preview();result.details.agent_remove.plan_id="e".repeat(64);result.details.agent_remove.schedules_before=2;
 h.mutation=(d:any)=>{const v=d.agent_remove;switch(name){case "feedback":d.feedback_workspace_id=v.feedback_workspace_id="other";break;case "origin":d.origin_workspace_id=v.scope_workspace_id=v.agent_workspace_id="other";break;case "scope":d.scope=v.scope="installation";delete d.origin_workspace_id;delete v.scope_workspace_id;break;case "record":v.record_id="other";break;case "task":v.task_id++;break;case "agent":v.agent_id="other";break;case "workspace":v.agent_workspace_id="other";break;case "schedules":v.schedules_before=2;break;case "enabled":v.enabled_schedules_before=0;break;case "plan":v.plan_id="e".repeat(64);break;case "expiry":v.expires_at=new Date(Date.now()+20_000).toISOString();break;case "revision":v.revision++;break;case "pending":v.resource_refresh_pending=false;break;case "operation":delete v.operation_id;break;case "outcome":v.outcome="process_exited";break;case "problem":v.problem="native_error";break;case "dispatch":v.dispatch_evidence="process_stopped";break;case "private":v.private="PRIVATE";}}
 await assert.rejects(h.apply(),e=>e instanceof Error&&!e.message.includes("PRIVATE"));assert.equal(h.calls.length,2);await assert.rejects(h.apply());assert.equal(h.calls.length,2);
});
test("returned model details cannot rewrite retained registration-removal consent",async()=>{
 const h=harness(),result=await h.preview();result.details.feedback_workspace_id="other";result.details.agent_remove.schedules_before=2;result.details.agent_remove.plan_id="e".repeat(64);await h.apply();assert.ok(!h.prompts.at(-1).includes('"schedules_before":2'));assert.ok(h.prompts.at(-1).includes('"schedules_before":3'));assert.equal(h.calls.length,2);
});
for(const stage of ["preview","apply"])for(const drift of ["workspace","session","context","abort","ui","mcp"])test(`removal post-dispatch ${stage} ${drift} withholds stale evidence`,async()=>{
 const h=harness(),abort=new AbortController();if(stage==="apply")await h.preview();const original=h.pi.exec;h.pi.exec=async(...args:any[])=>{const result=await original(...args);if(drift==="workspace")h.ctx.cwd="/work/other";if(drift==="session")h.ctx.session="other";if(drift==="context")h.session.update({feedback:true},h.ctx);if(drift==="abort")abort.abort();if(drift==="ui")h.ctx.hasUI=false;if(drift==="mcp")h.tools.clear();return result;};h.approvals.push(true);await assert.rejects(h.read(stage==="apply"?{action:"apply",plan_id:h.data.agent_remove.plan_id}:{},abort.signal));assert.equal(h.calls.length,stage==="apply"?2:1);
});
for(const scope of ["origin","feedback","installation"])for(const reason of ["origin_not_registered","daemon_unavailable","agent_remove_outcome_unknown_do_not_replay"])test(`removal unavailable evidence does not fall back ${scope}/${reason}`,async()=>{
 const data={diagnostics_schema:41,section:"agent_remove",scope,available:false,feedback_workspace_id:"feedback",...(scope==="origin"&&reason!=="origin_not_registered"?{origin_workspace_id:"registered"}:{}),evidence:"unavailable",reason};const h=harness(data);
 if(reason==="origin_not_registered"&&scope!=="origin"){await assert.rejects(h.preview({scope}));return;}
 assert.equal((await h.preview({scope})).details.available,false);await assert.rejects(h.read({scope,action:"apply",plan_id:"c".repeat(64)}));assert.equal(h.calls.length,1);
 for(const extra of [{raw:"PRIVATE"},{agent_remove:fixture(scope).agent_remove},{reason:"PRIVATE"},{evidence:"native_exact_agent_removal_plan_not_consent"},{feedback_workspace_id:null}]){const bad=harness({...data,...extra});await assert.rejects(bad.preview({scope}));assert.equal(bad.calls.length,1);}
});
for(const changed of [false,true])test(`unknown removal apply still binds registered feedback caller ${changed}`,async()=>{
 const h=harness();await h.preview();h.mutation=(d:any)=>{delete d.agent_remove;d.available=false;d.evidence="unavailable";d.reason="agent_remove_outcome_unknown_do_not_replay";if(changed)d.feedback_workspace_id="other";};if(changed)await assert.rejects(h.apply());else assert.equal((await h.apply()).details.available,false);await assert.rejects(h.apply());assert.equal(h.calls.length,2);
});
for(const section of ["overview","agents","agent_probe","refresh","cleanup_check","cleanup_preview","recreation_preview","retirement_preview","operations"])for(const agent of ["worker",""])test(`other sections reject removal agent selector ${section}/${agent}`,async()=>{
 const h=harness();await assert.rejects(h.read({section,agent_id:agent}));assert.equal(h.calls.length,0);assert.equal(h.prompts.length,0);
});


test("removal Pi limitations and capability scope agree with the native protocol", async () => {
 const native = await readFile(new URL("../../internal/protocol/diagnostic_agent_remove.go", import.meta.url), "utf8");
 const block = native.split("func DiagnosticAgentRemoveLimitations() []string {")[1].split("\n}")[0];
 const labels = [...block.matchAll(/"([a-z0-9_]+)"/g)].map(match => match[1]);
 assert.deepEqual(agentRemoveLimitations, labels);
 const cli = await readFile(new URL("../../cmd/radar/feedback.go", import.meta.url), "utf8");
 for (const contract of ['"agent_remove_previews": true', '"agent_remove_apply": true', '"runtime_controls": false']) assert.ok(cli.includes(contract));
});

for (const [total, enabled] of [[0,0],[1,1],[1048576,1048576]]) test(`removal accepts bounded capture counts ${total}/${enabled}`, async () => { await harness(fixture("origin",total,enabled)).preview(); });
for (const [total, enabled] of [[-1,0],[1,-1],[1,2],[1048577,0],[1.5,1],[1,0.5],[true,0],[1,NaN]]) test(`removal rejects inconsistent capture counts ${total}/${enabled}`, async () => { await assert.rejects(harness(fixture("origin",total as any,enabled as any)).preview()); });

for (const direction of ["stop-to-remove", "remove-to-stop"]) test(`separate consent cannot cross operation ${direction}`, async () => {
 const removal = fixture(), stop = structuredClone(removal);
 const v = stop.agent_remove; delete stop.agent_remove;
 stop.section = v.operation = "agent_stop"; stop.evidence = v.evidence = "native_exact_agent_stop_plan_not_consent";
 delete v.schedules_before; delete v.enabled_schedules_before;
 Object.assign(v, { desired_before: false, running_before: false, limitations: [...agentStopLimitations] }); stop.agent_stop = v;
 const first = direction === "stop-to-remove" ? "agent_stop" : "agent_remove", second = direction === "stop-to-remove" ? "agent_remove" : "agent_stop";
 const h = harness(first === "agent_stop" ? stop : removal);
 h.approvals.push(true); await h.read({ section: first });
 const prompts = h.prompts.length;
 h.data = second === "agent_stop" ? stop : removal;
 await assert.rejects(h.read({ section: second, action: "apply", plan_id: "c".repeat(64) }));
 assert.equal(h.calls.length, 1); assert.equal(h.prompts.length, prompts);
 // Even returning to the original operation cannot revive the old preview.
 await assert.rejects(h.read({ section: first, action: "apply", plan_id: "c".repeat(64) }));
 assert.equal(h.calls.length, 1);
});
