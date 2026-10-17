import assert from "node:assert/strict";
import { createHmac } from "node:crypto";
import { createServer } from "node:http";
import { once } from "node:events";
import test from "node:test";
import { githubWebhookHandler } from "../src/adapters.ts";

test("GitHub bridge verifies the original body and preserves delivery identity", async () => {
  const admitted: unknown[] = [];
  const target = createServer(async (req, res) => {
    let body = "";
    for await (const chunk of req) body += chunk;
    admitted.push(JSON.parse(body));
    res
      .writeHead(202, { "Content-Type": "application/json" })
      .end('{"taskId":1}');
  });
  target.listen(0, "127.0.0.1");
  await once(target, "listening");
  const targetPort = (target.address() as { port: number }).port;
  const bridge = createServer(
    (req, res) =>
      void githubWebhookHandler({
        url: `http://127.0.0.1:${targetPort}/agents/reviewer/events`,
        token: "fixture",
        secret: "webhook-fixture",
      })(req, res),
  );
  bridge.listen(0, "127.0.0.1");
  await once(bridge, "listening");
  const port = (bridge.address() as { port: number }).port;
  const body = '{ "action": "review_requested", "number": 7 }';
  const signature =
    "sha256=" +
    createHmac("sha256", "webhook-fixture").update(body).digest("hex");
  try {
    for (const signed of [false, true, true]) {
      const response = await fetch(`http://127.0.0.1:${port}`, {
        method: "POST",
        body,
        headers: {
          "x-hub-signature-256": signed
            ? signature
            : "sha256=" + "0".repeat(64),
          "x-github-event": "pull_request",
          "x-github-delivery": "delivery-1",
        },
      });
      assert.equal(response.status, signed ? 202 : 401);
    }
    assert.equal(admitted.length, 2);
    assert.deepEqual(admitted[0], admitted[1]);
    assert.deepEqual(admitted[0], {
      source: "github.webhook",
      id: "delivery-1",
      payload: {
        type: "github.webhook",
        eventType: "pull_request",
        body: JSON.parse(body),
      },
    });
  } finally {
    await new Promise<void>((done) => bridge.close(() => done()));
    await new Promise<void>((done) => target.close(() => done()));
  }
});
