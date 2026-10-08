#!/usr/bin/env bash
# Trusted bootstrap source: https://github.com/ChristianMoesl/radar
# Release bytes are authenticated separately with the embedded publisher keys.
set -euo pipefail

main() {
  [[ $# = 0 ]] || { echo 'usage: bash install.sh' >&2; return 2; }
  [[ $(uname -s) = Darwin ]] || { echo 'This installer is for macOS. Linux/Windows retain the manual installation described in the README.' >&2; return 1; }
  [[ $(id -u) != 0 ]] || { echo 'Run this installer as your normal user, without sudo.' >&2; return 1; }
  case "$(uname -m)" in
    arm64) arch=arm64; native_brew=/opt/homebrew/bin/brew;;
    x86_64) arch=amd64; native_brew=/usr/local/bin/brew;;
    *) echo 'Unsupported macOS architecture.' >&2; return 1;;
  esac
  prefix="$HOME/.local"
  if command -v radar >/dev/null 2>&1 || [[ -e "$prefix/bin/radar" || -L "$prefix/bin/radar" ]]; then
    echo 'Radar is already installed. Use radar update for managed updates; other installations stay manual.' >&2
    return 1
  fi
  if [[ -e "$prefix/libexec/radar/install.json" || -L "$prefix/libexec/radar/install.json" || -e "$prefix/libexec/radar/.upgrade" || -L "$prefix/libexec/radar/.upgrade" || -e "$prefix/libexec/radar/RadarNotifier.app" || -L "$prefix/libexec/radar/RadarNotifier.app" ]]; then
    echo 'Existing Radar update state needs inspection; this fresh-install bootstrap will not replace it.' >&2
    return 1
  fi
  # A pipe supplies the script, never approval. Headless execution cannot silently
  # install prerequisites or edit profiles; all prompts read the controlling TTY.
  exec 3</dev/tty || { echo 'Run this installer from an interactive terminal.' >&2; return 1; }
  terminal_state=$(stty -g <&3)
  work=$(mktemp -d)
  trap 'stty "$terminal_state" <&3 2>/dev/null || true; rm -rf "$work"' EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM
  ask() {
    local answer=
    printf '%s [y/N] ' "$1" >&2
    read -r answer <&3 || return 1
    [[ "$answer" = y || "$answer" = Y ]]
  }
  download() {
    curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
      --connect-timeout 15 --max-time 60 --max-filesize 1048576 --output "$2" "$1"
  }
  node_ready() {
    command -v node >/dev/null 2>&1 && env -u NODE_OPTIONS -u NODE_PATH node -e 'process.exit(Number(process.versions.node.split(".")[0]) >= 24 ? 0 : 1)'
  }
  brew=
  brew_prefix=
  # Offer the package manager before onboarding needs it, even if Node is ready.
  if ! node_ready || ! command -v npm >/dev/null 2>&1 || ! command -v git >/dev/null 2>&1 || ! command -v tmux >/dev/null 2>&1 || ! command -v fd >/dev/null 2>&1 || ! command -v gh >/dev/null 2>&1; then
    if command -v brew >/dev/null 2>&1; then
      brew=$(command -v brew)
    elif [[ -x "$native_brew" ]]; then
      brew=$native_brew
    else
      echo 'Homebrew can supply the missing workspace tools and Node.js 24+ for publisher verification and Pi.' >&2
      echo 'The official Homebrew installer may request normal macOS administrator approval.' >&2
      ask 'Install Homebrew from https://brew.sh?' || { echo 'Homebrew declined. No Radar files were installed.' >&2; return 1; }
      download https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh "$work/homebrew.sh"
      /bin/bash "$work/homebrew.sh" <&3
      [[ -x "$native_brew" ]] || { echo 'Homebrew is still unavailable; fix its installation before retrying.' >&2; return 1; }
      brew=$native_brew
    fi
    brew_prefix=$("$brew" --prefix)
    export PATH="$brew_prefix/bin:$brew_prefix/sbin:$PATH"
    if ! node_ready; then
      ask 'Install/update Node.js 24+ using Homebrew?' || { echo 'Node.js declined. No Radar files were installed.' >&2; return 1; }
      env -u NODE_OPTIONS -u NODE_PATH "$brew" install node <&3
      node_ready || { echo 'Node.js 24+ is still unavailable. No Radar files were installed.' >&2; return 1; }
    fi
  fi
  echo 'Finding an authenticated, fully published Radar release…' >&2
  # Do not let project/user Node preload hooks alter this verifier. There are no
  # environment overrides for publisher trust, release endpoints or selection.
  env -u NODE_OPTIONS -u NODE_PATH node --input-type=module - "$work" "$arch" "$(sw_vers -productVersion)" "$HOME" <<'NODE'
