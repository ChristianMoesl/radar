import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { mkdtemp, writeFile, readFile, rm } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import test from "node:test";
import { WorkspaceExecution } from "../src/environment.ts";
import { twgRead } from "../examples/twg.ts";

const execute = promisify(execFile);
const connection = {
  profile: "radar",
  cloudId: "cloud-fixture",
  accountId: "fixture-account",
  baseUrl: "https://jira.example.test",
};

for (const mode of ["local", "sbx"] as const) {
  test(`TWG native reads stay in selected ${mode} execution, sanitize credentials and consume all pages`, async () => {
    const dir = await mkdtemp(join(tmpdir(), "radar-twg-"));
    try {
      const calls = join(dir, "calls.jsonl");
      await writeFile(
        join(dir, "twg"),
        `#!${process.execPath}
const fs=require('node:fs'),a=process.argv.slice(2);
if(process.env.TWG_TOKEN||process.env.TWG_OUTBOUND_AUTH||process.env.TWG_AUTH_DISABLED||process.env.TWG_PROFILE)process.exit(12);
if(a[0]==='--version'){console.log('1.3.8');process.exit()}
if(a[0]!=='--profile'||a[1]!=='radar'||a[2]!=='--site'||a[3]!=='cloud-fixture')process.exit(13);
const text=a.join(' ');let result;
if(text.includes('auth profile list'))result={apiVersion:'v2',data:{profiles:[{name:'radar',authMethod:process.env.RADAR_TWG_SCENARIO==='pat'?'pat':'oauth',environment:'prod',cloudId:'cloud-fixture',userId:'fixture-account',domain:'jira.example.test'}]}};
else if(text.includes('doctor'))result={data:{auth:{resolved:{authMethod:'oauth'}},connectivity:{attempted:true,ok:true,userId:'ari:cloud:identity::user/fixture-account'}}};
else {
fs.appendFileSync(${JSON.stringify(calls)},JSON.stringify(a)+'\\n');
const next=a.includes('--after'),comments=a.includes('comment');
result={apiVersion:'v2',command:comments?'jira.workitem.comment.query':'jira.workitem.query',data:comments?[{id:next?'c2':'c1',updated:'revision',body:'discussion'}]:{issues:[{key:next?'ABC-2':'ABC-1',summary:'Work',updated:'revision',status:{name:'Open'},description:null}]},pageInfo:{hasNextPage:!next,nextCursor:next?null:'1'},meta:comments?{pagination:{startAt:next?1:0,maxResults:1,total:2}}:{}};
if(process.env.RADAR_TWG_SCENARIO==='partial')result.meta.partial=true;
if(process.env.RADAR_TWG_SCENARIO==='missing-total'&&comments)delete result.meta.pagination;
}
console.log(JSON.stringify(result));
`,
        { mode: 0o700 },
      );
      const execution = new WorkspaceExecution({
        id: "fixture",
        path: dir,
        mode,
        ...(mode === "sbx" ? { sandbox: "selected" } : {}),
      });
      let attempts = 0;
      let scenario = "normal";
      execution.exec = async (argv, cwd, signal) => {
        attempts++;
        assert.equal(cwd, dir);
        const { stdout } = await execute(
          process.execPath,
          argv.slice(1) as string[],
          {
            cwd: dir,
            signal,
            env: {
              ...process.env,
              PATH: dir + ":" + process.env.PATH,
              TWG_TOKEN: "injected-static-fixture",
              TWG_OUTBOUND_AUTH: "proxy-fixture",
              TWG_AUTH_DISABLED: "1",
              TWG_PROFILE: "wrong-profile",
              RADAR_TWG_SCENARIO: scenario,
            },
          },
        );
        return stdout;
      };
      assert.equal(
        (
          await twgRead(execution, connection, {
            kind: "issues",
            jql: "project = ABC",
          })
        ).length,
        2,
      );
      assert.equal(
        (
          await twgRead(execution, connection, {
            kind: "comments",
            key: "ABC-1",
          })
        ).length,
        2,
      );
      assert.equal(attempts, 2);
      const reads = (await readFile(calls, "utf8"))
        .trim()
        .split("\n")
        .map((line) => JSON.parse(line) as string[]);
      assert.equal(reads.length, 4);
      assert.ok(
        reads.every(
          (args) =>
            args.includes("query") &&
            !args.includes("create") &&
            !args.includes("login"),
        ),
      );
      assert.ok(reads[1]!.includes("--after"));
      scenario = "pat";
      await assert.rejects(
        twgRead(execution, connection, {
          kind: "issues",
          jql: "project = ABC",
        }),
        /inside the selected workspace/,
      );
      assert.equal(
        (await readFile(calls, "utf8")).trim().split("\n").length,
        4,
        "saved PAT reached issue dispatch",
      );
      scenario = "partial";
      await assert.rejects(
        twgRead(execution, connection, {
          kind: "issues",
          jql: "project = ABC",
        }),
        /inside the selected workspace/,
      );
      scenario = "missing-total";
      await assert.rejects(
        twgRead(execution, connection, { kind: "comments", key: "ABC-1" }),
        /inside the selected workspace/,
      );
    } finally {
      await rm(dir, { recursive: true, force: true });
    }
  });
}

test("Missing sandbox TWG session fails without host fallback", async () => {
  const execution = new WorkspaceExecution({
    id: "fixture",
    path: "/selected",
    mode: "sbx",
    sandbox: "selected",
  });
  let attempts = 0;
  execution.exec = async () => {
    attempts++;
    throw Error("private provider diagnostic");
  };
  await assert.rejects(
    twgRead(execution, connection, { kind: "issues", jql: "project = ABC" }),
    /inside the selected workspace/,
  );
  assert.equal(attempts, 1);
});
