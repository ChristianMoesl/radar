// Scheduled readiness checks are only an index filter. The production Go
// generator authenticates every archive before native tests and again before
// publication; this helper never provides unsigned hashes to the formula.
import { appendFileSync, readFileSync, realpathSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

const api = 'https://api.github.com';
const repository = 'ChristianMoesl/radar';
const tap = 'ChristianMoesl/homebrew-tap';
const tapFile = `${api}/repos/${tap}/contents/Formula/radar.rb`;
const npmPackage = '@christianmoesl/pi-radar';
const stable = /^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$/;

function isStable(version) {
  // JavaScript's $ also matches before a final newline; require the whole value.
  return typeof version === 'string' && version.length <= 64 && stable.exec(version)?.[0] === version;
}
function validateVersion(version) {
  if (!isStable(version)) {
    throw new Error('Homebrew publication requires a stable vX.Y.Z version');
  }
  return version;
}
function compare(a, b) {
  const left = validateVersion(a).slice(1).split('.').map(BigInt);
  const right = validateVersion(b).slice(1).split('.').map(BigInt);
  for (let i = 0; i < 3; i++) {
    if (left[i] !== right[i]) return left[i] > right[i] ? 1 : -1;
  }
  return 0;
}
export function formulaVersion(formula) {
  const matches = [...formula.matchAll(/^  version "([^"]+)"$/gm)];
  if (matches.length !== 1) throw new Error('tap formula must have one explicit version');
  return validateVersion(`v${matches[0][1]}`);
}
function tapContents(file) {
  if (file?.type !== 'file' || file.encoding !== 'base64' || typeof file.content !== 'string' || !/^[a-f0-9]{40}$/.test(file.sha)) {
    throw new Error('GitHub did not return the expected tap formula');
  }
  return Buffer.from(file.content, 'base64').toString('utf8');
}

// Inject only the HTTP transport in tests; production endpoints are fixed.
export function automationClient(token, send = fetch) {
  return async (url, { optional = false, ...options } = {}) => {
    const headers = { Accept: 'application/json', 'User-Agent': 'Radar-Homebrew-publisher' };
    if (url.startsWith(`${api}/`)) {
      headers.Accept = 'application/vnd.github+json';
      headers['X-GitHub-Api-Version'] = '2022-11-28';
      if (token) headers.Authorization = `Bearer ${token}`;
    }
    if (options.body) headers['Content-Type'] = 'application/json';
    const response = await send(url, { ...options, headers, signal: AbortSignal.timeout(30_000) });
    if (optional && response.status === 404) return null;
    if (!response.ok) throw new Error(`Homebrew automation request failed (HTTP ${response.status})`);
    return response.json();
  };
}

const noUpdate = reason => ({ ready: false, version: '', reason });
export async function prepareUpdate(request, requestedVersion = '') {
  if (requestedVersion) validateVersion(requestedVersion);
  const current = formulaVersion(tapContents(await request(`${tapFile}?ref=main`)));
  const releases = [];
  for (let page = 1; page <= 5; page++) {
    const items = await request(`${api}/repos/${repository}/releases?per_page=100&page=${page}`);
    if (!Array.isArray(items)) throw new Error('GitHub did not return a release list');
    releases.push(...items.filter(item => !item.draft && !item.prerelease && isStable(item.tag_name)));
    if (items.length < 100) break;
  }
  const candidates = releases.filter(item => requestedVersion ? item.tag_name === requestedVersion : compare(item.tag_name, current) > 0)
    .sort((a, b) => compare(b.tag_name, a.tag_name));
  if (requestedVersion && candidates.length === 0) throw new Error('requested version is not a public stable Radar release');
  for (const release of candidates) {
    const version = release.tag_name;
    const platforms = ['darwin_arm64', 'linux_amd64', 'linux_arm64'];
    const names = ['release.json', 'release.json.sig', ...platforms.map(platform => `radar_${version}_${platform}.tar.gz`)];
    if (!Array.isArray(release.assets) || names.some(name => release.assets.filter(asset => asset.name === name && asset.state === 'uploaded' && asset.size > 0).length !== 1)) continue;
    const manifest = await request(`https://github.com/${repository}/releases/download/${version}/release.json`, { optional: true });
    // Old macOS-only signed manifests cannot generate the all-platform formula.
    // This shape check only saves runners; authenticity remains the Go gate.
    if (manifest?.schema !== 1 || manifest.version !== version || platforms.some(platform => manifest.artifacts?.[platform === 'darwin_arm64' ? 'arm64' : platform]?.file !== `radar_${version}_${platform}.tar.gz`)) continue;
    const pkg = await request(`https://registry.npmjs.org/${encodeURIComponent(npmPackage)}/${version.slice(1)}`, { optional: true });
    if (pkg === null) continue; // staged approval is still a human action
    if (pkg.name !== npmPackage || pkg.version !== version.slice(1)) throw new Error('npm returned a different package/version');
    return { ready: true, version, reason: `${version} is public with the exact npm package; native Homebrew validation is required before publication.` };
  }
  return noUpdate('No newer all-platform Radar release is ready with its exact public npm package.');
}

export async function publishUpdate(request, version, formula) {
  validateVersion(version);
  if (!formula.startsWith('# Generated by radar/scripts/homebrew;') || formulaVersion(formula) !== version) {
    throw new Error('candidate is not the generated formula for the requested release');
  }
  const file = await request(`${tapFile}?ref=main`);
  const current = formulaVersion(tapContents(file));
  if (compare(current, version) >= 0) {
    return { published: false, reason: `Tap already contains ${current}; no overwrite or downgrade performed.` };
  }
  // GitHub's expected file SHA makes a concurrent formula edit fail safely.
  // This appends a normal commit; it never rewrites history or bypasses rules.
  await request(tapFile, {
    method: 'PUT',
    body: JSON.stringify({
      message: `chore: update Radar to ${version}`,
      content: Buffer.from(formula).toString('base64'),
      sha: file.sha,
      branch: 'main',
    }),
  });
  return { published: true, reason: `Published ${version} to ${tap}/Formula/radar.rb after all native checks and final reauthentication.` };
}

function summary(reason) {
  console.log(reason);
  if (process.env.GITHUB_STEP_SUMMARY) appendFileSync(process.env.GITHUB_STEP_SUMMARY, `## Homebrew publication\n\n${reason}\n`);
}
async function run([command, version, candidate]) {
  if (command === 'prepare' && !version && !candidate) {
    if (!process.env.GITHUB_OUTPUT) throw new Error('prepare must run as a GitHub Actions step');
    let result;
    const configured = process.env.TAP_TOKEN_CONFIGURED === 'true';
    if (!configured && process.env.GITHUB_EVENT_NAME !== 'workflow_dispatch') {
      result = noUpdate('Automatic publication is disabled until the Radar Actions secret HOMEBREW_TAP_TOKEN is configured.');
    } else {
      result = await prepareUpdate(automationClient(process.env.GH_TOKEN), process.env.REQUESTED_VERSION ?? '');
    }
    appendFileSync(process.env.GITHUB_OUTPUT, `ready=${result.ready}\nversion=${result.version}\nauto_publish=${configured}\n`);
    summary(result.reason);
    if (!configured && result.ready) summary('HOMEBREW_TAP_TOKEN is not configured; native validation and the downloadable formula remain available, but publication is skipped.');
  } else if (command === 'publish' && version && candidate) {
    if (!process.env.GH_TOKEN) throw new Error('HOMEBREW_TAP_TOKEN is required for publication');
    const result = await publishUpdate(automationClient(process.env.GH_TOKEN), version, readFileSync(candidate, 'utf8'));
    summary(result.reason);
  } else throw new Error('usage: automation.mjs prepare | publish <vX.Y.Z> <authenticated-formula>');
}
if (process.argv[1] && realpathSync(process.argv[1]) === fileURLToPath(import.meta.url)) {
  run(process.argv.slice(2)).catch(error => { console.error(error.message); process.exitCode = 1; });
}
