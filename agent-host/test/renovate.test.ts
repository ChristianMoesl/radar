import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import test from "node:test";
import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import { AgentHost } from "../src/host.ts";
import { taskId, type JsonValue } from "../src/index.ts";

const entrypoint = resolve(import.meta.dirname, "../examples/renovate.ts");
const pr = {
  type: "github.pull_request",
  nodeId: "PR_1",
  repository: "acme/app",
  number: 1,
  author: "renovate",
  authorType: "Bot",
  state: "OPEN",
  headOid: "aaa",
  url: "https://github.com/acme/app/pull/1",
};
const configuration = {
  renovateAuthor: "renovate[bot]",
  comment: "@review-agent please review this PR.",
  includeExisting: false,
};

async function fixture(includeExisting = false) {
  const dir = await mkdtemp(join(tmpdir(), "radar-renovate-"));
  const input = {
    id: "renovate",
    dataDir: dir,
    workspace: { id: "fixture", path: dir, mode: "local" as const },
    config: { ...configuration, includeExisting },
  };
  let host = await AgentHost.open(entrypoint, input);
  const comments: { id: number; body: string; html_url: string }[] = [];
  let posts = 0;
  let readFailure = false;
  let postFailure = false;
  let lostResponse = false;
  let currentState = "open";
  const install = () => {
    host.host.execution.exec = async () => {
      throw new Error("GitHub service calls must not execute in the workspace");
    };
    host.host.mcp.json = async (server, tool, args, _signal, readOnly) => {
      assert.equal(server, "github");
      assert.equal(args.owner, "acme");
      assert.equal(args.repo, "app");
      if (tool === "add_issue_comment") {
        assert.equal(readOnly, false);
        posts++;
        if (postFailure) throw new Error("GitHub posting unavailable");
        const body = String(args.body);
        const comment = {
          id: posts,
          body,
          html_url: `https://github.com/acme/app/issues/1#issuecomment-${posts}`,
        };
        comments.push(comment);
        if (lostResponse) throw new Error("POST response lost");
        return comment;
      }
      if (tool === "issue_read") {
        assert.equal(args.method, "get_comments");
        if (readFailure) throw new Error("GitHub comments unavailable");
        const page = Number(args.page);
        return comments.slice((page - 1) * 100, page * 100);
      }
      assert.equal(tool, "pull_request_read");
      return {
        node_id: pr.nodeId,
        state: currentState,
        user: { login: "renovate[bot]" },
      };
    };
  };
  install();
  const wait = async (receipt: { taskId: number }) => {
    const settled = await host.harness.waitForTask(
      taskId(receipt.taskId),
      BACKGROUND_CONTEXT,
    );
    assert.equal(
      settled.state.outcome.status,
      "completed",
      JSON.stringify(settled.state),
    );
  };
  const deliver = async (
    id: string,
    data: JsonValue = pr,
    baseline = false,
    change = "discovered",
  ) => {
    const event = {
      source: "radar.collection.github",
      id,
      payload: { change, baseline, data },
    };
    const receipt = await host.deliver(event);
    await wait(receipt);
    return { event, receipt };
  };
  const reconcile = async (id: string) => {
    const snapshot = await host.snapshot();
    const action = snapshot.presentation.actions[0]!;
    assert.ok(action, "missing visible reconciliation action");
    await wait(
      await host.deliver(
        { source: "radar.user", id, payload: null },
        { revision: snapshot.presentation.revision, action: action.id },
      ),
    );
  };
  return {
    get host() {
      return host;
    },
    comments,
    get posts() {
      return posts;
    },
    deliver,
    reconcile,
    failReads(value: boolean) {
      readFailure = value;
    },
    failPosts(value: boolean) {
      postFailure = value;
    },
    loseResponse() {
      lostResponse = true;
    },
    closePR() {
      currentState = "closed";
    },
    async restart() {
      await host.close();
      host = await AgentHost.open(entrypoint, input);
      install();
    },
    async close() {
      await host.close();
      await rm(dir, { recursive: true, force: true });
    },
  };
}

