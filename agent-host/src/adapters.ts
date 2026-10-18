import { createHmac, timingSafeEqual } from "node:crypto";
import {
  createServer,
  type IncomingMessage,
  type ServerResponse,
} from "node:http";
import {
  MAX_FRAME_BYTES,
  type IncomingEvent,
  type JsonValue,
} from "./index.ts";

export type EventTarget = { url: string; token: string };

export async function forwardEvent(
  target: EventTarget,
  event: IncomingEvent,
  signal?: AbortSignal,
): Promise<unknown> {
  const response = await fetch(target.url, {
    method: "POST",
    headers: {
      Authorization: `Bearer ${target.token}`,
      "Content-Type": "application/json",
    },
    body: JSON.stringify(event),
    signal: signal ?? AbortSignal.timeout(12000),
  });
  if (!response.ok)
    throw new Error(
      `event admission failed (${response.status}); retry the same event identity`,
    );
  return response.json();
}

export function githubSignature(
  secret: string,
  body: Uint8Array,
  signature: string | undefined,
): boolean {
  if (!secret || !signature || !/^sha256=[0-9a-f]{64}$/.test(signature))
    return false;
  const expected = createHmac("sha256", secret).update(body).digest();
  return timingSafeEqual(expected, Buffer.from(signature.slice(7), "hex"));
}

export function githubWebhookHandler(
  options: EventTarget & { secret: string },
) {
  return async (
    request: IncomingMessage,
    response: ServerResponse,
  ): Promise<void> => {
    if (request.method !== "POST") {
      response.writeHead(405).end();
      return;
    }
    try {
      const chunks: Buffer[] = [];
      let size = 0;
      for await (const chunk of request) {
        size += chunk.length;
        if (size > MAX_FRAME_BYTES) {
          response.writeHead(413).end();
          request.destroy();
          return;
        }
        chunks.push(Buffer.from(chunk));
      }
      const body = Buffer.concat(chunks);
      if (
        !githubSignature(
          options.secret,
          body,
          request.headers["x-hub-signature-256"] as string | undefined,
        )
      ) {
        response.writeHead(401).end();
        return;
      }
      const delivery = request.headers["x-github-delivery"];
      const eventType = request.headers["x-github-event"];
      if (typeof delivery !== "string" || typeof eventType !== "string") {
        response.writeHead(400).end();
        return;
      }
      if (eventType === "ping") {
        response.writeHead(204).end();
        return;
      }
      const payload = JSON.parse(body.toString()) as JsonValue;
      const receipt = await forwardEvent(options, {
        source: "github.webhook",
        id: delivery,
        payload: { type: "github.webhook", eventType, body: payload },
      });
      response
        .writeHead(202, { "Content-Type": "application/json" })
        .end(JSON.stringify(receipt));
    } catch {
      response
        .writeHead(503)
        .end(
          "Delivery was not acknowledged; redeliver with the same GitHub delivery ID.",
        );
    }
  };
}

export function serveGithubWebhook(
  options: EventTarget & { secret: string; port?: number; hostname?: string },
) {
  const server = createServer((request, response) => {
    void githubWebhookHandler(options)(request, response);
  });
  server.requestTimeout = 15000;
  server.headersTimeout = 5000;
  server.listen(options.port ?? 7434, options.hostname ?? "127.0.0.1");
  return server;
}
