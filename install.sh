#!/usr/bin/env bash
# Trusted bootstrap source: https://github.com/ChristianMoesl/radar
# Release bytes are authenticated separately with the embedded publisher keys.
set -euo pipefail

radar_verify_release() {
  env -u NODE_OPTIONS -u NODE_PATH node --input-type=module - "$@" <<'NODE'
import { createHash, createPublicKey, verify } from 'node:crypto';
import { constants, accessSync, lstatSync, statSync, mkdirSync, mkdtempSync, writeFileSync, readFileSync, realpathSync, renameSync, rmSync, openSync, fchmodSync, fsyncSync, closeSync } from 'node:fs';
import { join, posix, resolve } from 'node:path';
import { gunzipSync } from 'node:zlib';
import { spawnSync } from 'node:child_process';

try {
// Keep these independently committed roots aligned with internal/update/keys.json.
const trustedKeys = { "release-1": "Q4QjbCIw3Jjtk0oI/E/+xiaEpwYAsejQA7agVI0VGuY=" };
const stateEpoch = 2;
const maxArchive = 128 * 1024 * 1024, maxExpanded = 512 * 1024 * 1024;
const [work, arch, macos, home, platform, existing] = process.argv.slice(2);
const artifactKey = platform === 'linux' ? `linux_${arch}` : arch;
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
  const f = m.artifacts?.[artifactKey];
  fields(f, platform === 'linux' ? ['file', 'size', 'sha256', 'binary_sha256'] : ['file', 'size', 'sha256', 'binary_sha256', 'notifier_version', 'notifier_sha256']);
  if (f.file !== `radar_${version}_${platform}_${arch}.tar.gz` || !Number.isSafeInteger(f.size) || f.size <= 0 || f.size > maxArchive || !digest(f.sha256) || !digest(f.binary_sha256)) throw new Error('Invalid release artifact');
  if (platform === 'darwin' && (!digest(f.notifier_sha256) || !stable(f.notifier_version))) throw new Error('Invalid release artifact');
  return m;
}
function ownedDirectory(path) {
  const s = lstatSync(path);
  if (!s.isDirectory() || s.uid !== process.getuid() || (s.mode & 0o022)) throw new Error('Install directories must be regular, private-to-your-user directories, not shared or symlink paths');
}
function stat(path) {
  try { return lstatSync(path); } catch (error) { if (error.code === 'ENOENT') return null; throw error; }
}
function privateFile(path) {
  const s = stat(path);
  if (s && (!s.isFile() || s.uid !== process.getuid() || (s.mode & 0o022))) throw new Error(`Refusing an unowned, shared or non-regular install file: ${path}`);
  return s;
}
function privateDirectory(path) {
  // Inspect every component before creating directories or following paths.
  // System ancestors such as /home and /tmp may belong to root; the destination
  // and any newly created directories must belong exclusively to this user.
  let current = '/';
  for (const component of path.split('/').filter(Boolean)) {
    current = join(current, component);
    let s = stat(current);
    if (!s) { mkdirSync(current, { mode: 0o755 }); s = lstatSync(current); }
    if (!s.isDirectory() || s.uid === process.getuid() && (s.mode & 0o022)) throw new Error(`Install path must use regular, non-shared directories, not symlinks: ${current}`);
  }
  ownedDirectory(path);
  try { accessSync(path, constants.W_OK | constants.X_OK); } catch { throw new Error(`Installation directory is not writable and searchable: ${path}`); }
}
function prepareLinuxInstall() {
  const pathEntries = (process.env.PATH ?? '').split(':').map(p => resolve(p || '.'));
  const localBin = join(home, '.local/bin'), homeBin = join(home, 'bin');
  let bindir;
  if (Object.hasOwn(process.env, 'RADAR_INSTALL_DIR')) {
    if (!process.env.RADAR_INSTALL_DIR) throw new Error('RADAR_INSTALL_DIR must not be empty');
    bindir = resolve(process.env.RADAR_INSTALL_DIR);
  } else bindir = pathEntries.includes(localBin) ? localBin : pathEntries.includes(homeBin) ? homeBin : localBin;
  if (/[:\x00-\x1f\x7f]/.test(bindir)) throw new Error('RADAR_INSTALL_DIR must not contain a colon or control characters');
  privateDirectory(bindir);
  const binary = join(bindir, 'radar'), receiptPath = join(bindir, '.radar-install.json');
  const lock = join(bindir, '.radar-install.lock');
  try { mkdirSync(lock, { mode: 0o700 }); } catch (error) {
    if (error.code === 'EEXIST') throw new Error(`Another installation is running, or an interrupted installer left ${lock}. Inspect it before removing the lock and retrying.`);
    throw error;
  }
  process.once('exit', () => rmSync(lock, { recursive: true, force: true }));
  process.once('SIGINT', () => process.exit(130));
  process.once('SIGTERM', () => process.exit(143));
  let receipt = null;
  if (privateFile(receiptPath)) {
    receipt = JSON.parse(readFileSync(receiptPath, 'utf8'));
    fields(receipt, ['schema', 'installer', 'version', 'state_epoch', 'binary_sha256', 'previous_binary_sha256']);
    // An epoch 1 receipt proves binary ownership too. The new daemon performs
    // the backed-up data conversion; the installer never reads application data.
    if (receipt.schema !== 1 || receipt.installer !== 'radar-linux-bootstrap' || !tag(receipt.version) || ![1, stateEpoch].includes(receipt.state_epoch) || !digest(receipt.binary_sha256) || receipt.previous_binary_sha256 !== null && !digest(receipt.previous_binary_sha256)) throw new Error('Invalid or incompatible Linux installation receipt; inspect it before retrying');
  }
  let previous = null;
  if (privateFile(binary)) {
    previous = sha(readFileSync(binary));
    if (!receipt || ![receipt.binary_sha256, receipt.previous_binary_sha256].includes(previous)) throw new Error(`Refusing to overwrite an executable not managed by this installer: ${binary}. Choose another RADAR_INSTALL_DIR.`);
  } else if (receipt?.previous_binary_sha256) throw new Error('Installer-managed binary is missing; inspect the installation before retrying');
  if (existing && resolve(existing) !== binary) {
    const manager = /\/(?:\.linuxbrew|Cellar|homebrew)\//i.test(existing) ? 'Homebrew' : /\/(?:\.cargo|cargo)\//.test(existing) ? 'Cargo' : /\/(?:node_modules|\.npm)\//.test(existing) ? 'npm' : /^\/usr\//.test(existing) ? 'the system or a package manager' : 'another installation';
    console.error(`Existing Radar at ${existing} may be managed by ${manager}; it will be left untouched.`);
  }
  const manuals = join(home, '.local/share/man'), notices = join(home, '.local/share/radar');
  for (const directory of [join(manuals, 'man1'), join(manuals, 'man5'), notices]) privateDirectory(directory);
  for (const file of [join(manuals, 'man1/radar.1'), join(manuals, 'man5/radar-config.5'), join(notices, 'LICENSE')]) privateFile(file);
  return { bindir, binary, receiptPath, receipt, previous, lock, manuals, notices, pathEntries };
}
function atomicFile(path, data, mode, staging) {
  const temporary = join(staging, 'next');
  const fd = openSync(temporary, constants.O_WRONLY | constants.O_CREAT | constants.O_EXCL, mode);
  try { writeFileSync(fd, data); fchmodSync(fd, mode); fsyncSync(fd); } finally { closeSync(fd); }
  renameSync(temporary, path);
}
function installLinux(i, m, files) {
  privateDirectory(i.bindir);
  const current = privateFile(i.binary) ? sha(readFileSync(i.binary)) : null;
  if (current !== i.previous) throw new Error('Radar changed during the download; refusing to replace it. Inspect the installation and retry.');
  // The receipt records both identities before the binary's atomic rename, so
  // rerunning after an interrupted activation recognizes either valid binary.
  const receipt = { schema: 1, installer: 'radar-linux-bootstrap', version: m.version, state_epoch: stateEpoch, binary_sha256: m.artifacts[artifactKey].binary_sha256, previous_binary_sha256: i.previous };
  // Stage each file on its destination filesystem. Never truncate an executable
  // that might be running, and never follow a destination symlink.
  const put = (path, data, mode) => {
    privateFile(path);
    const directory = join(path, '..');
    privateDirectory(directory);
    const stage = mkdtempSync(join(directory, '.radar-stage-'));
    const cleanup = () => rmSync(stage, { recursive: true, force: true });
    process.once('exit', cleanup);
    try { atomicFile(path, data, mode, stage); } finally { cleanup(); process.removeListener('exit', cleanup); }
  };
  put(join(i.manuals, 'man1/radar.1'), files.get('share/man/man1/radar.1').data, 0o644);
  put(join(i.manuals, 'man5/radar-config.5'), files.get('share/man/man5/radar-config.5').data, 0o644);
  put(join(i.notices, 'LICENSE'), files.get('LICENSE').data, 0o644);
  put(i.receiptPath, JSON.stringify(receipt) + '\n', 0o600);
  put(i.binary, files.get('bin/radar').data, 0o755);
  console.log(`Installed Radar ${m.version} at ${i.binary}`);
  if (!i.pathEntries.includes(i.bindir)) {
    const quote = s => "'" + s.replaceAll("'", "'\\''") + "'";
    const fishQuote = s => "'" + s.replaceAll('\\', '\\\\').replaceAll("'", "\\'") + "'";
    console.log(`Add ${i.bindir} to PATH:\n  Bash: add export PATH=${quote(i.bindir)}:"$PATH" to ~/.bashrc\n  Zsh: add export PATH=${quote(i.bindir)}:"$PATH" to ~/.zshrc\n  Fish: run fish_add_path -- ${fishQuote(i.bindir)} (persists for future shells)`);
  }
  // Resolve PATH again after installing: a previously absent higher-priority
  // directory can now contain our binary, so the pre-install lookup is not enough.
  for (const directory of i.pathEntries) {
    const candidate = join(directory, 'radar');
    try {
      if (statSync(candidate).isFile()) {
        accessSync(candidate, constants.X_OK);
        if (candidate !== i.binary) console.error(`Warning: ${candidate} takes precedence in PATH. Put ${i.bindir} earlier in PATH, or run ${i.binary} directly.`);
        break;
      }
    } catch (error) { if (!['ENOENT', 'ENOTDIR', 'EACCES'].includes(error.code)) throw error; }
  }
}
// Never follow a custom/symlink installation or overwrite a managed transaction.
if (resolve(home) !== home || realpathSync(home) !== home) throw new Error('HOME must be an absolute canonical path');
ownedDirectory(home);
for (const p of platform === 'darwin' ? ['.local', '.local/bin', '.local/libexec', '.local/libexec/radar', '.local/share', '.local/share/man', '.local/share/man/man1', '.local/share/man/man5'] : []) {
  try { ownedDirectory(join(home, p)); } catch (e) { if (e.code !== 'ENOENT') throw e; }
}
for (const p of platform === 'darwin' ? ['.local/share/man/man1/radar.1', '.local/share/man/man5/radar-config.5'] : []) {
  try {
    const s = lstatSync(join(home, p));
    if (!s.isFile() || s.uid !== process.getuid() || (s.mode & 0o022)) throw new Error('Manual pages must be regular, private-to-your-user files, not shared or symlink paths');
  } catch (e) { if (e.code !== 'ENOENT') throw e; }
}
let linux;
if (platform === 'linux') linux = prepareLinuxInstall();
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
    for (const a of [m.artifacts[artifactKey]]) {
      if (!Array.isArray(r.assets) || r.assets.filter(f => f.name === a.file && f.size === a.size && f.state === 'uploaded').length !== 1) throw new Error('Incomplete release upload');
    }
    const pkg = JSON.parse(await get(`https://registry.npmjs.org/${encodeURIComponent(packageName)}/${m.pi_version}`, 1024 * 1024));
    if (pkg.name !== packageName || pkg.version !== m.pi_version) throw new Error('Matching Pi package is not public');
    selected = m; break;
  } catch (error) {
    if (overall.aborted) throw error;
    console.error(`Skipping ${r.tag_name}: ${error.message}`);
  }
}
if (!selected) throw new Error('No fully published Radar release is ready yet. Please try again later; no Radar files were installed.');
if (platform === 'darwin' && compare(macos, selected.min_macos) < 0 || Number(process.versions.node.split('.')[0]) < selected.min_node) throw new Error(platform === 'darwin' ? `This release requires macOS ${selected.min_macos}+ and Node.js ${selected.min_node}+` : `This release requires Node.js ${selected.min_node}+`);
if (linux?.receipt && compare(linux.receipt.version, selected.version) > 0) throw new Error('Refusing to downgrade the installer-managed Radar installation');
const artifact = selected.artifacts[artifactKey];
const archive = await get(`${downloads}/${selected.version}/${artifact.file}`, artifact.size);
if (archive.length !== artifact.size || sha(archive) !== artifact.sha256) throw new Error('Release archive size/hash verification failed');
// Parse the ustar format produced by make dist. Preflight every entry;
// no native tar process sees unaudited paths, links, devices or metadata records.
const tar = gunzipSync(archive, { maxOutputLength: maxExpanded });
const root = artifact.file.slice(0, -7), files = new Map(), dirs = [], seen = new Set();
const decoder = new TextDecoder('utf-8', { fatal: true });
const text = b => decoder.decode(b.subarray(0, b.indexOf(0) < 0 ? b.length : b.indexOf(0)));
const octal = b => { const s = text(b).trim(); if (!/^[0-7]+$/.test(s)) throw new Error('Unsupported tar numeric encoding'); return parseInt(s, 8); };
const required = ['bin/radar', 'README.md', 'LICENSE', 'install.sh', 'install-agent-instructions.sh', 'install-notifier.sh', 'share/radar/AGENTS.md', 'share/man/man1/radar.1', 'share/man/man5/radar-config.5'];
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
    if (!required.includes(rel) && !(platform === 'darwin' && rel.startsWith('libexec/radar/RadarNotifier.app/Contents/'))) throw new Error('Unexpected release file');
    files.set(rel, { data: tar.subarray(offset, offset + size), executable: (mode & 0o111) !== 0 });
  }
  offset += Math.ceil(size / 512) * 512;
}
if (!ended || required.some(p => !files.has(p)) || !files.get('bin/radar').executable || !files.get('install.sh').executable) throw new Error('Incomplete release archive');
// Bootstrap sources the authenticated installer's functions, never an older
// imperative installer that would overwrite Radar during prerequisite repair.
const installer = decoder.decode(files.get('install.sh').data);
if (!installer.includes('\nradar_install_archive() {\n') || !installer.includes('\nradar_install_prerequisites() {\n')) throw new Error('This release predates prerequisite-aware installation. Please try again after a new release is published.');
if (sha(files.get('bin/radar').data) !== artifact.binary_sha256) throw new Error('Radar binary identity mismatch');
if (platform === 'linux') {
  const b = files.get('bin/radar').data;
  if (b.length < 64 || b.subarray(0, 4).toString('hex') !== '7f454c46' || b[4] !== 2 || b[5] !== 1 || b.readUInt16LE(18) !== (arch === 'amd64' ? 62 : 183)) throw new Error('Release binary architecture mismatch');
} else {
const bundlePrefix = 'libexec/radar/RadarNotifier.app/';
const bundle = [...files.keys()].filter(p => p.startsWith(bundlePrefix)).sort((a, b) => Buffer.compare(Buffer.from(a), Buffer.from(b)));
if (!bundle.length || sha(Buffer.from(bundle.map(p => `${p.slice(bundlePrefix.length)}\0${files.get(p).executable ? 1 : 0}\0${sha(files.get(p).data)}\n`).join(''))) !== artifact.notifier_sha256) throw new Error('Notifier bundle identity mismatch');
for (const p of ['bin/radar', bundlePrefix + 'Contents/MacOS/radar-notifier']) {
  const b = files.get(p)?.data;
  if (!b || b.length < 32 || b.readUInt32LE(0) !== 0xfeedfacf || b.readUInt32LE(4) !== 0x0100000c) throw new Error('Release binary architecture mismatch');
}
}
const destination = join(work, 'release'); mkdirSync(destination, { mode: 0o700 });
for (const p of dirs) mkdirSync(join(destination, p), { recursive: true, mode: 0o755 });
for (const [p, f] of files) {
  const path = join(destination, root, p);
  mkdirSync(join(path, '..'), { recursive: true, mode: 0o755 });
  writeFileSync(path, f.data, { flag: 'wx', mode: f.executable ? 0o755 : 0o644 });
}
const directory = join(destination, root);
if (platform === 'linux') {
  console.error(`Verified Radar ${selected.version}, its publisher signature and checksums.`);
  // Run only the prerequisite helper from the authenticated archive. Keep the
  // installation lock and original binary identity until provisioning finishes.
  const prerequisites = spawnSync('bash', ['-c', 'source "$1"; radar_install_prerequisites || exit; printf "%s" "$PATH" > "$2"', 'radar-installer', join(directory, 'install.sh'), join(work, 'prerequisites-path')], { stdio: 'inherit' });
  if (prerequisites.error || prerequisites.status !== 0) throw new Error('Required tools could not be prepared. No Radar files were installed.');
  installLinux(linux, selected, files);
} else {
  console.error(`Verified Radar ${selected.version}, its publisher signature and notification companion.`);
}
writeFileSync(join(work, 'selected.json'), JSON.stringify({ version: selected.version, directory, binary: linux?.binary }), { flag: 'wx', mode: 0o600 });
} catch (error) {
  console.error(`Radar installer: ${error.message}`);
  process.exitCode = 1;
}
NODE
}