test("Renovate tagging requires the exact comment and an explicit backlog decision", async () => {
  const dir = await mkdtemp(join(tmpdir(), "radar-renovate-config-"));
  try {
    await assert.rejects(
      AgentHost.open(entrypoint, {
        id: "renovate",
        dataDir: dir,
        workspace: { id: "fixture", path: dir, mode: "local" },
        config: {
          renovateAuthor: "renovate",
          comment: "@review-agent please review",
        },
      }),
      /includeExisting/,
    );
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
});

test("baseline provenance, author and open-state filters prevent unwanted tagging", async () => {
  const f = await fixture();
  try {
    // A later head event can execute before its initial snapshot event.
    await f.deliver("baseline-changed", pr, true, "changed");
    await f.deliver("baseline", pr, true, "snapshot");
    await f.deliver("rebase", { ...pr, headOid: "bbb" });
    await f.deliver("other-author", {
      ...pr,
      nodeId: "PR_2",
      number: 2,
      author: "someone",
    });
    await f.deliver("closed", {
      ...pr,
      nodeId: "PR_3",
      number: 3,
      state: "CLOSED",
    });
    assert.equal(f.posts, 0);
    await f.restart();
    await f.deliver("reopened", { ...pr, headOid: "ccc" });
    assert.equal(f.posts, 0, "restart forgot the excluded baseline");
  } finally {
    await f.close();
  }
});

test("new PRs tag once across concurrent revisions, receipt retries and host restart", async () => {
  const f = await fixture();
  try {
    await Promise.all([
      f.deliver("one"),
      f.deliver("two", { ...pr, headOid: "bbb" }),
      f.deliver("three", { ...pr, headOid: "ccc" }),
    ]);
    assert.equal(f.posts, 1);
    await f.restart();
    await f.deliver("four", { ...pr, headOid: "ddd" });
    const { event, receipt } = await f.deliver("retry");
    assert.equal((await f.host.deliver(event)).taskId, receipt.taskId);
    assert.equal(f.posts, 1);
    assert.match(
      f.comments[0]!.body,
      /^@review-agent please review this PR\.\n\n<!-- radar-renovate:[a-f0-9]+ -->$/,
    );
    assert.equal((await f.host.snapshot()).presentation.attention, false);
  } finally {
    await f.close();
  }
});

test("explicit backlog inclusion tags the initial snapshot without a model", async () => {
  const f = await fixture(true);
  try {
    await f.deliver("snapshot", pr, true, "snapshot");
    assert.equal(f.posts, 1);
  } finally {
    await f.close();
  }
});

test("a lost POST response reconciles its marker, including paginated comments", async () => {
  const f = await fixture();
  try {
    for (let i = 0; i < 100; i++)
      f.comments.push({ id: 1000 + i, body: "unrelated", html_url: "" });
    f.loseResponse();
    await f.deliver("lost");
    assert.equal(f.posts, 1);
    assert.equal(f.comments.length, 101);
    assert.equal((await f.host.snapshot()).presentation.attention, false);
    await f.restart();
    await f.deliver("updated", { ...pr, headOid: "bbb" });
    assert.equal(f.posts, 1);
  } finally {
    await f.close();
  }
});

test("failed reads are visible and can be retried; uncertain posts never repeat blindly", async () => {
  const f = await fixture();
  try {
    f.failReads(true);
    await f.deliver("read-failure");
    assert.equal(f.posts, 0);
    assert.equal((await f.host.snapshot()).presentation.attention, true);
    f.failReads(false);
    f.failPosts(true);
    await f.reconcile("retry-before-post");
    assert.equal(f.posts, 1);
    await f.restart();
    f.failPosts(false);
    await f.reconcile("uncertain");
    await f.deliver("another-head", { ...pr, headOid: "bbb" });
    assert.equal(f.posts, 1, "uncertain posting was retried without evidence");
    const snapshot = await f.host.snapshot();
    assert.equal(snapshot.presentation.attention, true);
    assert.match(snapshot.presentation.summary, /uncertain/);
    // A subsequently visible marker resolves uncertainty without another POST.
    const { createHash } = await import("node:crypto");
    const marker = createHash("sha256").update("renovate:PR_1").digest("hex");
    f.comments.push({
      id: 99,
      body: `@review-agent please review this PR.\n\n<!-- radar-renovate:${marker} -->`,
      html_url: "https://github.com/acme/app/issues/1#issuecomment-99",
    });
    await f.reconcile("now-visible");
    assert.equal(f.posts, 1);
    assert.equal((await f.host.snapshot()).presentation.attention, false);
  } finally {
    await f.close();
  }
});

test("catch-up rechecks live PR state before commenting", async () => {
  const f = await fixture();
  try {
    f.closePR();
    await f.deliver("delayed");
    assert.equal(f.posts, 0);
  } finally {
    await f.close();
  }
});
