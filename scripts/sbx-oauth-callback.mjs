// Ephemeral transport for Pup's localhost callback on a disposable CI runner.
// Only the runner holds the private key. Workflow inputs contain ciphertext.
import { generateKeyPairSync, randomBytes, createCipheriv, createDecipheriv,
  publicEncrypt, privateDecrypt, constants } from "node:crypto";
import { readFileSync, writeFileSync } from "node:fs";

const [operation, keyPath, run] = process.argv.slice(2);
if (!/^\d+$/.test(run ?? "")) throw Error("Invalid test run");
if (operation === "keygen") {
  const pair = generateKeyPairSync("rsa", { modulusLength: 3072,
    publicKeyEncoding: { type: "spki", format: "pem" },
    privateKeyEncoding: { type: "pkcs8", format: "pem" } });
  writeFileSync(keyPath, pair.privateKey, { mode: 0o600 });
  writeFileSync(keyPath + ".pub", pair.publicKey, { mode: 0o644 });
} else {
  const input = readFileSync(0, "utf8").trim();
  if (input.length > 32768) throw Error("Callback exceeds limit");
  const aad = Buffer.from("radar-sbx-oauth-proxy:" + run);
  const options = { key: readFileSync(keyPath), padding: constants.RSA_PKCS1_OAEP_PADDING, oaepHash: "sha256" };
  if (operation === "seal") {
    const key = randomBytes(32), iv = randomBytes(12);
    const cipher = createCipheriv("aes-256-gcm", key, iv);
    cipher.setAAD(aad);
    const data = Buffer.concat([cipher.update(input, "utf8"), cipher.final()]);
    process.stdout.write(Buffer.from(JSON.stringify({ version: 1, run,
      key: publicEncrypt(options, key).toString("base64"), iv: iv.toString("base64"),
      data: data.toString("base64"), tag: cipher.getAuthTag().toString("base64") })).toString("base64url"));
  } else if (operation === "open") {
    const packet = JSON.parse(Buffer.from(input, "base64url").toString());
    if (packet.version !== 1 || packet.run !== run) throw Error("Wrong test run");
    const key = privateDecrypt(options, Buffer.from(packet.key, "base64"));
    const decipher = createDecipheriv("aes-256-gcm", key, Buffer.from(packet.iv, "base64"));
    decipher.setAAD(aad);
    decipher.setAuthTag(Buffer.from(packet.tag, "base64"));
    process.stdout.write(Buffer.concat([decipher.update(Buffer.from(packet.data, "base64")), decipher.final()]));
  } else throw Error("Unknown callback operation");
}