main() {
  [[ $# = 0 ]] || { echo 'usage: bash install.sh' >&2; return 2; }
  [[ $(id -u) != 0 ]] || { echo 'Run this installer as your normal user, without sudo.' >&2; return 1; }
  [[ -n ${HOME:-} ]] || { echo 'HOME must be set to your user directory.' >&2; return 1; }
  local platform arch native_brew macos= existing
  case "$(uname -s)" in
    Linux)
      platform=linux
      native_brew=/home/linuxbrew/.linuxbrew/bin/brew
      case "$(uname -m)" in
        x86_64|amd64) arch=amd64;;
        aarch64|arm64) arch=arm64;;
        *) echo 'Unsupported Linux architecture: Radar requires amd64 or arm64.' >&2; return 1;;
      esac;;
    Darwin)
      platform=darwin
      native_brew=/opt/homebrew/bin/brew
      macos=$(sw_vers -productVersion)
      case "$(uname -m)" in
        arm64) arch=arm64;;
        *) echo 'Unsupported macOS architecture: Radar requires Apple Silicon (arm64).' >&2; return 1;;
      esac;;
    *) echo 'This installer supports Linux and Apple Silicon macOS. On Windows, run it inside WSL.' >&2; return 1;;
  esac
  existing=$(command -v radar || true)
  prefix="$HOME/.local"
  repair=false
  if [[ $platform = darwin ]] && { [[ -n $existing ]] || [[ -e "$prefix/bin/radar" || -L "$prefix/bin/radar" ]]; }; then
    repair=true
    echo 'Radar is already installed. Checking prerequisites only; Radar and its configuration will not be replaced.' >&2
  fi
  if [[ $platform = darwin && $repair = false ]] && [[ -e "$prefix/libexec/radar/install.json" || -L "$prefix/libexec/radar/install.json" || -e "$prefix/libexec/radar/.upgrade" || -L "$prefix/libexec/radar/.upgrade" || -e "$prefix/libexec/radar/RadarNotifier.app" || -L "$prefix/libexec/radar/RadarNotifier.app" ]]; then
    echo 'Existing Radar update state needs inspection; this fresh-install bootstrap will not replace it.' >&2
    return 1
  fi
  # A pipe supplies the script, never approval. Headless execution cannot silently
  # install prerequisites or edit profiles; all prompts read the controlling TTY.
  interactive=false
  terminal_state=
  terminal_stty=
  if { exec 3</dev/tty; } 2>/dev/null; then
    interactive=true
    # Prefer the explicitly named GNU utility where the distribution provides it.
    # Keep its path pinned even when prerequisite installation changes PATH.
    if ! terminal_stty=$(command -v gnustty || command -v stty) ||
       ! terminal_state=$("$terminal_stty" -g <&3) || [[ -z "$terminal_state" ]] ||
       ! "$terminal_stty" "$terminal_state" <&3; then
      echo 'Cannot save and restore terminal modes; install a working GNU/BSD stty (GNU may be named gnustty).' >&2
      return 1
    fi
  fi
  work=$(mktemp -d)
  cleanup() {
    local status=$?
    if [[ $interactive = true ]] && ! "$terminal_stty" "$terminal_state" <&3; then
      echo 'Failed to restore terminal modes.' >&2
      if [[ $status = 0 ]]; then status=1; fi
    fi
    rm -rf "$work"
    return "$status"
  }
  trap cleanup EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM
  ask() {
    local answer=
    [[ $interactive = true ]] || { echo 'Missing prerequisites need approval in an interactive terminal. Install the tools on PATH, then rerun.' >&2; return 1; }
    printf '%s [%s] ' "$1" "${2:-Y/n}" >&2
    read -r answer <&3 || return 1
    [[ "$answer" = y || "$answer" = Y || ( -z "$answer" && "${2:-Y/n}" = Y/n ) ]]
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
  # Activate an existing Homebrew for both the archive installer and Radar.
  # Offer it for missing tools, even if Node is already ready.
  if ! node_ready || ! command -v npm >/dev/null 2>&1 || ! command -v git >/dev/null 2>&1 || ! command -v tmux >/dev/null 2>&1 || ! command -v fd >/dev/null 2>&1 || ! command -v gh >/dev/null 2>&1 || command -v brew >/dev/null 2>&1 || [[ -x "$native_brew" ]]; then
    if command -v brew >/dev/null 2>&1; then
      brew=$(command -v brew)
    elif [[ -x "$native_brew" ]]; then
      brew=$native_brew
    elif [[ $platform = darwin ]]; then
      echo 'Homebrew can supply the missing workspace tools and Node.js 24+ for publisher verification and Pi.' >&2
      echo 'The official Homebrew installer may request normal macOS administrator approval.' >&2
      ask 'Install Homebrew from https://brew.sh?' || { echo 'Homebrew declined. No Radar files were installed.' >&2; return 1; }
      download https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh "$work/homebrew.sh"
      /bin/bash "$work/homebrew.sh" <&3
      [[ -x "$native_brew" ]] || { echo 'Homebrew is still unavailable; fix its installation before retrying.' >&2; return 1; }
      brew=$native_brew
    fi
    if [[ -n $brew ]]; then
      brew_prefix=$("$brew" --prefix)
      export PATH="$brew_prefix/bin:$brew_prefix/sbin:$PATH"
    fi
    if ! node_ready; then
      local -a node_command
      if [[ -n $brew ]]; then
        node_command=("$brew" install node)
      elif command -v apt-get >/dev/null 2>&1; then
        node_command=(sudo apt-get install -y nodejs npm)
      elif command -v dnf >/dev/null 2>&1; then
        node_command=(sudo dnf install -y nodejs npm)
      elif command -v pacman >/dev/null 2>&1; then
        node_command=(sudo pacman -S --needed --noconfirm nodejs npm)
      else
        echo 'Node.js 24+ is required for publisher verification and Pi. Install it on PATH, then rerun.' >&2
        return 1
      fi
      printf 'Node.js 24+ is required. Installation command:' >&2
      printf ' %q' "${node_command[@]}" >&2
      printf '\n' >&2
      ask 'Install/update Node.js 24+ now?' || { echo 'Node.js declined. No Radar files were installed.' >&2; return 1; }
      if ! env -u NODE_OPTIONS -u NODE_PATH "${node_command[@]}" <&3; then
        echo 'Installing Node.js failed. Fix the command above, then rerun this installer.' >&2
        return 1
      fi
      node_ready || { echo 'Node.js 24+ is still unavailable. No Radar files were installed.' >&2; return 1; }
    fi
  fi
  echo 'Finding an authenticated, fully published Radar release…' >&2
  # Do not let project/user Node preload hooks alter this verifier. There are no
  # environment overrides for publisher trust, release endpoints or selection.
  radar_verify_release "$work" "$arch" "$macos" "$HOME" "$platform" "$existing"
  release_dir=$(env -u NODE_OPTIONS -u NODE_PATH node -p 'JSON.parse(require("node:fs").readFileSync(process.argv[1])).directory' "$work/selected.json")
  binary="$prefix/bin/radar"
  if [[ $platform = darwin ]]; then
    codesign --verify --strict "$release_dir/libexec/radar/RadarNotifier.app"
    source "$release_dir/install.sh"
    radar_install_prerequisites
  else
    binary=$(env -u NODE_OPTIONS -u NODE_PATH node -p 'JSON.parse(require("node:fs").readFileSync(process.argv[1])).binary' "$work/selected.json")
    export PATH="$(cat "$work/prerequisites-path")"
  fi
  if [[ $repair = true ]]; then
    echo 'Required tools are ready. Use radar update to update Radar itself.' >&2
    binary=${existing:-"$prefix/bin/radar"}
  fi
  if [[ $platform = darwin && $repair = false ]]; then
    (unset PREFIX BINDIR LIBEXECDIR MANDIR; radar_install_archive "$release_dir")
  fi
  bindir=$(dirname "$binary")
  profile=
  case "${SHELL:-/bin/zsh}" in
    */zsh) profile="${ZDOTDIR:-$HOME}/.zshrc";;
    */bash) if [[ $platform = darwin ]]; then profile="$HOME/.bash_profile"; else profile="$HOME/.bashrc"; fi;;
    */fish) profile="${XDG_CONFIG_HOME:-$HOME/.config}/fish/config.fish";;
  esac
  if [[ $interactive = true && -n "$profile" ]] && ask "Add Radar commands to $profile for future terminals?" y/N; then
    if env -u NODE_OPTIONS -u NODE_PATH node --input-type=module - "$profile" "$brew_prefix" "$bindir" "${SHELL:-/bin/zsh}" <<'NODE'
