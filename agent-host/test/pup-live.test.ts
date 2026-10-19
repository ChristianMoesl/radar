import assert from "node:assert/strict";
import test from "node:test";
import { WorkspaceExecution } from "../src/environment.ts";
import { pupRequest, pupSearchSpans } from "../examples/pup.ts";

test("Live Pup telemetry through the selected workspace", {
  skip: !process.env.RADAR_TEST_DATADOG_SITE,
  timeout: 150000,
}, async (context) => {
  const required = (key: string): string => {
    const value = process.env[key];
    assert.ok(value, `${key} must explicitly select approved live reads`);
    return value;
  };
  const site = required("RADAR_TEST_DATADOG_SITE");
  const org = required("RADAR_TEST_DATADOG_ORG");
  const path = required("RADAR_TEST_DATADOG_WORKSPACE");
  const mode = process.env.RADAR_TEST_DATADOG_EXECUTION ?? "local";
  assert.ok(mode === "local" || mode === "sbx");
  const execution = new WorkspaceExecution({ id: "pup-live", path, mode });
  try {
    const to = Math.floor(Date.now() / 1000), from = to - 900;
    const query = required("RADAR_TEST_DATADOG_METRIC_QUERY");
    await context.test("metric query", async (metricTest) => {
      const metrics = await pupRequest(execution, site, org,
        `/api/v1/query?from=${from}&to=${to}&query=${encodeURIComponent(query)}`) as Record<string, any>;
      assert.equal(metrics.status, "ok");
      assert.ok(Array.isArray(metrics.series));
      assert.ok(metrics.series.some((series: Record<string, any>) =>
        Array.isArray(series.pointlist) && series.pointlist.some((point: unknown[]) =>
          Array.isArray(point) && typeof point[1] === "number" && Number.isFinite(point[1]))),
      "live metric verification requires at least one actual numeric sample");
      metricTest.diagnostic(`${metrics.series.length} series through ${mode} workspace execution`);
    });

    for (const [kind, route, queryKey] of [
      ["logs", "/api/v2/logs/events/search", "RADAR_TEST_DATADOG_LOGS_QUERY"],
      ["spans", "/api/v2/spans/events/search", "RADAR_TEST_DATADOG_SPANS_QUERY"],
    ] as const) {
      const scopedQuery = process.env[queryKey];
      await context.test(kind, { skip: !scopedQuery }, async (telemetryTest) => {
        const fromISO = new Date(from * 1000).toISOString();
        const toISO = new Date(to * 1000).toISOString();
        const response = (kind === "spans"
          ? await pupSearchSpans(execution, site, org, scopedQuery!, fromISO, toISO)
          : await pupRequest(execution, site, org, route, {
            method: "POST",
            body: JSON.stringify({
              filter: { query: scopedQuery, from: fromISO, to: toISO },
              page: { limit: 1 },
            }),
          })) as Record<string, any>;
        assert.ok(Array.isArray(response.data), `${kind} must return structured results`);
        telemetryTest.diagnostic(`${response.data.length} returned event(s); empty results verify access, not ingestion`);
      });
    }
  } finally {
    execution.close();
  }
});
