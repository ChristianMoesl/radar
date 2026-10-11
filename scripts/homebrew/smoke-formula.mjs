// CI-only fixture: test the production formula's Linux behavior against a local
// native archive. This is NOT an authenticated release/tap generator; never
// publish this formula. Production generation uses signed public metadata + npm.
import { createHash } from 'node:crypto';
import { lstatSync, readFileSync, realpathSync, writeFileSync } from 'node:fs';
import { basename } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

export function smokeFormula(archive) {
  const path = realpathSync(archive);
  if (!lstatSync(archive).isFile()) throw new Error('archive must be a regular file');
  const match = basename(path).match(/^radar_(v\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?)_linux_(amd64|arm64)\.tar\.gz$/);
  if (!match) throw new Error('expected a versioned Linux release archive');
  const [, tag, arch] = match;
  const template = readFileSync(new URL('testdata/Formula/radar.rb', import.meta.url), 'utf8');
  const hash = createHash('sha256').update(readFileSync(path)).digest('hex');
  // Replace only the native Linux download block, retaining install/test/caveats.
  // Fail if the golden fixture changes instead of silently using a remote URL.
  const nativeDownload = new RegExp(`url "[^"]+/radar_v1\\.2\\.3_linux_${arch}\\.tar\\.gz"\\n      sha256 "[a-f0-9]{64}"`, 'g');
  if (!template.includes('version "1.2.3"') || [...template.matchAll(nativeDownload)].length !== 1) {
    throw new Error('Homebrew fixture changed; update the local smoke substitutions');
  }
  // The source is an explicit local fixture, never an unsigned remote release.
  return template.replace(/^# Generated.*$/m, '# LOCAL CI SMOKE FIXTURE ONLY. Do not publish.')
    .replace('version "1.2.3"', `version "${tag.slice(1)}"`)
    .replace(nativeDownload, `url "${pathToFileURL(path).href}"\n      sha256 "${hash}"`);
}

if (process.argv[1] && realpathSync(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    if (process.argv.length !== 4) throw new Error('usage: smoke-formula.mjs <local-linux-archive> <output-formula>');
    writeFileSync(process.argv[3], smokeFormula(process.argv[2]));
  } catch (error) {
    console.error(error.message);
    process.exitCode = 1;
  }
}
