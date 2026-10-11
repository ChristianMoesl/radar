// Scheduled readiness checks are only an index filter. The production Go
// generator authenticates every archive before native tests and again before
// publication; this helper never provides unsigned hashes to the formula.
import { appendFileSync, readFileSync, realpathSync } from 'node:fs';
import { lstat, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { execFile } from 'node:child_process';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { promisify } from 'node:util';
import { fileURLToPath } from 'node:url';

const api = 'https://api.github.com';
const repository = 'ChristianMoesl/radar';
const tap = 'ChristianMoesl/homebrew-tap';
const tapFile = `${api}/repos/${tap}/contents/Formula/radar.rb`;
const tapSSH = `git@github.com:${tap}.git`;
const exec = promisify(execFile);
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
  if (file?.type !== 'file' || file.encoding !== 'base64' || typeof file.content !== 'string') {
    throw new Error('GitHub did not return the expected tap formula');
  }
  return Buffer.from(file.content, 'base64').toString('utf8');
}

// Inject only the HTTP transport in tests; production endpoints are fixed.
export function automationClient(token, send = fetch) {
  return async (url, { optional = false } = {}) => {
    const headers = { Accept: 'application/json', 'User-Agent': 'Radar-Homebrew-publisher' };
    if (url.startsWith(`${api}/`)) {
      headers.Accept = 'application/vnd.github+json';
      headers['X-GitHub-Api-Version'] = '2022-11-28';
      if (token) headers.Authorization = `Bearer ${token}`;
    }
    const response = await send(url, { headers, signal: AbortSignal.timeout(30_000) });
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

async function runGit(args, cwd) {
  const { stdout } = await exec('git', args, {
    cwd, encoding: 'utf8', timeout: 60_000,
    env: { ...process.env, GIT_TERMINAL_PROMPT: '0' },
  });
  return stdout;
}

// The workflow authenticates the exact candidate before granting SSH access.
// Inject Git execution only in tests; production has one fixed SSH destination.
export async function publishUpdate(version, formula, git = runGit) {
  validateVersion(version);
  if (!formula.startsWith('# Generated by radar/scripts/homebrew;') || formulaVersion(formula) !== version) {
    throw new Error('candidate is not the generated formula for the requested release');
  }
  const directory = await mkdtemp(join(process.env.RUNNER_TEMP ?? tmpdir(), 'radar-homebrew-tap-'));
  const checkout = join(directory, 'tap');
  try {
    await git(['clone', '--depth', '1', '--single-branch', '--branch', 'main', '--', tapSSH, checkout]);
    const formulaDirectory = join(checkout, 'Formula');
    const path = join(formulaDirectory, 'radar.rb');
    if (!(await lstat(formulaDirectory)).isDirectory() || !(await lstat(path)).isFile()) {
      throw new Error('tap formula must be a regular file in a real Formula directory');
    }
    const current = formulaVersion(await readFile(path, 'utf8'));
    if (compare(current, version) >= 0) {
      return { published: false, reason: `Tap already contains ${current}; no overwrite or downgrade performed.` };
    }
    await writeFile(path, formula);
    await git(['add', '--', 'Formula/radar.rb'], checkout);
    // A tap .gitattributes filter must not change the authenticated bytes.
    if (await git(['show', ':Formula/radar.rb'], checkout) !== formula) {
      throw new Error('staged tap formula differs from the authenticated candidate');
    }
    await git(['config', 'user.name', 'github-actions[bot]'], checkout);
    await git(['config', 'user.email', '41898282+github-actions[bot]@users.noreply.github.com'], checkout);
    await git(['commit', '-m', `chore: update Radar to ${version}`], checkout);
    // Normal push rejects any concurrent main update. Never force or rebase a
    // candidate after validation; the next scheduled run re-reads current state.
    await git(['push', '--no-follow-tags', 'origin', 'HEAD:refs/heads/main'], checkout);
    return { published: true, reason: `Published ${version} to ${tap}/Formula/radar.rb over SSH after all native checks and final reauthentication.` };
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
}

function summary(reason) {
  console.log(reason);
  if (process.env.GITHUB_STEP_SUMMARY) appendFileSync(process.env.GITHUB_STEP_SUMMARY, `## Homebrew publication\n\n${reason}\n`);
}
async function run([command, version, candidate]) {
  if (command === 'prepare' && !version && !candidate) {
    if (!process.env.GITHUB_OUTPUT) throw new Error('prepare must run as a GitHub Actions step');
    let result;
    const configured = process.env.TAP_SSH_KEY_CONFIGURED === 'true';
    if (!configured && process.env.GITHUB_EVENT_NAME !== 'workflow_dispatch') {
      result = noUpdate('Automatic publication is disabled until the Radar Actions secret HOMEBREW_TAP_SSH_KEY is configured.');
    } else {
      result = await prepareUpdate(automationClient(process.env.GH_TOKEN), process.env.REQUESTED_VERSION ?? '');
    }
    appendFileSync(process.env.GITHUB_OUTPUT, `ready=${result.ready}\nversion=${result.version}\nauto_publish=${configured}\n`);
    summary(result.reason);
    if (!configured && result.ready) summary('HOMEBREW_TAP_SSH_KEY is not configured; native validation and the downloadable formula remain available, but publication is skipped.');
  } else if (command === 'publish' && version && candidate) {
    if (!process.env.GIT_SSH_COMMAND) throw new Error('Use scripts/homebrew/publish.sh to configure the SSH deploy key before publication');
    const result = await publishUpdate(version, readFileSync(candidate, 'utf8'));
    summary(result.reason);
  } else throw new Error('usage: automation.mjs prepare | publish <vX.Y.Z> <authenticated-formula>');
}
if (process.argv[1] && realpathSync(process.argv[1]) === fileURLToPath(import.meta.url)) {
  run(process.argv.slice(2)).catch(error => { console.error(error.message); process.exitCode = 1; });
}
