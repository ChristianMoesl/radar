import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";

const execute = promisify(execFile);
assert.ok(process.argv[2], "provide the packed agent-host tarball");
const dir = await mkdtemp(join(tmpdir(), "radar-packed-agent-"));
try {
  await execute(
    "npm",
    [
      "install",
      "--prefix",
      dir,
      "--ignore-scripts",
      "--no-audit",
      "--no-fund",
      resolve(process.argv[2]),
    ],
    { maxBuffer: 1024 * 1024 },
  );
  const packageDir = join(dir, "node_modules/@christianmoesl/radar-agent-host");
  const metadata = JSON.parse(
    await readFile(join(packageDir, "package.json"), "utf8"),
  );
  assert.equal(metadata.name, "@christianmoesl/radar-agent-host");
  assert.match(
    await readFile(join(packageDir, metadata.bin["radar-agent-host"]), "utf8"),
    /^#!\/usr\/bin\/env node/,
  );
  assert.match(
    await readFile(join(packageDir, "dist/examples/review/SKILL.md"), "utf8"),
    /review/i,
  );
  await writeFile(
    join(dir, "probe.mjs"),
    `
import assert from 'node:assert/strict';
import {PROTOCOL_VERSION,createModels,createRegistry,WorkspaceExecution,defineTask,present,defineAgent} from '@christianmoesl/radar-agent-host';
import {createReadOnlyTools,previewWorkspace} from '@christianmoesl/radar-agent-host/tools';
import {githubSignature,forwardEvent} from '@christianmoesl/radar-agent-host/adapters';
import {McpServices} from '@christianmoesl/radar-agent-host/mcp';
import {createMcpTools,createHostMcpTools,mcpModelToolName} from '@christianmoesl/radar-agent-host/mcp-tools';
assert.equal(PROTOCOL_VERSION,1);
for(const value of [createModels,createRegistry,WorkspaceExecution,defineTask,present,defineAgent,createReadOnlyTools,previewWorkspace,githubSignature,forwardEvent,McpServices,createMcpTools,createHostMcpTools,mcpModelToolName]) assert.equal(typeof value,'function');
for(const name of ['counter','review','jira','datadog','datadog-simple','mcp-check']) {
 const definition=(await import('./node_modules/@christianmoesl/radar-agent-host/dist/examples/'+name+'.js')).default;
 assert.equal(typeof definition.configure,'function');assert.ok(definition.eventTask.definition);
}
`,
  );
  await execute(process.execPath, [join(dir, "probe.mjs")], {
    maxBuffer: 1024 * 1024,
  });
  await writeFile(
    join(dir, "probe.mts"),
    `
import {defineAgent,type SimpleAgentOptions,type AgentLimits,type HostContext,type WorkspaceBinding} from '@christianmoesl/radar-agent-host';
const limits:AgentLimits={maxTurns:3};
const options:SimpleAgentOptions={name:'fixture',instructions:'Review fixture',model:{provider:'openai',modelId:'gpt-4.1-mini'},mcp:{placement:'host',connections:[]},limits};
const definition=defineAgent(options);
import {createMcpTools,createHostMcpTools,type WorkspaceMcpConfig} from '@christianmoesl/radar-agent-host/mcp-tools';
async function inspectTools(host:HostContext,config:WorkspaceMcpConfig){
 const workspace=await createMcpTools(host.execution,config);
 const mode:'local'|'sbx'=workspace.inspection.execution.mode;
 const id:string=workspace.inspection.execution.id;
 const selected=await createHostMcpTools(host.mcp,[]);
 const placement:'host-service'=selected.inspection.execution.mode;
}
const binding:WorkspaceBinding={id:'fixture',path:'/workspace',mode:'sbx',sandbox:'fixture'};
// @ts-expect-error Required binding fields must remain visible to consumers.
const missing:WorkspaceBinding={};
function configure(host:HostContext){const mode:'local'|'sbx'=host.workspace.mode;return host.execution.readOnly(host.workspace.path,'SKILL.md');}
`,
  );
  const repository = fileURLToPath(new URL("../", import.meta.url));
  await execute(
    process.execPath,
    [
      join(repository, "node_modules/typescript/bin/tsc"),
      "--noEmit",
      "--strict",
      "--skipLibCheck",
      "--module",
      "NodeNext",
      "--target",
      "ES2022",
      "--typeRoots",
      join(repository, "node_modules/@types"),
      join(dir, "probe.mts"),
    ],
    { cwd: dir, maxBuffer: 1024 * 1024 },
  );
  process.stdout.write(
    `Packed ${metadata.name}@${metadata.version}: isolated install and all public imports passed\n`,
  );
} finally {
  await rm(dir, { recursive: true, force: true });
}
