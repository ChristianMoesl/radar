import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { mkdtemp, writeFile, readFile, rm } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import test from "node:test";
import { WorkspaceExecution } from "../src/environment.ts";
import { pupRequest } from "../examples/pup.ts";

const execute=promisify(execFile);
for (const mode of ["local","sbx"] as const) test(`Pup telemetry uses selected ${mode} execution and rejects legacy auth`, async()=>{
 const dir=await mkdtemp(join(tmpdir(),"radar-pup-"));
 try {
  const calls=join(dir,"calls.jsonl");
  const tool=join(dir,"pup");
  await writeFile(tool,`#!${process.execPath}
const fs=require('node:fs');
const a=process.argv.slice(2);
if(a[0]==='--version'){console.log('pup 2.0.0');process.exit()}
if(process.env.DD_ACCESS_TOKEN||process.env.PUP_MOCK_SERVER||process.env.DD_API_KEY!=='\\n'||process.env.DD_APP_KEY!=='\\n')process.exit(12);
if(a.includes('status')){console.log(JSON.stringify({authenticated:true,token_type:'Bearer',has_refresh:true,status:'valid',site:'datadoghq.eu',org:'radar',scopes:['logs_read_data']}));process.exit()}
if(!a.includes('--org')||!a.includes('--no-envelope'))process.exit(13);
fs.appendFileSync(${JSON.stringify(calls)},JSON.stringify({args:a,body:fs.readFileSync(0,'utf8')})+'\\n');
console.log(JSON.stringify({data:[{id:'log-1'}]}));
`,{mode:0o700});
  const execution=new WorkspaceExecution({id:"fixture",path:dir,mode,...(mode==="sbx"?{sandbox:"selected-sandbox"}:{})});
  let launched=0;
  execution.exec=async(argv,cwd,signal)=>{
   launched++;assert.equal(cwd,dir);
   const {stdout}=await execute(process.execPath,argv.slice(1) as string[],{cwd:dir,signal,env:{...process.env,PATH:dir+":"+process.env.PATH,DD_ACCESS_TOKEN:"must-not-be-used",PUP_MOCK_SERVER:"must-not-be-used"}});
   return stdout;
  };
  const body=JSON.stringify({filter:{query:"service:fixture",from:"now-5m",to:"now"}});
  assert.deepEqual(await pupRequest(execution,"datadoghq.eu","radar","/api/v2/logs/events/search",{method:"POST",body}),{data:[{id:"log-1"}]});
  assert.equal(launched,1);
  const call=JSON.parse((await readFile(calls,"utf8")).trim());assert.equal(call.body,body);
  assert.ok(call.args.includes("/api/v2/logs/events/search"));
  await assert.rejects(pupRequest(execution,"datadoghq.eu","radar","/api/v1/monitor/1",{method:"DELETE"}),/only configured telemetry reads/);
  assert.equal(launched,1,"a write reached the selected executor");
 } finally {await rm(dir,{recursive:true,force:true})}
});
test("Missing sandbox CLI fails without host fallback",async()=>{
 const execution=new WorkspaceExecution({id:"fixture",path:"/selected",mode:"sbx",sandbox:"selected"});
 let attempts=0;execution.exec=async()=>{attempts++;throw new Error("private diagnostic")};
 await assert.rejects(pupRequest(execution,"datadoghq.eu","radar","/api/v1/query?query=example"),/inside the selected workspace/);
 assert.equal(attempts,1);
});
