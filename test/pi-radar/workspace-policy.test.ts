import assert from "node:assert/strict";
import { test } from "node:test";
import { registerFeedbackSession } from "../../extensions/pi-radar/feedback.ts";
import { workspacePolicyLimitations } from "../../extensions/pi-radar/workspace-policy.ts";

function fixture(scope="origin", attached=true): any {
 const evidence="fresh_registration_recorded_recipe_not_runtime";
 return {diagnostics_schema:39,section:"workspace_policy",scope,available:true,feedback_workspace_id:scope==="feedback"?"registered":"feedback",...(scope==="origin"?{origin_workspace_id:"registered"}:{}),evidence,inventory:{diagnostics_schema:39,section:"workspace_policy",evidence,observed_at:"2026-11-22T07:20:00.000000001Z",revision:2,snapshot:"a".repeat(64),total:1,pagination_complete:true,resource_refresh_pending:false,limitations:[...workspacePolicyLimitations],items:[{id:"registered",workspace_policy:{id:"registered",origin:"recorded_workspace_registration",sandbox_registered:attached,...(attached?{sandbox:{kit_configured:true,kit_path_configured:false,env_file_configured:true,ready_command_configured:true,managed_mount_count:30,additional_mount_count:20,port_count:18}}:{})}}]}};
}
function harness(data:any){
 const tools=new Map<string,any>(["mcp__linear__get_project","mcp__linear__save_issue"].map(name=>[name,{name,exposure:"direct"}]));
 let active=[...tools.keys()];const calls:any[]=[];let prompts=0;
 const ctx:any={cwd:"/work/feedback",hasUI:false,ui:{setStatus(){},confirm(){prompts++;throw new Error("unexpected approval");}}};
 const pi:any={getAllTools:()=>[...tools.values()],getActiveTools:()=>active,setActiveTools:(names:string[])=>{active=names;},registerTool:(tool:any)=>tools.set(tool.name,tool),on(){},exec:async(binary:string,args:string[],options:any)=>{calls.push({binary,args,options});return{code:0,stdout:JSON.stringify(data),stderr:"",killed:false};}};
 const session=registerFeedbackSession(pi);session.update({feedback:true},ctx);
 return {calls,tools,session,prompts:()=>prompts,read:(params:any={})=>tools.get("radar_diagnostics").execute("call",{section:"workspace_policy",parent_id:"registered",...params},undefined,undefined,ctx)};
}
for(const scope of ["origin","feedback","installation"])for(const attached of [false,true])test(`registered workspace policy is read-only, exact and bounded: ${scope}/${attached}`,async()=>{
 const data=fixture(scope,attached),h=harness(data);const result=await h.read({scope});
 assert.deepEqual(result.details.inventory,data.inventory);assert.equal(h.prompts(),0);assert.equal(h.calls.length,1);
 assert.equal(h.calls[0].args[h.calls[0].args.indexOf("--parent-id")+1],"registered");assert.equal(h.calls[0].args[h.calls[0].args.indexOf("--scope")+1],scope);
 assert.ok(!h.calls[0].args.includes("--action"));assert.match(h.session.instructions()!,/historical setting origins/);
});
for(const params of [{parent_id:undefined},{parent_id:""},{state:"done"},{id:"registered"},{cursor:"x"},{cleanup_mode:"expired"},{workspace_id:"registered"},{action:"apply"},{plan_id:"b".repeat(64)},{limit:51},{raw:"private-ignored-selector"}])test(`registered workspace policy rejects inapplicable selectors before dispatch: ${JSON.stringify(params)}`,async()=>{
 const h=harness(fixture());await assert.rejects(h.read(params));assert.deepEqual(h.calls,[]);assert.equal(h.prompts(),0);
});
const mutations:Record<string,(data:any)=>void>={
 envelope_private:d=>d.raw="synthetic-private-recipe",page_private:d=>d.inventory.raw="synthetic-private-recipe",row_private:d=>d.inventory.items[0].raw="synthetic-private-recipe",view_private:d=>d.inventory.items[0].workspace_policy.command="synthetic-private-recipe",recipe_private:d=>d.inventory.items[0].workspace_policy.sandbox.env_file="synthetic-private-recipe",
 old_schema:d=>d.diagnostics_schema=28,old_page_schema:d=>d.inventory.diagnostics_schema=28,wrong_scope:d=>d.scope="feedback",missing_origin:d=>delete d.origin_workspace_id,wrong_origin:d=>d.origin_workspace_id="other",wrong_parent:d=>d.inventory.items[0].id="other",wrong_view_parent:d=>d.inventory.items[0].workspace_policy.id="other",bad_origin:d=>d.inventory.items[0].workspace_policy.origin="repository_file",effective_claim:d=>d.inventory.items[0].workspace_policy.effective=true,
 missing_recipe:d=>delete d.inventory.items[0].workspace_policy.sandbox,absent_with_recipe:d=>d.inventory.items[0].workspace_policy.sandbox_registered=false,unknown_sandbox:d=>delete d.inventory.items[0].workspace_policy.sandbox_registered,
 negative:d=>d.inventory.items[0].workspace_policy.sandbox.managed_mount_count=-1,fraction:d=>d.inventory.items[0].workspace_policy.sandbox.port_count=0.5,overflow:d=>d.inventory.items[0].workspace_policy.sandbox.port_count=Number.MAX_SAFE_INTEGER+1,null_flag:d=>d.inventory.items[0].workspace_policy.sandbox.env_file_configured=null,missing_flag:d=>delete d.inventory.items[0].workspace_policy.sandbox.kit_configured,
 invalid_time:d=>d.inventory.observed_at="2026-02-30T00:00:00Z",bad_snapshot:d=>d.inventory.snapshot="private",bad_revision:d=>d.inventory.revision=-1,missing_limits:d=>d.inventory.limitations=[],duplicate_limits:d=>d.inventory.limitations[1]=d.inventory.limitations[0],evidence:d=>d.inventory.evidence="effective_policy",more_rows:d=>d.inventory.items.push(d.inventory.items[0]),total:d=>d.inventory.total=0,incomplete:d=>d.inventory.pagination_complete=false,cursor:d=>d.inventory.next_cursor="opaque",null_row:d=>d.inventory.items[0]=null,null_view:d=>d.inventory.items[0].workspace_policy=null,null_recipe:d=>d.inventory.items[0].workspace_policy.sandbox=null,
};
for(const [name,mutate]of Object.entries(mutations))test(`registered workspace policy rejects private or inconsistent evidence: ${name}`,async()=>{
 const data=fixture();mutate(data);const h=harness(data);await assert.rejects(h.read(),error=>error instanceof Error&&!error.message.includes("synthetic-private-recipe"));assert.equal(h.prompts(),0);
});
for(const reason of ["origin_not_registered","daemon_unavailable","snapshot_changed_restart_pagination"])test(`registered workspace policy keeps unavailable evidence unknown: ${reason}`,async()=>{
 const data={diagnostics_schema:39,section:"workspace_policy",scope:"origin",available:false,feedback_workspace_id:"feedback",evidence:"unavailable",reason};
 const h=harness(data);assert.equal((await h.read()).details.available,false);
 for(const [key,value]of [["inventory",fixture().inventory],["raw","synthetic-private-recipe"],["evidence","fresh_registration_recorded_recipe_not_runtime"],["reason","private-error"]]){
  const broken=harness({...data,[key as string]:value});await assert.rejects(broken.read());
 }
});