import { constants, existsSync, lstatSync, openSync, fstatSync, readFileSync, writeSync, closeSync, realpathSync, mkdirSync } from 'node:fs';
import { dirname } from 'node:path';
const [profile, brew, bindir, shell] = process.argv.slice(2);
let destination = profile;
if (existsSync(profile)) destination = realpathSync(profile);
else if (lstatSafe(profile)) throw new Error('Cannot edit a broken shell-profile symlink');
mkdirSync(dirname(destination), { recursive: true });
function lstatSafe(path) { try { return lstatSync(path); } catch (e) { if (e.code === 'ENOENT') return null; throw e; } }
const fd = openSync(destination, constants.O_APPEND | constants.O_RDWR | constants.O_CREAT | constants.O_NOFOLLOW, 0o644);
try {
  const s = fstatSync(fd);
  if (!s.isFile() || s.uid !== process.getuid()) throw new Error('Shell profile is not a regular file owned by you');
  const before = readFileSync(fd, 'utf8');
  const marker = '# Radar command PATH';
  if (!before.split('\n').includes(marker)) {
    const quote = s => "'" + s.replaceAll("'", "'\\''") + "'";
    const paths = [...new Set([bindir, process.env.HOME + '/.local/bin', ...(brew ? [brew + '/bin', brew + '/sbin'] : [])])];
    const fishQuote = s => "'" + s.replaceAll('\\', '\\\\').replaceAll("'", "\\'") + "'";
    const command = shell.endsWith('/fish') ? `fish_add_path -- ${paths.map(fishQuote).join(' ')}` : `export PATH=${quote(paths.join(':'))}:"$PATH"`;
    const block = `\n${marker}\n${command}\n`;
    writeSync(fd, Buffer.from(block));
  }
} finally { closeSync(fd); }
NODE
    then
      echo 'PATH configured. In new terminals, start with: radar' >&2
    else
      printf 'Could not configure PATH; Radar is installed. Start later with: %s\n' "$binary" >&2
    fi
  else
    printf 'Shell profile left unchanged. Start later with: %s\n' "$binary" >&2
  fi
  export PATH="$bindir:$PATH"
  [[ $interactive = true ]] || { echo 'Run radar in a terminal to complete guided setup.' >&2; return; }
  echo 'Starting Radar — its setup will configure Pi extensions, directories and integrations.' >&2
  (cd "$HOME" && "$binary" <&3)
}
main "$@"
