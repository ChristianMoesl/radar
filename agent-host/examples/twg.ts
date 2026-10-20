import type { JsonValue, WorkspaceExecution } from "../src/index.ts";

export type TWGConnection = {
  profile: string;
  cloudId: string;
  accountId: string;
  baseUrl: string;
};

// This runs entirely in the selected workspace. TWG owns its storage and refresh;
// a missing sandbox CLI/session fails there without attempting host execution.
const program = String.raw`
const {spawnSync}=require('node:child_process');
const request=JSON.parse(process.argv[1]);
const env={...process.env};
for(const key of Object.keys(env))if(key.startsWith('TWG_')&&!['TWG_CONFIG_DIR','TWG_AUTH_STORAGE_KEY','TWG_SECRET_STORE'].includes(key))delete env[key];
env.TWG_BACKGROUND_UPDATE_CHECK='0';
function run(args){
 const result=spawnSync('twg',args,{env,encoding:'utf8',timeout:15000,maxBuffer:1048576,stdio:['ignore','pipe','ignore']});
 if(result.error||result.status!==0)throw Error('TWG CLI request failed');
 return result.stdout;
}
const prefix=['--profile',request.profile,'--site',request.cloudId,'--output','json','--output-summary','none','--api-version','v2','--timeout-ms','10000'];
function check(){
 const version=run(['--version']).trim(),m=/^(\d+)\.(\d+)\.(\d+)$/.exec(version);
 if(!m||(+m[1]<1)||(+m[1]===1&&(+m[2]<3||(+m[2]===3&&+m[3]<8))))throw Error('TWG 1.3.8+ required');
 const listing=JSON.parse(run([...prefix,'auth','profile','list']));
 const profiles=listing.data?.profiles?.filter(p=>p.name===request.profile);
 if(listing.apiVersion!=='v2'||profiles?.length!==1)throw Error('TWG profile unavailable');
 const profile=profiles[0];
 if(profile.authMethod!=='oauth'||profile.environment!=='prod'||profile.cloudId!==request.cloudId||profile.userId!==request.accountId||profile.domain?.toLowerCase()!==new URL(request.baseUrl).hostname.toLowerCase())throw Error('TWG OAuth profile/site/account mismatch');
 const doctor=JSON.parse(run([...prefix,'doctor'])).data;
 if(doctor?.auth?.resolved?.authMethod!=='oauth'||doctor?.connectivity?.attempted!==true||doctor.connectivity.ok!==true||!doctor.connectivity.userId?.endsWith('/'+request.accountId))throw Error('TWG OAuth identity unavailable');
 // The user accepts changes by another process between this check and read.
}
function read(args,command){
 check();const result=JSON.parse(run([...prefix,...args]));
 const a=result.meta?.acquisition,e=result.meta?.evidence;
 if(result.apiVersion!=='v2'||result.command!==command||result.error||result.warnings?.length||result.meta?.partial||e?.truncation?.truncated||(e?.coverage?.status&&e.coverage.status!=='complete')||(a&&(a.failed||a.omitted||a.truncated||a.requested!==a.succeeded||a.returned!==a.succeeded)))throw Error('Incomplete TWG result');
 return result;
}
try{
 for(const key of ['JIRA_EMAIL','JIRA_API_TOKEN','JIRA_ACCESS_TOKEN'])if(Object.hasOwn(env,key))throw Error('Remove retired Jira credential inputs');
 const items=[],seen=new Set(),cursors=new Set();let after,expectedTotal,bytes=0;
 for(let page=0;page<100;page++){
  let args,command;
  if(request.kind==='issues'){
   command='jira.workitem.query';args=['jira','workitem','query','--jql',request.jql,'--fields','summary,updated,description,status','--first','100'];
  }else if(request.kind==='comments'){
   if(!/^[A-Z][A-Z0-9_]*-[1-9][0-9]*$/.test(request.key))throw Error('Invalid issue key');
   command='jira.workitem.comment.query';args=['jira','workitem','comment','query','--issue-id',request.key,'--first','100','--order-by','created'];
  }else throw Error('Only Jira reads are allowed');
  if(after!==undefined)args.push('--after',after);
  const result=read(args,command),rows=request.kind==='issues'?result.data?.issues:result.data;
  if(!Array.isArray(rows)||rows.length>100)throw Error('Invalid Jira page');
  bytes+=Buffer.byteLength(JSON.stringify(rows));if(bytes>1048576)throw Error('Jira result exceeds workspace output limit');
  for(const row of rows){
   const id=request.kind==='issues'?row.key:row.id;
   if(typeof id!=='string'||!id||seen.has(id)||typeof row.updated!=='string'||!row.updated||(request.kind==='issues'?(typeof row.summary!=='string'||!row.summary||!row.status?.name):row.body===undefined))throw Error('Invalid or duplicate Jira row');
   seen.add(id);items.push(row);
  }
  if(request.kind==='comments'){
   const p=result.meta?.pagination;
   if(!p||!Number.isInteger(p.total)||p.total<0||!Number.isInteger(p.startAt)||p.startAt!==items.length-rows.length||!Number.isInteger(p.maxResults)||p.maxResults<0||p.total<items.length||(expectedTotal!==undefined&&expectedTotal!==p.total))throw Error('Incomplete comment totals');
   expectedTotal=p.total;
  }
  const p=result.pageInfo;
  if(p===undefined||p===null||p.hasNextPage===false){
   if(p?.nextCursor)throw Error('Contradictory Jira pagination');
   if(request.kind==='comments'&&items.length!==expectedTotal)throw Error('Incomplete Jira comments');
   process.stdout.write(JSON.stringify(items));process.exit(0);
  }
  if(p.hasNextPage!==true||!rows.length||typeof p.nextCursor!=='string'||!p.nextCursor||cursors.has(p.nextCursor))throw Error('Jira pagination stalled');
  after=p.nextCursor;cursors.add(after);
 }
 throw Error('Jira page limit exceeded');
}catch{process.stderr.write('TWG OAuth read failed in the selected workspace');process.exitCode=1;}
`;

export async function twgRead(
  execution: WorkspaceExecution,
  connection: TWGConnection,
  request: { kind: "issues"; jql: string } | { kind: "comments"; key: string },
  signal?: AbortSignal,
): Promise<JsonValue[]> {
  if (
    !/^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/.test(connection.profile) ||
    connection.profile.toLowerCase() === "default" ||
    !/^[A-Za-z0-9-]+$/.test(connection.cloudId) ||
    !connection.accountId ||
    new URL(connection.baseUrl).protocol !== "https:"
  )
    throw new Error(
      "Configure the named TWG OAuth profile, Cloud ID, site and account",
    );
  const timeout = AbortSignal.timeout(45000);
  try {
    const text = await execution.exec(
      ["node", "-e", program, JSON.stringify({ ...connection, ...request })],
      execution.binding.path,
      signal ? AbortSignal.any([signal, timeout]) : timeout,
    );
    const result: unknown = JSON.parse(text);
    if (!Array.isArray(result)) throw new Error("Invalid TWG result");
    return result as JsonValue[];
  } catch {
    throw new Error(
      "TWG Jira read failed; verify CLI 1.3.8+, the named OAuth login, site and account inside the selected workspace",
    );
  }
}
