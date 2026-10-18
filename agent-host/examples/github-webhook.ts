#!/usr/bin/env node
import { serveGithubWebhook } from "../src/adapters.ts";

const { RADAR_EVENT_URL, RADAR_EVENT_TOKEN, GITHUB_WEBHOOK_SECRET } =
  process.env;
if (!RADAR_EVENT_URL || !RADAR_EVENT_TOKEN || !GITHUB_WEBHOOK_SECRET) {
  throw new Error(
    "configure RADAR_EVENT_URL, RADAR_EVENT_TOKEN, and GITHUB_WEBHOOK_SECRET",
  );
}
serveGithubWebhook({
  url: RADAR_EVENT_URL,
  token: RADAR_EVENT_TOKEN,
  secret: GITHUB_WEBHOOK_SECRET,
  port: Number(process.env.PORT ?? 7434),
});
console.error("GitHub webhook bridge is listening on loopback");
