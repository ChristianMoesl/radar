import assert from "node:assert/strict";
import { test } from "node:test";
import { registerFeedbackSession } from "../../extensions/pi-radar/feedback.ts";
import { recreationPreviewLimitations } from "../../extensions/pi-radar/recreation-preview.ts";

function fixture(scope="origin", status="recreate", state="running", observed=true): any {
 const evidence="explicit_native_exact_workspace_recreation_proposal";
 return {diagnostics_schema:34,section:"recreation_preview",scope,available:true,feedback_workspace_id:scope==="feedback"?"registered":"feedback",...(scope==="origin"?{origin_workspace_id:"registered"}:{}),evidence,inventory:{diagnostics_schema:34,section:"recreation_preview",evidence,observed_at:"2026-11-23T08:40:00.123456789Z",revision:3,snapshot:"a".repeat(64),total:1,pagination_complete:true,resource_refresh_pending:false,limitations:[...recreationPreviewLimitations],items:[{id:"registered",recreation_preview:{id:"registered",preview_id:"recreation-preview:"+"b".repeat(64),status,previous_state:state,runtime_identity_observed:observed,execution_eligibility:"unknown_fresh_execution_guards_not_run",ownership_evidence:"fresh_bounded_registration",agent_owner_count:0,lease_evidence:"unknown_no_lock_file"}}]}};
}
function harness(data:any=fixture()) {
 const tools=new Map<string,any>(["mcp__linear__get_project","mcp__linear__save_issue"].map(name=>[name,{name,exposure:"direct"}]));let active=[...tools.keys()];
 const calls:any[]=[],prompts:string[]=[];const approvals:boolean[]=[];
 const ctx:any={cwd:"/work/feedback",hasUI:true,ui:{setStatus(){},confirm:async(title:string,text:string)=>{prompts.push(title+" "+text);return approvals.shift()??false;}}};
 const pi:any={getAllTools:()=>[...tools.values()],getActiveTools:()=>active,setActiveTools:(names:string[])=>{active=names;},registerTool:(tool:any)=>tools.set(tool.name,tool),on(){},exec:async(binary:string,args:string[],options:any)=>{calls.push({binary,args,options});return {code:0,stdout:JSON.stringify(data),stderr:"",killed:false};}};
 const session=registerFeedbackSession(pi);session.update({feedback:true},ctx);
 return {ctx,pi,tools,calls,prompts,approvals,session,read:(params:any={},signal?:AbortSignal)=>tools.get("radar_diagnostics").execute("call",{section:"recreation_preview",parent_id:"registered",...params},signal,undefined,ctx)};
}
for(const scope of ["origin","feedback","installation"])for(const [status,state,observed]of [["recreate","running",true],["recreate","stopped",true],["blocked","unknown",true],["blocked","unknown",false],["skipped","unknown",false]] as const)test(`native recreation proposal requires exact fresh approval: ${scope}/${status}/${state}/${observed}`,async()=>{
 const data=fixture(scope,status,state,observed),h=harness(data);h.approvals.push(true);
 const result=await h.read({scope,limit:1});assert.deepEqual(result.details.inventory,data.inventory);assert.equal(h.calls.length,1);assert.equal(h.prompts.length,1);assert.equal(h.calls[0].options.timeout,35000);
 assert.deepEqual(h.calls[0].args,["feedback-diagnostics","--workspace","/work/feedback","--section","recreation_preview","--json","--scope",scope,"--parent-id","registered","--limit","1"]);
 for(const phrase of ["registered",scope,"native recreation planner once","30 seconds","secret/policy metadata","auto-start the SBX daemon","readability only","never read its contents","discards sandbox-local state","Ownership, activity, leases","not full eligibility","not later maintenance","do not automatically retry"])assert.ok(h.prompts[0].includes(phrase),phrase);
 const denied=await h.read({scope});assert.equal(denied.details.cancelled,true);assert.equal(h.calls.length,1);assert.equal(h.prompts.length,2);
});
for(const mode of ["denied","headless","pre_abort","approval_abort","mcp_lost","immutable_request"])test(`recreation proposal approval fails closed or binds immutable request: ${mode}`,async()=>{
 const h=harness();const abort=new AbortController();const params:any={section:"recreation_preview",parent_id:"registered",scope:"origin"};
 if(mode==="headless")h.ctx.hasUI=false;if(mode==="pre_abort")abort.abort();
 h.ctx.ui.confirm=async()=>{if(mode==="approval_abort")abort.abort();if(mode==="mcp_lost")h.tools.clear();if(mode==="immutable_request"){params.parent_id="other";params.scope="installation";}return mode!=="denied";};
 const call=h.tools.get("radar_diagnostics").execute("call",params,abort.signal,undefined,h.ctx);
 if(mode==="denied"){assert.equal((await call).details.cancelled,true);}else if(mode==="immutable_request"){await call;assert.ok(h.calls[0].args.includes("registered"));assert.ok(!h.calls[0].args.includes("other"));return;}else await assert.rejects(call);
 assert.equal(h.calls.length,0);
});
for(const params of [{parent_id:undefined},{parent_id:""},{parent_id:"x".repeat(513)},{scope:"all"},{id:""},{state:""},{cursor:""},{action:"preview"},{action:"apply"},{plan_id:"b".repeat(64)},{cleanup_mode:"manual"},{workspace_id:"registered"},{limit:51},{limit:0},{since:"2026-11-23 00:00:00"},{command:"private"}])test(`recreation proposal rejects selectors before approval: ${JSON.stringify(params)}`,async()=>{
 const h=harness();h.approvals.push(true);await assert.rejects(h.read(params));assert.deepEqual(h.calls,[]);assert.deepEqual(h.prompts,[]);
});
const mutations:Record<string,(d:any)=>void>={
 private_envelope:d=>d.raw="PRIVATE",private_page:d=>d.inventory.raw="PRIVATE",private_row:d=>d.inventory.items[0].raw="PRIVATE",private_view:d=>d.inventory.items[0].recreation_preview.reason="PRIVATE",native_plan:d=>d.inventory.items[0].recreation_preview.plan_id="PRIVATE",recipe:d=>d.inventory.items[0].recreation_preview.recipe={env_file:"PRIVATE"},
 old_schema:d=>d.diagnostics_schema=32,old_page_schema:d=>d.inventory.diagnostics_schema=32,wrong_scope:d=>d.scope="feedback",missing_origin:d=>delete d.origin_workspace_id,wrong_origin:d=>d.origin_workspace_id="other",wrong_parent:d=>d.inventory.items[0].id="other",wrong_view_id:d=>d.inventory.items[0].recreation_preview.id="other",
 bad_preview_id:d=>d.inventory.items[0].recreation_preview.preview_id="b".repeat(64),outcome_claim:d=>d.inventory.items[0].recreation_preview.status="recreated",bad_state:d=>d.inventory.items[0].recreation_preview.previous_state="healthy",absent_identity:d=>d.inventory.items[0].recreation_preview.runtime_identity_observed=false,missing_identity:d=>delete d.inventory.items[0].recreation_preview.runtime_identity_observed,skipped_identity:d=>d.inventory.items[0].recreation_preview.status="skipped",eligibility_claim:d=>d.inventory.items[0].recreation_preview.execution_eligibility="eligible",unknown_recreate:d=>d.inventory.items[0].recreation_preview.previous_state="unknown",
 pending:d=>d.inventory.resource_refresh_pending=true,total:d=>d.inventory.total=2,more_rows:d=>d.inventory.items.push(d.inventory.items[0]),incomplete:d=>d.inventory.pagination_complete=false,cursor:d=>d.inventory.next_cursor="opaque",bad_time:d=>d.inventory.observed_at="2026-02-30T00:00:00Z",bad_revision:d=>d.inventory.revision=-1,bad_snapshot:d=>d.inventory.snapshot="PRIVATE",missing_limits:d=>d.inventory.limitations=[],duplicate_limits:d=>d.inventory.limitations[1]=d.inventory.limitations[0],raw_limits:d=>d.inventory.limitations[0]="PRIVATE",evidence:d=>d.inventory.evidence="verified_outcome",reason:d=>d.reason="PRIVATE",null_page:d=>d.inventory=null,null_row:d=>d.inventory.items[0]=null,null_view:d=>d.inventory.items[0].recreation_preview=null,
};
for(const [name,mutate]of Object.entries(mutations))test(`recreation proposal rejects private or inconsistent evidence: ${name}`,async()=>{
 const data=fixture();mutate(data);const h=harness(data);h.approvals.push(true);await assert.rejects(h.read(),e=>e instanceof Error&&!e.message.includes("PRIVATE"));assert.equal(h.calls.length,1);
});
for(const reason of ["origin_not_registered","daemon_unavailable","snapshot_changed_restart_pagination"])test(`recreation unavailable evidence is unknown without fallback: ${reason}`,async()=>{
 const data={diagnostics_schema:34,section:"recreation_preview",scope:"origin",available:false,feedback_workspace_id:"feedback",evidence:"unavailable",reason};const h=harness(data);h.approvals.push(true);assert.equal((await h.read()).details.available,false);assert.equal(h.calls.length,1);
 for(const extra of [{raw:"PRIVATE"},{inventory:fixture().inventory},{reason:"PRIVATE"},{evidence:"explicit_native_exact_workspace_recreation_proposal"}]){const bad=harness({...data,...extra});bad.approvals.push(true);await assert.rejects(bad.read());assert.equal(bad.calls.length,1);}
});
for(const kind of ["transport","nonzero","json","oversized"])test(`recreation proposal sanitizes failures and never retries: ${kind}`,async()=>{
 const h=harness();h.approvals.push(true);h.pi.exec=async()=>{h.calls.push({});if(kind==="transport")throw new Error("PRIVATE transport");return {code:kind==="nonzero"?1:0,stdout:kind==="oversized"?"PRIVATE".repeat(4000):"PRIVATE invalid json",stderr:"PRIVATE",killed:false};};
 await assert.rejects(h.read(),e=>e instanceof Error&&!e.message.includes("PRIVATE")&&e.message.includes("do not automatically retry"));assert.equal(h.calls.length,1);
});

