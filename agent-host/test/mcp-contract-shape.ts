const types = new Set(["object", "array", "string", "number", "integer", "boolean", "null"]);

// Retain structural API requirements only. Provider descriptions, defaults,
// examples, enum values and extensions must never enter qualification logs.
export function schemaShape(value: unknown, depth = 0): Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value) || depth > 12) return {};
  const input = value as Record<string, unknown>;
  const result: Record<string, unknown> = {};
  if (typeof input.type === "string" && types.has(input.type)) result.type = input.type;
  if (Array.isArray(input.type)) result.type = input.type.filter(type => types.has(type));
  if (input.properties && typeof input.properties === "object" && !Array.isArray(input.properties)) {
    const properties = input.properties as Record<string, unknown>;
    result.properties = Object.fromEntries(Object.entries(properties).map(([name, property]) =>
      [name, schemaShape(property, depth + 1)]));
    if (Array.isArray(input.required)) result.required = input.required.filter(name =>
      typeof name === "string" && Object.hasOwn(properties, name));
  }
  if (input.items && typeof input.items === "object") result.items = schemaShape(input.items, depth + 1);
  if (typeof input.additionalProperties === "boolean") result.additionalProperties = input.additionalProperties;
  else if (input.additionalProperties && typeof input.additionalProperties === "object")
    result.additionalProperties = schemaShape(input.additionalProperties, depth + 1);
  for (const key of ["anyOf", "oneOf", "allOf"])
    if (Array.isArray(input[key])) result[key] = input[key].map(item => schemaShape(item, depth + 1));
  return result;
}
