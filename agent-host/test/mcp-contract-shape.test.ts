import assert from "node:assert/strict";
import test from "node:test";
import { schemaShape } from "./mcp-contract-shape.ts";

test("native contract discovery keeps property shapes without provider values", () => {
  const shape = schemaShape({
    type: "object", required: ["body", "properties"], additionalProperties: false,
    description: "private-description", examples: ["private-example"],
    properties: {
      body: { type: "string", default: "private-default", enum: ["private-enum"] },
      properties: { type: "array", items: { type: "object", properties: {
        key: { type: "string" }, value: { anyOf: [{ type: "object", additionalProperties: true }, { type: "null" }] },
      }, required: ["key", "value"], additionalProperties: false } },
    },
  });
  assert.equal(JSON.stringify(shape).includes("private"), false);
  assert.deepEqual(shape.required, ["body", "properties"]);
  assert.equal(shape.additionalProperties, false);
  const properties = shape.properties as Record<string, any>;
  assert.deepEqual(properties.body, { type: "string" });
  assert.deepEqual(properties.properties.items.required, ["key", "value"]);
  assert.deepEqual(properties.properties.items.properties.value.anyOf,
    [{ type: "object", additionalProperties: true }, { type: "null" }]);
});