import { createHash, createPublicKey, verify } from 'node:crypto';
import { lstatSync, mkdirSync, writeFileSync, realpathSync } from 'node:fs';
import { join, posix, resolve } from 'node:path';
import { gunzipSync } from 'node:zlib';

try {
// Keep these independently committed roots aligned with internal/update/keys.json.
const trustedKeys = { "release-1": "Q4QjbCIw3Jjtk0oI/E/+xiaEpwYAsejQA7agVI0VGuY=" };
const stateEpoch = 1;
const maxArchive = 128 * 1024 * 1024, maxExpanded = 512 * 1024 * 1024;
const [work, arch, macos, home] = process.argv.slice(2);
const repository = 'ChristianMoesl/radar', packageName = '@christianmoesl/pi-radar';
const downloads = `https://github.com/${repository}/releases/download`;
const stable = v => typeof v === 'string' && v.length <= 64 && /^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$/.test(v);
const tag = v => typeof v === 'string' && v.startsWith('v') && stable(v.slice(1));
const sha = b => createHash('sha256').update(b).digest('hex');
const digest = v => typeof v === 'string' && /^[a-f0-9]{64}$/.test(v);
const compare = (a, b) => {
  const aa = a.replace(/^v/, '').split('.'), bb = b.replace(/^v/, '').split('.');
  for (let i = 0; i < 3; i++) {
    const x = BigInt(aa[i] ?? 0), y = BigInt(bb[i] ?? 0);
    if (x !== y) return x > y ? 1 : -1;
  }
  return 0;
};
const fields = (v, names) => {
  if (!v || typeof v !== 'object' || Array.isArray(v) || Object.keys(v).length !== names.length || names.some(n => !Object.hasOwn(v, n))) throw new Error('Invalid release metadata fields');
};
const base64 = (s, length) => {
  if (typeof s !== 'string' || Buffer.from(s, 'base64').toString('base64') !== s || Buffer.from(s, 'base64').length !== length) throw new Error('Invalid release signature encoding');
  return Buffer.from(s, 'base64');
};
const overall = AbortSignal.timeout(120000);
async function get(address, limit) {
  for (let redirects = 0; redirects <= 5; redirects++) {
    const url = new URL(address);
    if (url.protocol !== 'https:') throw new Error('Unsafe release redirect');
    const response = await fetch(url, { redirect: 'manual', signal: AbortSignal.any([overall, AbortSignal.timeout(45000)]), headers: { 'User-Agent': 'Radar-bootstrap' } });
    if ([301, 302, 303, 307, 308].includes(response.status)) {
      address = new URL(response.headers.get('location'), url).href;
      await response.body?.cancel();
      continue;
    }
    if (response.status !== 200) { await response.body?.cancel(); throw new Error(`Release service HTTP ${response.status}`); }
    if (Number(response.headers.get('content-length')) > limit) { await response.body?.cancel(); throw new Error('Release response exceeds size limit'); }
    const chunks = []; let size = 0;
    for await (const chunk of response.body) {
      size += chunk.length;
      if (size > limit) throw new Error('Release response exceeds size limit');
      chunks.push(chunk);
    }
    return Buffer.concat(chunks);
  }
  throw new Error('Too many release redirects');
}
function manifest(bytes, sigBytes, version) {
  const sig = JSON.parse(sigBytes); fields(sig, ['key_id', 'signature']);
  if (!Object.hasOwn(trustedKeys, sig.key_id)) throw new Error('Release publisher is not trusted');
  const raw = base64(trustedKeys[sig.key_id], 32);
  const key = createPublicKey({ key: Buffer.concat([Buffer.from('302a300506032b6570032100', 'hex'), raw]), format: 'der', type: 'spki' });
  if (!verify(null, bytes, key, base64(sig.signature, 64))) throw new Error('Invalid release publisher signature');
  const m = JSON.parse(bytes);
  fields(m, ['schema', 'version', 'state_epoch', 'min_macos', 'pi_version', 'min_pi', 'min_node', 'artifacts']);
  if (m.schema !== 1 || m.state_epoch !== stateEpoch || m.version !== version || !tag(m.version) || m.pi_version !== version.slice(1) || !stable(m.min_pi) || !Number.isInteger(m.min_node) || m.min_node < 24 || m.min_node > 100 || !/^[0-9]+\.[0-9]+$/.test(m.min_macos)) throw new Error('Unsupported release contract');
  fields(m.artifacts, ['amd64', 'arm64']);
  for (const a of ['amd64', 'arm64']) {
    const f = m.artifacts[a]; fields(f, ['file', 'size', 'sha256', 'binary_sha256', 'notifier_version', 'notifier_sha256']);
    if (f.file !== `radar_${version}_darwin_${a}.tar.gz` || !Number.isSafeInteger(f.size) || f.size <= 0 || f.size > maxArchive || !digest(f.sha256) || !digest(f.binary_sha256) || !digest(f.notifier_sha256) || !stable(f.notifier_version)) throw new Error('Invalid release artifact');
  }
  return m;
}
function ownedDirectory(path) {
  const s = lstatSync(path);
  if (!s.isDirectory() || s.uid !== process.getuid() || (s.mode & 0o022)) throw new Error('Install directories must be regular, private-to-your-user directories, not shared or symlink paths');
}
// Never follow a custom/symlink installation or overwrite a managed transaction.
if (resolve(home) !== home || realpathSync(home) !== home) throw new Error('HOME must be an absolute canonical path');
ownedDirectory(home);
for (const p of ['.local', '.local/bin', '.local/libexec', '.local/libexec/radar']) {
  try { ownedDirectory(join(home, p)); } catch (e) { if (e.code !== 'ENOENT') throw e; }
}
let selected;
const releases = [];
for (let page = 1; page <= 5; page++) {
  const r = JSON.parse(await get(`https://api.github.com/repos/${repository}/releases?per_page=100&page=${page}`, 8 * 1024 * 1024));
  if (!Array.isArray(r)) throw new Error('Invalid release listing');
  releases.push(...r.filter(r => r && r.draft === false && r.prerelease === false && tag(r.tag_name)));
  if (r.length < 100) break;
}
releases.sort((a, b) => compare(b.tag_name, a.tag_name));
for (const r of releases) {
  try {
    const bytes = await get(`${downloads}/${r.tag_name}/release.json`, 1024 * 1024);
    const sig = await get(`${downloads}/${r.tag_name}/release.json.sig`, 4096);
    const m = manifest(bytes, sig, r.tag_name);
    for (const a of Object.values(m.artifacts)) {
      if (!Array.isArray(r.assets) || r.assets.filter(f => f.name === a.file && f.size === a.size && f.state === 'uploaded').length !== 1) throw new Error('Incomplete release upload');
    }
    const pkg = JSON.parse(await get(`https://registry.npmjs.org/${encodeURIComponent(packageName)}/${m.pi_version}`, 1024 * 1024));
    if (pkg.name !== packageName || pkg.version !== m.pi_version) throw new Error('Matching Pi package is not public');
    selected = m; break;
  } catch (error) {
    if (overall.aborted) throw error;
  }
}
if (!selected) throw new Error('No fully published Radar release is ready yet. Please try again later; no Radar files were installed.');
if (compare(macos, selected.min_macos) < 0 || Number(process.versions.node.split('.')[0]) < selected.min_node) throw new Error(`This release requires macOS ${selected.min_macos}+ and Node.js ${selected.min_node}+`);
const artifact = selected.artifacts[arch];
const archive = await get(`${downloads}/${selected.version}/${artifact.file}`, artifact.size);
if (archive.length !== artifact.size || sha(archive) !== artifact.sha256) throw new Error('Release archive size/hash verification failed');
// Parse the ustar format produced by macOS make dist. Preflight every entry;
// no native tar process sees unaudited paths, links, devices or metadata records.
const tar = gunzipSync(archive, { maxOutputLength: maxExpanded });
const root = artifact.file.slice(0, -7), files = new Map(), dirs = [], seen = new Set();
const decoder = new TextDecoder('utf-8', { fatal: true });
const text = b => decoder.decode(b.subarray(0, b.indexOf(0) < 0 ? b.length : b.indexOf(0)));
const octal = b => { const s = text(b).trim(); if (!/^[0-7]+$/.test(s)) throw new Error('Unsupported tar numeric encoding'); return parseInt(s, 8); };
const required = ['bin/radar', 'README.md', 'LICENSE', 'install.sh', 'install-agent-instructions.sh', 'install-notifier.sh', 'share/radar/AGENTS.md'];
let offset = 0, expanded = 0, count = 0, ended = false;
while (offset + 512 <= tar.length) {
  const h = tar.subarray(offset, offset + 512); offset += 512;
  if (h.every(b => b === 0)) { if (tar.subarray(offset).some(b => b !== 0)) throw new Error('Trailing tar data'); ended = true; break; }
  if (++count > 2000 || text(h.subarray(257, 263)) !== 'ustar') throw new Error('Unsupported release archive format');
  let checksum = 0; for (let i = 0; i < 512; i++) checksum += i >= 148 && i < 156 ? 32 : h[i];
  if (checksum !== octal(h.subarray(148, 156))) throw new Error('Invalid tar checksum');
  const prefix = text(h.subarray(345, 500));
  const name = `${prefix ? prefix + '/' : ''}${text(h.subarray(0, 100))}`.replace(/\/$/, '');
  const size = octal(h.subarray(124, 136)), mode = octal(h.subarray(100, 108));
  const type = h[156];
  if (posix.normalize(name) !== name || /[\\\x00-\x1f\x7f]/.test(name) || !(name === root || name.startsWith(root + '/')) || seen.has(name)) throw new Error('Unsafe or duplicate archive path');
  seen.add(name);
  const rel = name === root ? '' : name.slice(root.length + 1);
  if (![0, 48, 53].includes(type) || type === 53 && size !== 0) throw new Error('Archive links, special files and metadata records are not permitted');
  if (size > maxExpanded - expanded || offset + Math.ceil(size / 512) * 512 > tar.length) throw new Error('Truncated or oversized archive');
  expanded += size;
  if (type === 53) dirs.push(name);
  else {
    if (!required.includes(rel) && !rel.startsWith('libexec/radar/RadarNotifier.app/Contents/')) throw new Error('Unexpected release file');
    files.set(rel, { data: tar.subarray(offset, offset + size), executable: (mode & 0o111) !== 0 });
  }
  offset += Math.ceil(size / 512) * 512;
}
if (!ended || required.some(p => !files.has(p)) || !files.get('bin/radar').executable || !files.get('install.sh').executable) throw new Error('Incomplete release archive');
if (sha(files.get('bin/radar').data) !== artifact.binary_sha256) throw new Error('Radar binary identity mismatch');
const bundlePrefix = 'libexec/radar/RadarNotifier.app/';
const bundle = [...files.keys()].filter(p => p.startsWith(bundlePrefix)).sort((a, b) => Buffer.compare(Buffer.from(a), Buffer.from(b)));
if (!bundle.length || sha(Buffer.from(bundle.map(p => `${p.slice(bundlePrefix.length)}\0${files.get(p).executable ? 1 : 0}\0${sha(files.get(p).data)}\n`).join(''))) !== artifact.notifier_sha256) throw new Error('Notifier bundle identity mismatch');
for (const p of ['bin/radar', bundlePrefix + 'Contents/MacOS/radar-notifier']) {
  const b = files.get(p)?.data;
  if (!b || b.length < 32 || b.readUInt32LE(0) !== 0xfeedfacf || b.readUInt32LE(4) !== (arch === 'arm64' ? 0x0100000c : 0x01000007)) throw new Error('Release binary architecture mismatch');
}
const destination = join(work, 'release'); mkdirSync(destination, { mode: 0o700 });
for (const p of dirs) mkdirSync(join(destination, p), { recursive: true, mode: 0o755 });
for (const [p, f] of files) {
  const path = join(destination, root, p);
  mkdirSync(join(path, '..'), { recursive: true, mode: 0o755 });
  writeFileSync(path, f.data, { flag: 'wx', mode: f.executable ? 0o755 : 0o644 });
}
writeFileSync(join(work, 'selected.json'), JSON.stringify({ version: selected.version, directory: join(destination, root) }), { flag: 'wx', mode: 0o600 });
console.error(`Verified Radar ${selected.version}, its publisher signature and notification companion.`);
} catch (error) {
  console.error(`Radar installer: ${error.message}`);
  process.exitCode = 1;
}
NODE
  release_dir=$(env -u NODE_OPTIONS -u NODE_PATH node -p 'JSON.parse(require("node:fs").readFileSync(process.argv[1])).directory' "$work/selected.json")
  codesign --verify --strict "$release_dir/libexec/radar/RadarNotifier.app"
  env -u PREFIX -u BINDIR -u LIBEXECDIR /bin/bash "$release_dir/install.sh"
  profile=
  case "${SHELL:-/bin/zsh}" in
    */zsh) profile="${ZDOTDIR:-$HOME}/.zshrc";;
    */bash) profile="$HOME/.bash_profile";;
  esac
  if [[ -n "$profile" ]] && ask "Add Radar commands to $profile for future terminals?"; then
    if env -u NODE_OPTIONS -u NODE_PATH node --input-type=module - "$profile" "$brew_prefix" <<'NODE'
