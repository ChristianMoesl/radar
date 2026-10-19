import type { JsonValue, WorkspaceExecution } from "../src/index.ts";

// Executed inside the selected workspace, including SBX. The CLI and its OAuth
// storage must be provisioned there; no host credentials or host fallback.
const program = String.raw`
const {spawnSync}=require('node:child_process');
const request=JSON.parse(process.argv[1]);
const env={...process.env};
for(const key of ['DD_ACCESS_TOKEN','DD_API_KEY','DD_APP_KEY','DD_SITE','DD_ORG','DD_OUTPUT','PUP_OUTPUT','PUP_FILTER','PUP_MOCK_SERVER'])delete env[key];
env.DD_SITE=request.site;env.DD_ORG=request.org;env.DD_API_KEY='\n';env.DD_APP_KEY='\n';
function run(args,input){
 const r=spawnSync('pup',args,{env,input,encoding:'utf8',timeout:15000,maxBuffer:524288,stdio:['pipe','pipe','ignore']});
 if(r.status!==0||r.error)throw Error('Pup request failed; verify OAuth login and CLI access inside this workspace');
 return r.stdout;
}
try{
 const version=run(['--version']);const m=/(?:^|\s)v?(\d+)\.(\d+)\.(\d+)(?:\s|$)/.exec(version);
 if(!m||Number(m[1])<2)throw Error('Pup 2.0.0+ is required inside this workspace');
 const prefix=['--org',request.org,'--no-envelope','--output','json'];
 const auth=JSON.parse(run([...prefix,'auth','status']));
 if(!auth.authenticated||auth.auth_method||String(auth.token_type).toLowerCase()!=='bearer'||!auth.has_refresh||auth.status!=='valid'||auth.site!==request.site||auth.org!==request.org||!Array.isArray(auth.scopes)||!auth.scopes.includes(request.scope))throw Error('Pup OAuth session or required read scope is unavailable inside this workspace');
 const args=[...prefix,'api',request.path,'-X',request.method];
 if(request.body!==undefined)args.push('--input','-');
 const result=JSON.parse(run(args,request.body));
 process.stdout.write(JSON.stringify(result));
}catch{process.stderr.write('Pup OAuth telemetry read failed inside the selected workspace');process.exitCode=1;}
`;

export async function pupRequest(
  execution: WorkspaceExecution,
  site: string,
  org: string,
  path: string,
  options: RequestInit = {},
): Promise<JsonValue> {
  const method = options.method ?? "GET";
  const route = path.split("?")[0];
  const scope = method === "GET" && route === "/api/v1/query"
    ? "timeseries_query"
    : method === "POST" && route === "/api/v2/logs/events/search"
      ? "logs_read_data"
      : method === "POST" && route === "/api/v2/spans/events/search"
        ? "apm_read"
        : undefined;
  if (!scope) throw new Error("Datadog investigator permits only configured telemetry reads");
  const request = { site, org, path, method, scope, body: options.body };
  const timeout = AbortSignal.timeout(45000);
  try {
    const text = await execution.exec(
      ["node", "-e", program, JSON.stringify(request)],
      execution.binding.path,
      options.signal ? AbortSignal.any([options.signal, timeout]) : timeout,
    );
    return JSON.parse(text) as JsonValue;
  } catch {
    throw new Error("Pup telemetry read failed; verify CLI installation, the named OAuth session and read scopes inside the selected workspace");
  }
}

export function pupSearchSpans(
  execution: WorkspaceExecution,
  site: string,
  org: string,
  query: string,
  from: string,
  to: string,
  signal?: AbortSignal,
): Promise<JsonValue> {
  return pupRequest(execution, site, org, "/api/v2/spans/events/search", {
    method: "POST",
    signal,
    body: JSON.stringify({
      data: {
        type: "search_request",
        attributes: { filter: { query, from, to }, page: { limit: 100 } },
      },
    }),
  });
}
