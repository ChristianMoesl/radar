import assert from "node:assert/strict";
import { test } from "node:test";
import { registerFeedbackSession } from "../../extensions/pi-radar/feedback.ts";
import { repositoryPolicyLimitations, repositoryPolicyKeys } from "../../extensions/pi-radar/repository-policy.ts";

const id="workspace-resource:"+"a".repeat(64);
function fixture(scope="origin", evidence="fresh_bounded_repository_configuration"): any {
 const label="explicit_current_registered_member_repository_declarations";
 const settings=repositoryPolicyKeys.map(key=>({key,value:["copy_files","setup","sbx.additional_mounts"].includes(key)?"0":key==="sbx.enabled"?"inherit":"false",origin:"repository_default",declaration:"absent"}));
 return {diagnostics_schema:38,section:"repository_policy",scope,available:true,feedback_workspace_id:scope==="feedback"?"registered":"feedback",...(scope==="origin"?{origin_workspace_id:"registered"}:{}),evidence:label,inventory:{diagnostics_schema:38,section:"repository_policy",evidence:label,observed_at:"2026-11-22T11:20:00.000000001Z",revision:2,snapshot:"a".repeat(64),total:1,pagination_complete:true,resource_refresh_pending:false,limitations:[...repositoryPolicyLimitations],items:[{id,repository_policy:{id,workspace_id:"registered",origin:"current_registered_member_repository",available:true,evidence,settings}}]}};
}
function harness(data:any){
 const tools=new Map<string,any>(["mcp__linear__get_project","mcp__linear__save_issue"].map(name=>[name,{name,exposure:"direct"}]));let active=[...tools.keys()];const calls:any[]=[],prompts:any[]=[],approvals:any[]=[true];
 const ctx:any={cwd:"/work/feedback",hasUI:true,ui:{setStatus(){},confirm:async(...args:any[])=>{prompts.push(args);return approvals.shift();}}};
 const pi:any={getAllTools:()=>[...tools.values()],getActiveTools:()=>active,setActiveTools:(names:string[])=>{active=names;},registerTool:(tool:any)=>tools.set(tool.name,tool),on(){},exec:async(binary:string,args:string[],options:any)=>{calls.push({binary,args,options});return{code:0,stdout:JSON.stringify(data),stderr:"",killed:false};}};
 const session=registerFeedbackSession(pi);session.update({feedback:true},ctx);
 return {ctx,calls,prompts,approvals,session,read:(params:any={},signal?:AbortSignal)=>tools.get("radar_diagnostics").execute("call",{section:"repository_policy",parent_id:"registered",id,...params},signal,undefined,ctx)};
}
for(const scope of ["origin","feedback","installation"])for(const evidence of ["fresh_bounded_repository_configuration","repository_configuration_absent"])test(`current repository declarations require exact approved file access: ${scope}/${evidence}`,async()=>{
 const data=fixture(scope,evidence),h=harness(data),result=await h.read({scope});assert.deepEqual(result.details.inventory,data.inventory);assert.equal(h.prompts.length,1);assert.equal(h.calls.length,1);
 const prompt=h.prompts[0].join(" ");for(const text of [id,"registered",scope,"64 KiB","not its member worktree","No referenced","not historical","publication consent"])assert.ok(prompt.includes(text),text);
 const args=h.calls[0].args;assert.equal(args[args.indexOf("--id")+1],id);assert.equal(args[args.indexOf("--parent-id")+1],"registered");assert.equal(args[args.indexOf("--scope")+1],scope);assert.ok(!args.includes("--action"));assert.match(h.session.instructions()!,/repository_policy only for explicitly requested/);
});
for(const mode of ["denied","no_ui","aborted"])test(`repository file access does not dispatch without approval: ${mode}`,async()=>{
 const h=harness(fixture());if(mode==="denied")h.approvals.splice(0,1,false);if(mode==="no_ui")h.ctx.hasUI=false;
 const controller=new AbortController();if(mode==="aborted")controller.abort();
 if(mode==="denied")assert.equal((await h.read()).details.cancelled,true);else await assert.rejects(h.read({},controller.signal));assert.deepEqual(h.calls,[]);
});
for(const params of [{parent_id:undefined},{parent_id:""},{id:undefined},{id:"/arbitrary/path"},{id:"workspace-resource:"+"g".repeat(64)},{id:id+"a"},{state:"done"},{cursor:"opaque"},{cleanup_mode:"expired"},{workspace_id:"registered"},{action:"apply"},{plan_id:"b".repeat(64)},{limit:51},{raw:"private-selector"}])test(`current repository rejects selectors before confirmation: ${JSON.stringify(params)}`,async()=>{
 const h=harness(fixture());await assert.rejects(h.read(params));assert.deepEqual(h.calls,[]);assert.deepEqual(h.prompts,[]);
});
const mutations:Record<string,(data:any)=>void>={
 envelope_private:d=>d.raw="private-secret",page_private:d=>d.inventory.raw="private-secret",row_private:d=>d.inventory.items[0].raw="private-secret",view_private:d=>d.inventory.items[0].repository_policy.command="private-secret",setting_private:d=>d.inventory.items[0].repository_policy.settings[0].path="private-secret",
 old_schema:d=>d.diagnostics_schema=29,old_page:d=>d.inventory.diagnostics_schema=29,wrong_scope:d=>d.scope="feedback",missing_origin:d=>delete d.origin_workspace_id,wrong_origin:d=>d.origin_workspace_id="other",wrong_row:d=>d.inventory.items[0].id="other",wrong_member:d=>d.inventory.items[0].repository_policy.id="other",wrong_parent:d=>d.inventory.items[0].repository_policy.workspace_id="other",wrong_provenance:d=>d.inventory.items[0].repository_policy.origin="historical_recipe",effective:d=>d.inventory.items[0].repository_policy.effective=true,
 null_row:d=>d.inventory.items[0]=null,null_view:d=>d.inventory.items[0].repository_policy=null,null_settings:d=>d.inventory.items[0].repository_policy.settings=null,null_setting:d=>d.inventory.items[0].repository_policy.settings[0]=null,missing_setting:d=>d.inventory.items[0].repository_policy.settings.pop(),duplicate_setting:d=>d.inventory.items[0].repository_policy.settings[1]=d.inventory.items[0].repository_policy.settings[0],unknown_setting:d=>d.inventory.items[0].repository_policy.settings[0].key="secret",private_value:d=>d.inventory.items[0].repository_policy.settings[2].value="private-secret",negative:d=>d.inventory.items[0].repository_policy.settings[0].value="-1",fraction:d=>d.inventory.items[0].repository_policy.settings[0].value="0.5",leading_zero:d=>d.inventory.items[0].repository_policy.settings[0].value="01",overflow:d=>d.inventory.items[0].repository_policy.settings[0].value="9007199254740992",bad_mode:d=>d.inventory.items[0].repository_policy.settings[4].value="automatic",bad_declaration:d=>d.inventory.items[0].repository_policy.settings[0].declaration="inherited",bad_origin:d=>d.inventory.items[0].repository_policy.settings[0].origin="global_default",origin_mismatch:d=>d.inventory.items[0].repository_policy.settings[0].origin="repository_file",undeclared_value:d=>d.inventory.items[0].repository_policy.settings[0].value="1",reason_on_available:d=>d.inventory.items[0].repository_policy.reason="configuration_invalid",unavailable_with_settings:d=>d.inventory.items[0].repository_policy.available=false,
 invalid_time:d=>d.inventory.observed_at="2026-02-30T00:00:00Z",bad_snapshot:d=>d.inventory.snapshot="private-secret",bad_revision:d=>d.inventory.revision=-1,missing_limits:d=>d.inventory.limitations=[],duplicate_limits:d=>d.inventory.limitations[1]=d.inventory.limitations[0],evidence:d=>d.inventory.evidence="effective_policy",more_rows:d=>d.inventory.items.push(d.inventory.items[0]),total:d=>d.inventory.total=0,incomplete:d=>d.inventory.pagination_complete=false,cursor:d=>d.inventory.next_cursor="opaque",
};
for(const [name,mutate]of Object.entries(mutations))test(`current repository rejects private or inconsistent output before delivery: ${name}`,async()=>{
 const data=fixture();mutate(data);const h=harness(data);await assert.rejects(h.read(),error=>error instanceof Error&&!error.message.includes("private-secret"));
});
for(const reason of ["repository_unavailable","configuration_read_unavailable","configuration_not_regular_or_exceeds_bound","configuration_changed_or_read_incomplete","configuration_invalid","registration_changed_or_unavailable","cancelled_or_timed_out"])test(`unavailable repository remains unknown with no defaults: ${reason}`,async()=>{
 const data=fixture(),view=data.inventory.items[0].repository_policy;Object.assign(view,{available:false,evidence:"unavailable",reason,settings:[]});const h=harness(data);assert.equal((await h.read()).details.inventory.items[0].repository_policy.available,false);
 view.settings=fixture().inventory.items[0].repository_policy.settings;await assert.rejects(harness(data).read());
});
for(const reason of ["origin_not_registered","daemon_unavailable","snapshot_changed_restart_pagination"])test(`unavailable repository transport has no fallback: ${reason}`,async()=>{
 const data={diagnostics_schema:38,section:"repository_policy",scope:"origin",available:false,feedback_workspace_id:"feedback",evidence:"unavailable",reason};assert.equal((await harness(data).read()).details.available,false);
 for(const [key,value]of [["inventory",fixture().inventory],["raw","private-secret"],["reason","private-secret"],["evidence","repository_configuration_absent"]])await assert.rejects(harness({...data,[key as string]:value}).read());
});
test("current repository accepts explicit file declarations including nulls and rejects absent-file values",async()=>{
 const data=fixture(),settings=data.inventory.items[0].repository_policy.settings;
 for(const setting of settings){setting.origin="repository_file";setting.declaration="value";setting.value=["copy_files","setup","sbx.additional_mounts"].includes(setting.key)?"23":setting.key==="sbx.enabled"?"required":"true";}
 assert.equal((await harness(data).read()).details.available,true);
 settings[4]={key:"sbx.enabled",value:"inherit",origin:"repository_default",declaration:"null"};assert.equal((await harness(data).read()).details.available,true);
 data.inventory.items[0].repository_policy.evidence="repository_configuration_absent";await assert.rejects(harness(data).read());
});