for(const lease of ["unknown_no_lock_file","exclusive_available_at_probe","contended_at_probe","unavailable"])for(const owners of [undefined,0,1,1024])test(`recreation ownership and lease observations do not grant execution: ${lease}/${owners}`,async()=>{
 const data=fixture(),view=data.inventory.items[0].recreation_preview;
 view.lease_evidence=lease;
 if(owners===undefined){view.ownership_evidence="unavailable";delete view.agent_owner_count;}else view.agent_owner_count=owners;
 const h=harness(data);h.approvals.push(true);const result=await h.read();assert.equal(result.details.inventory.items[0].recreation_preview.execution_eligibility,"unknown_fresh_execution_guards_not_run");
 for(const phrase of ["including stopped registrations","twice briefly probe/release","without creating coordination files","not runtime ownership, holder identity, writer admission","fingerprint drift"])assert.ok(h.prompts[0].includes(phrase),phrase);
 assert.equal(h.calls.length,1);
});
const ownershipMutations:Record<string,(v:any)=>void>={
 missing_ownership:(v:any)=>delete v.ownership_evidence,
 private_ownership:(v:any)=>v.ownership_evidence="PRIVATE",
 missing_count:(v:any)=>delete v.agent_owner_count,
 unknown_with_count:(v:any)=>v.ownership_evidence="unavailable",
 negative_count:(v:any)=>v.agent_owner_count=-1,
 fractional_count:(v:any)=>v.agent_owner_count=0.5,
 oversize_count:(v:any)=>v.agent_owner_count=1025,
 null_count:(v:any)=>v.agent_owner_count=null,
 string_count:(v:any)=>v.agent_owner_count="0",
 missing_lease:(v:any)=>delete v.lease_evidence,
 private_holder:(v:any)=>v.lease_evidence="PRIVATE-holder",
 holder_payload:(v:any)=>v.lease_holder="PRIVATE-holder",
 claimed_owner_block:(v:any)=>v.execution_eligibility="blocked_registered_agent",
 claimed_lease_block:(v:any)=>v.execution_eligibility="blocked_lease_contention",
};
for(const [name,mutate]of Object.entries(ownershipMutations))test(`recreation rejects inconsistent owner or lease evidence: ${name}`,async()=>{
 const data=fixture();mutate(data.inventory.items[0].recreation_preview);const h=harness(data);h.approvals.push(true);
 await assert.rejects(h.read(),e=>e instanceof Error&&!e.message.includes("PRIVATE"));assert.equal(h.calls.length,1);
});