import { constants, existsSync, lstatSync, openSync, fstatSync, readFileSync, writeSync, closeSync, realpathSync } from 'node:fs';
const [profile, brew] = process.argv.slice(2);
let destination = profile;
if (existsSync(profile)) destination = realpathSync(profile);
else if (lstatSafe(profile)) throw new Error('Cannot edit a broken shell-profile symlink');
function lstatSafe(path) { try { return lstatSync(path); } catch (e) { if (e.code === 'ENOENT') return null; throw e; } }
const fd = openSync(destination, constants.O_APPEND | constants.O_RDWR | constants.O_CREAT | constants.O_NOFOLLOW, 0o644);
try {
  const s = fstatSync(fd);
  if (!s.isFile() || s.uid !== process.getuid()) throw new Error('Shell profile is not a regular file owned by you');
  const before = readFileSync(fd, 'utf8');
  const marker = '# Radar command PATH';
  if (!before.split('\n').includes(marker)) {
    const quote = s => "'" + s.replaceAll("'", "'\\''") + "'";
    const path = brew ? `${quote(brew + '/bin:' + brew + '/sbin:')}` : '';
    const block = `\n${marker}\nexport PATH=${path}"$HOME/.local/bin:$PATH"\n`;
    writeSync(fd, Buffer.from(block));
  }
} finally { closeSync(fd); }
NODE
    then
      echo 'PATH configured. In new terminals, start with: radar' >&2
    else
      echo 'Could not configure PATH; Radar is installed. Start later with: ~/.local/bin/radar' >&2
    fi
  else
    echo 'Shell profile left unchanged. Start later with: ~/.local/bin/radar' >&2
  fi
  export PATH="$prefix/bin:$PATH"
  echo 'Starting Radar — its setup will offer the remaining tools and integrations.' >&2
  (cd "$HOME" && "$prefix/bin/radar" <&3)
}
main "$@"
