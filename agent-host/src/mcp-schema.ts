import { Ajv } from "ajv";
import { Ajv2020 } from "ajv/dist/2020.js";
import formats from "ajv-formats";

// No coercion, defaults, property removal, remote references or schema downloads.
const options = { strict: true, allowUnionTypes: true, addUsedSchema: false, logger: false as const };
const draft7 = new Ajv(options);
const draft2020 = new Ajv2020(options);
(formats as unknown as (ajv: Ajv) => void)(draft7);
(formats as unknown as (ajv: Ajv2020) => void)(draft2020);

export function mcpInputValidator(schema: unknown): (args: unknown) => void {
  let check: ReturnType<Ajv["compile"]>;
  try {
    if (!schema || typeof schema !== "object" || Array.isArray(schema) ||
        (schema as { type?: unknown }).type !== "object" || "$async" in schema) throw new Error();
    const dialect = (schema as { $schema?: string }).$schema;
    const compiler = dialect === "http://json-schema.org/draft-07/schema#"
      ? draft7 : draft2020;
    check = compiler.compile(structuredClone(schema));
  } catch {
    throw new Error("MCP input schema is invalid or unsupported");
  }
  return (args) => {
    if (!check(args)) throw new Error("MCP arguments do not match the selected tool schema");
  };
}
