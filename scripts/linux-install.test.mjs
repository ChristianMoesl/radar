import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createHash, generateKeyPairSync, sign } from 'node:crypto';
import { chmodSync, existsSync, lstatSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, realpathSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import { join, dirname } from 'node:path';
import { tmpdir } from 'node:os';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';
import { gzipSync } from 'node:zlib';

const root = fileURLToPath(new URL('../', import.meta.url));
const source = readFileSync(join(root, 'install.sh'), 'utf8');
const sha = bytes => createHash('sha256').update(bytes).digest('hex');
const repository = 'https://api.github.com/repos/ChristianMoesl/radar';
const downloads = 'https://github.com/ChristianMoesl/radar/releases/download';

// Use actual publisher signatures, archive bytes, permissions and installer
// processes. Only trust roots and network/tool boundaries are fixture-local;
// production offers no key or endpoint override.
function tar(entries) {
  const blocks = [];
  for (const { name, data = Buffer.alloc(0), mode = 0o644, type = '0', link = '' } of entries) {
    const b = Buffer.from(data), header = Buffer.alloc(512);
    header.write(name, 0, 100);
    const octal = (value, offset, length) => header.write(value.toString(8).padStart(length - 1, '0') + '\0', offset, length);
    octal(mode, 100, 8); octal(0, 108, 8); octal(0, 116, 8);
    octal(b.length, 124, 12); octal(0, 136, 12);
    header.fill(32, 148, 156); header.write(type, 156, 1);
    header.write(link, 157, 100); header.write('ustar\0', 257, 6); header.write('00', 263, 2);
    const checksum = header.reduce((sum, byte) => sum + byte, 0);
    header.write(checksum.toString(8).padStart(6, '0') + '\0 ', 148, 8);
    blocks.push(header, b, Buffer.alloc((512 - b.length % 512) % 512));
  }
  return gzipSync(Buffer.concat([...blocks, Buffer.alloc(1024)]));
}

function executable(arch, version, platform) {
  const bytes = Buffer.alloc(96);
  if (platform === 'darwin') {
    bytes.writeUInt32LE(0xfeedfacf); bytes.writeUInt32LE(0x0100000c, 4);
  } else {
    bytes.write('7f454c46', 0, 'hex'); bytes[4] = 2; bytes[5] = 1;
    bytes.writeUInt16LE(arch === 'amd64' ? 62 : 183, 18);
  }
  bytes.write(version, 64);
  return bytes;
}

function fixture(t, { platform = 'linux', arch = 'amd64', mutation = '', homeName = 'user home' } = {}) {
  const temp = realpathSync(mkdtempSync(join(tmpdir(), 'radar-installer-')));
  t.after(() => rmSync(temp, { recursive: true, force: true }));
  const home = join(temp, homeName), tools = join(home, 'tools'), work = join(home, 'work');
  for (const dir of [home, tools, work]) mkdirSync(dir, { recursive: true, mode: 0o700 });
  const put = (path, data, mode = 0o644) => {
    mkdirSync(dirname(path), { recursive: true, mode: 0o755 });
    writeFileSync(path, data, { mode });
  };
  const shell = (name, body) => put(join(tools, name), '#!/bin/sh\n' + body + '\n', 0o755);
  for (const file of ['.bashrc', '.bash_profile', '.zshrc', '.config/fish/config.fish', '.config/radar/config.yaml']) put(join(home, file), '# preserve user content\n', 0o600);
  const { publicKey, privateKey } = generateKeyPairSync('ed25519');
  const roots = JSON.stringify({ fixture: publicKey.export({ type: 'spki', format: 'der' }).subarray(-32).toString('base64') });
  let script = source.replace(/const trustedKeys = \{[^\n]+\};/, `const trustedKeys = ${roots};`);
  put(join(home, 'install.sh'), script, 0o755);
  put(join(home, 'fetch.mjs'), `
import fs from 'node:fs';
import { syncBuiltinESMExports } from 'node:module';
const home = process.env.HOME;
const rename = fs.renameSync;
fs.renameSync = (from, to) => {
  if (to.endsWith('/radar') && fs.existsSync(home + '/fail-rename')) throw new Error('fixture atomic rename failure');
  return rename(from, to);
};
syncBuiltinESMExports();
globalThis.fetch = async (address) => {
  const url = String(address);
  fs.appendFileSync(home + '/requests', url + '\\n');
  const routes = JSON.parse(fs.readFileSync(home + '/routes.json', 'utf8'));
  const r = routes[url];
  if (!r) return new Response('missing', { status: 404 });
  if (url.endsWith('.tar.gz') && fs.existsSync(home + '/replace-during-download')) {
    fs.writeFileSync(home + '/.local/bin/radar', 'unrelated replacement');
  }
  if (r.redirect) return new Response(null, { status: 302, headers: { location: r.redirect } });
  const bytes = Buffer.from(r.data, 'base64');
  return new Response(bytes, { status: 200, headers: { 'content-length': String(bytes.length) } });
};
`);
  shell('node', 'exec "$FIXTURE_NODE" --import "$HOME/fetch.mjs" "$@"');
  shell('uname', `if [ "$1" = -s ]; then echo ${platform === 'darwin' ? 'Darwin' : 'Linux'}; else echo ${arch === 'amd64' ? 'x86_64' : 'aarch64'}; fi`);
  for (const manager of ['sudo', 'brew', 'cargo', 'npm', 'apt-get', 'dnf', 'pacman']) shell(manager, 'echo "unexpected package manager" >> "$HOME/manager-calls"; exit 99');
  shell('brew', 'if [ "$1" = --prefix ]; then printf "%s" "$HOME/brew"; else echo "unexpected package manager" >> "$HOME/manager-calls"; exit 99; fi');
  for (const [tool, version] of Object.entries({ git: 'git 2.50', tmux: 'tmux 3.6', fd: 'fd 10.0', npm: '11.0', pi: '0.85.1', gh: 'gh 2.70' })) shell(tool, `echo '${version}'`);
  const installer = readFileSync(join(root, 'scripts/install-prerequisites.sh'), 'utf8') + '\n' + readFileSync(join(root, 'scripts/install.sh'), 'utf8').replace(/^source .*\n/m, '');
  const routes = {};
  const releases = [];
  const publish = (version = 'v1.2.3') => {
    const artifacts = {}, assets = [];
    const architectures = platform === 'darwin' ? ['arm64'] : ['amd64', 'arm64'];
    for (const cpu of architectures) {
      const key = platform === 'darwin' ? cpu : `linux_${cpu}`;
      const file = `radar_${version}_${platform}_${cpu}.tar.gz`, directory = file.slice(0, -7);
      const binary = executable(mutation === 'wrong CPU' ? (cpu === 'amd64' ? 'arm64' : 'amd64') : cpu, version, platform);
      const files = [
        { name: 'bin/radar', data: binary, mode: mutation === 'non-executable binary' ? 0o644 : 0o755 },
        { name: 'README.md', data: 'fixture documentation' }, { name: 'LICENSE', data: 'fixture MIT notice' },
        { name: 'install.sh', data: installer, mode: 0o755 },
        { name: 'install-agent-instructions.sh', data: '#!/bin/sh\nexit 99\n', mode: 0o755 },
        { name: 'install-notifier.sh', data: '#!/bin/sh\nexit 99\n', mode: 0o755 },
        { name: 'share/radar/AGENTS.md', data: 'fixture instructions' },
        { name: 'share/man/man1/radar.1', data: 'fixture manual' },
        { name: 'share/man/man5/radar-config.5', data: 'fixture configuration manual' },
      ];
      if (platform === 'darwin') files.push({ name: 'libexec/radar/RadarNotifier.app/Contents/MacOS/radar-notifier', data: binary, mode: 0o755 });
      if (mutation === 'missing manual') files.pop();
      const entries = files.map(f => ({ ...f, name: `${directory}/${f.name}` }));
      if (mutation === 'traversal') entries.push({ name: `${directory}/../outside`, data: 'unsafe' });
      if (mutation === 'symlink entry') entries.push({ name: `${directory}/link`, type: '2', link: '/tmp/outside' });
      if (mutation === 'duplicate entry') entries.push(entries[0]);
      if (mutation === 'notifier on Linux') entries.push({ name: `${directory}/libexec/radar/RadarNotifier.app/Contents/file`, data: 'unsafe' });
      let archive = tar(entries);
      if (mutation === 'truncated tar') archive = gzipSync(Buffer.alloc(512, 65));
      artifacts[key] = { file, size: archive.length, sha256: sha(archive), binary_sha256: mutation === 'binary checksum' ? 'a'.repeat(64) : sha(binary) };
      if (platform === 'darwin') {
        artifacts[key].notifier_version = '1.0.0';
        artifacts[key].notifier_sha256 = sha(`Contents/MacOS/radar-notifier\0${1}\0${sha(binary)}\n`);
      }
      if (mutation === 'notifier metadata on Linux') artifacts[key].notifier_sha256 = 'a'.repeat(64);
      if (mutation === 'archive checksum') { archive = Buffer.from(archive); archive[archive.length - 1] ^= 1; }
      routes[`${downloads}/${version}/${file}`] = { data: archive.toString('base64') };
      assets.push({ name: file, size: artifacts[key].size, state: 'uploaded' });
    }
    const manifest = { schema: 1, version, state_epoch: mutation === 'incompatible epoch' ? 1 : 2, min_macos: '13.0', pi_version: version.slice(1), min_pi: '0.85.1', min_node: mutation === 'newer Node required' ? 100 : 24, artifacts };
    if (mutation === 'wrong filename') manifest.artifacts[platform === 'darwin' ? arch : `linux_${arch}`].file = '../wrong.tar.gz';
    const bytes = Buffer.from(JSON.stringify(manifest));
    const signature = sign(null, bytes, privateKey);
    if (mutation === 'bad signature') signature[0] ^= 1;
    routes[`${downloads}/${version}/release.json`] = { data: bytes.toString('base64') };
    if (mutation !== 'unsigned') routes[`${downloads}/${version}/release.json.sig`] = { data: Buffer.from(JSON.stringify({ key_id: mutation === 'unknown publisher' ? 'unknown' : 'fixture', signature: signature.toString('base64') })).toString('base64') };
    if (mutation === 'HTTP redirect') routes[`${downloads}/${version}/release.json`].redirect = 'http://untrusted.example/release.json';
    if (mutation !== 'npm pending') routes[`https://registry.npmjs.org/%40christianmoesl%2Fpi-radar/${manifest.pi_version}`] = { data: Buffer.from(JSON.stringify({ name: '@christianmoesl/pi-radar', version: manifest.pi_version })).toString('base64') };
    releases.unshift({ tag_name: version, draft: false, prerelease: false, assets: mutation === 'missing asset' ? [] : mutation === 'duplicate asset' ? [...assets, ...assets] : assets });
    routes[`${repository}/releases?per_page=100&page=1`] = { data: Buffer.from(JSON.stringify(releases)).toString('base64') };
    put(join(home, 'routes.json'), JSON.stringify(routes));
  };
  publish();
  const run = ({ path = [], installDir, args = [], stdin, extra = {}, umask } = {}) => {
    const env = { HOME: home, PATH: [...path, tools, '/usr/bin', '/bin'].join(':'), SHELL: '/bin/bash', FIXTURE_NODE: process.execPath, NODE_OPTIONS: '--import /must-not-run', ...extra };
    if (installDir !== undefined) env.RADAR_INSTALL_DIR = installDir;
    const command = platform === 'darwin'
      ? [ '-c', 'source "$HOME/verifier.sh"; radar_verify_release "$HOME/work" arm64 26.0 "$HOME" darwin' ]
      : umask ? ['-c', `umask ${umask}; exec bash "$HOME/install.sh"`] : stdin ? [] : [join(home, 'install.sh'), ...args];
    if (platform === 'darwin') put(join(home, 'verifier.sh'), script.replace(/\nmain "\$@"\n?$/, '\n'));
    const result = spawnSync('/bin/bash', command, { cwd: home, env, input: stdin, encoding: 'utf8', timeout: 15000 });
    assert.ifError(result.error);
    return { ...result, output: result.stdout + result.stderr };
  };
  return { home, tools, work, run, put, shell, publish, routes, script };
}

function installed(f, directory = join(f.home, '.local/bin')) {
  const binary = join(directory, 'radar');
  assert.equal(lstatSync(binary).mode & 0o777, 0o755);
  assert.equal(readFileSync(binary).subarray(0, 4).toString('hex'), '7f454c46');
  const receipt = JSON.parse(readFileSync(join(directory, '.radar-install.json')));
  assert.equal(receipt.binary_sha256, sha(readFileSync(binary)));
  assert.equal(lstatSync(join(directory, '.radar-install.json')).mode & 0o777, 0o600);
  assert.equal(existsSync(join(directory, '.radar-install.lock')), false);
  assert.equal(readdirSync(directory).some(p => p.startsWith('.radar-stage-')), false);
  assert.equal(existsSync(join(f.home, 'manager-calls')), false);
  for (const file of ['.bashrc', '.bash_profile', '.zshrc', '.config/fish/config.fish', '.config/radar/config.yaml']) assert.equal(readFileSync(join(f.home, file), 'utf8'), '# preserve user content\n');
  return receipt;
}

test('Linux installation runs as a real unprivileged user', () => {
  assert.notEqual(process.getuid(), 0, 'Run this suite as an ordinary user; CI explicitly drops root.');
});

test('embedded publisher trust still matches the updater', () => {
  const keys = JSON.parse(source.match(/const trustedKeys = (\{[^\n]+\});/)[1]);
  assert.deepEqual(keys, JSON.parse(readFileSync(join(root, 'internal/update/keys.json'))));
  const epoch = Number(readFileSync(join(root, 'internal/update/manifest.go'), 'utf8').match(/^const StateEpoch = (\d+)/m)[1]);
  assert.match(source, new RegExp(`const stateEpoch = ${epoch};`));
});

for (const scenario of ['neither on PATH', 'both on PATH', 'home bin on PATH', 'local bin missing but on PATH', 'home bin missing but on PATH', 'trailing slash', 'substring is not a PATH entry', 'empty PATH entry', 'explicit nested directory', 'explicit relative directory', 'explicit directory overrides both']) {
  test(`directory selection: ${scenario}`, t => {
    const f = fixture(t), local = join(f.home, '.local/bin'), homeBin = join(f.home, 'bin');
    let expected = local, path = [], installDir;
    if (['both on PATH', 'home bin on PATH', 'trailing slash'].includes(scenario)) mkdirSync(homeBin, { mode: 0o755 });
    if (scenario === 'both on PATH') { mkdirSync(local, { recursive: true, mode: 0o755 }); path = [homeBin, local]; }
    if (scenario === 'home bin on PATH' || scenario === 'home bin missing but on PATH') { path = [homeBin]; expected = homeBin; }
    if (scenario === 'local bin missing but on PATH') path = [local];
    if (scenario === 'trailing slash') { path = [homeBin + '/']; expected = homeBin; }
    if (scenario === 'substring is not a PATH entry') path = [local + '-other'];
    if (scenario === 'empty PATH entry') path = [''];
    if (scenario.startsWith('explicit')) {
      installDir = scenario === 'explicit relative directory' ? 'custom/bin' : join(f.home, 'custom/bin');
      expected = join(f.home, 'custom/bin');
      if (scenario === 'explicit directory overrides both') path = [local, homeBin];
    }
    const result = f.run({ path, installDir });
    assert.equal(result.status, 0, result.output); installed(f, expected);
    assert.equal(result.output.includes('Add ' + expected + ' to PATH:'), !path.some(p => p.replace(/\/$/, '') === expected));
    assert.equal(readFileSync(join(f.home, '.local/share/man/man1/radar.1'), 'utf8'), 'fixture manual');
    assert.equal(readFileSync(join(f.home, '.local/share/radar/LICENSE'), 'utf8'), 'fixture MIT notice');
  });
}

for (const manager of ['.linuxbrew/bin', '.cargo/bin', '.npm/bin', 'node_modules/.bin']) {
  test(`never selects or overwrites ${manager} automatically`, t => {
    const f = fixture(t), directory = join(f.home, manager);
    const data = '#!/bin/sh\necho do-not-run >> "$HOME/unrelated-ran"\n';
    f.put(join(directory, 'radar'), data, 0o755);
    const result = f.run({ path: [directory] });
    assert.equal(result.status, 0, result.output); installed(f);
    assert.equal(readFileSync(join(directory, 'radar'), 'utf8'), data);
    assert.equal(existsSync(join(f.home, 'unrelated-ran')), false);
    assert.match(result.output, /Existing Radar.*left untouched/);
    assert.match(result.output, /takes precedence in PATH/);
  });
}

test('a newly installed earlier PATH entry supersedes the old executable', t => {
  const f = fixture(t), local = join(f.home, '.local/bin'), other = join(f.home, '.cargo/bin');
  f.put(join(other, 'radar'), '#!/bin/sh\nexit 99\n', 0o755);
  const result = f.run({ path: [local, other] });
  assert.equal(result.status, 0, result.output); installed(f);
  assert.doesNotMatch(result.output, /takes precedence/);
});

for (const cpu of ['amd64', 'arm64']) {
  test(`selects and verifies the Linux ${cpu} artifact`, t => {
    const f = fixture(t, { arch: cpu }), result = f.run();
    assert.equal(result.status, 0, result.output); installed(f);
    const requests = readFileSync(join(f.home, 'requests'), 'utf8');
    assert.match(requests, new RegExp(`linux_${cpu}\\.tar\\.gz`));
    assert.doesNotMatch(requests, /darwin_.*\.tar\.gz/);
  });
}

test('headless piped installation never consumes consent or launches Radar', t => {
  const f = fixture(t), result = f.run({ stdin: f.script });
  assert.equal(result.status, 0, result.output); installed(f);
  assert.doesNotMatch(result.output, /Starting Radar/);
  assert.match(result.output, /Run radar in a terminal/);
});

for (const tool of ['node', 'gh']) {
  test(`headless installation refuses missing ${tool} without consuming piped approval`, t => {
    const f = fixture(t);
    f.shell(tool, 'exit 1');
    const result = f.run({ stdin: f.script + '\ny\ny\n' });
    assert.notEqual(result.status, 0, result.output);
    assert.match(result.output, /interactive terminal/);
    assert.equal(existsSync(join(f.home, 'manager-calls')), false);
    assert.equal(existsSync(join(f.home, '.local/bin/radar')), false);
    assert.equal(existsSync(join(f.home, '.local/bin/.radar-install.lock')), false);
  });
}

test('Bash, Zsh and Fish instructions quote a custom directory safely', t => {
  const f = fixture(t), directory = join(f.home, "it's $(touch injected) \\ bin");
  const result = f.run({ installDir: directory });
  assert.equal(result.status, 0, result.output); installed(f, directory);
  assert.match(result.output, /Bash:.*~\/.bashrc/); assert.match(result.output, /Zsh:.*~\/.zshrc/);
  assert.match(result.output, /Fish: run fish_add_path -- /);
  const instruction = result.output.match(/Bash: add (export PATH=.+) to ~\/.bashrc/)[1];
  const shell = spawnSync('/bin/bash', ['-c', instruction + '; printf "%s" "$PATH"'], { cwd: f.home, env: { HOME: f.home, PATH: '/usr/bin:/bin' }, encoding: 'utf8' });
  assert.equal(shell.status, 0, shell.stderr); assert.equal(shell.stdout, directory + ':/usr/bin:/bin');
  assert.equal(existsSync(join(f.home, 'injected')), false);
});

for (const shell of ['zsh', 'fish']) {
  test(`${shell} accepts the printed PATH instruction`, t => {
    const available = spawnSync(shell, ['--version']);
    if (available.error?.code === 'ENOENT') { t.skip(`${shell} is installed in the distribution CI matrix`); return; }
    assert.ifError(available.error);
    const f = fixture(t), directory = join(f.home, "it's $(touch injected) \\ bin");
    const result = f.run({ installDir: directory });
    assert.equal(result.status, 0, result.output);
    const instruction = shell === 'zsh'
      ? result.output.match(/Zsh: add (export PATH=.+) to ~\/.zshrc/)[1]
      : result.output.match(/Fish: run (fish_add_path -- .+) \(persists for future shells\)/)[1];
    const command = instruction + (shell === 'zsh' ? '; printf "%s" "$PATH"' : '; printf "%s" $PATH[1]');
    const check = spawnSync(shell, [...(shell === 'zsh' ? ['-f'] : []), '-c', command], { cwd: f.home, env: { HOME: f.home, PATH: '/usr/local/bin:/usr/bin:/bin' }, encoding: 'utf8' });
    assert.ifError(check.error);
    assert.equal(check.status, 0, check.stderr);
    assert.equal(check.stdout, shell === 'zsh' ? directory + ':/usr/local/bin:/usr/bin:/bin' : directory);
    assert.equal(existsSync(join(f.home, 'injected')), false);
  });
}

for (const scenario of ['read only', 'not searchable', 'empty override', 'colon override', 'control character override', 'symlink directory', 'symlink ancestor', 'shared directory', 'unrelated executable', 'symlink executable', 'directory executable', 'symlink receipt', 'invalid receipt', 'existing lock']) {
  test(`rejects unsafe destination: ${scenario}`, t => {
    const f = fixture(t), directory = join(f.home, 'destination');
    let installDir = directory;
    mkdirSync(directory, { mode: 0o755 });
    if (scenario === 'read only') chmodSync(directory, 0o555);
    if (scenario === 'not searchable') chmodSync(directory, 0o600);
    if (scenario === 'empty override') installDir = '';
    if (scenario === 'colon override') installDir += ':bad';
    if (scenario === 'control character override') installDir += '\n';
    if (scenario === 'symlink directory' || scenario === 'symlink ancestor') { installDir = join(f.home, 'linked') + (scenario === 'symlink ancestor' ? '/bin' : ''); symlinkSync(directory, join(f.home, 'linked')); }
    if (scenario === 'shared directory') chmodSync(directory, 0o777);
    if (scenario === 'unrelated executable') f.put(join(directory, 'radar'), 'preserve unrelated executable', 0o755);
    if (scenario === 'symlink executable') { f.put(join(f.home, 'external'), 'preserve external'); symlinkSync(join(f.home, 'external'), join(directory, 'radar')); }
    if (scenario === 'directory executable') mkdirSync(join(directory, 'radar'), { mode: 0o755 });
    if (scenario === 'symlink receipt') { f.put(join(f.home, 'external'), '{}'); symlinkSync(join(f.home, 'external'), join(directory, '.radar-install.json')); }
    if (scenario === 'invalid receipt') f.put(join(directory, '.radar-install.json'), '{}');
    if (scenario === 'existing lock') mkdirSync(join(directory, '.radar-install.lock'), { mode: 0o700 });
    const result = f.run({ installDir });
    assert.notEqual(result.status, 0, result.output);
    assert.equal(existsSync(join(f.home, 'requests')), false, 'fail before any download');
    if (scenario === 'unrelated executable') assert.equal(readFileSync(join(directory, 'radar'), 'utf8'), 'preserve unrelated executable');
    // Restore traversal/write rights for cleanup even if this scenario failed.
    chmodSync(directory, 0o755);
  });
}

for (const mutation of ['unsigned', 'unknown publisher', 'bad signature', 'archive checksum', 'binary checksum', 'wrong CPU', 'wrong filename', 'incompatible epoch', 'traversal', 'symlink entry', 'duplicate entry', 'notifier on Linux', 'notifier metadata on Linux', 'truncated tar', 'non-executable binary', 'missing manual', 'missing asset', 'duplicate asset', 'HTTP redirect', 'npm pending', 'newer Node required']) {
  test(`fails closed before installation: ${mutation}`, t => {
    const f = fixture(t, { mutation }), result = f.run();
    assert.notEqual(result.status, 0, result.output);
    assert.equal(existsSync(join(f.home, '.local/bin/radar')), false);
    assert.equal(existsSync(join(f.home, '.local/bin/.radar-install.json')), false);
    assert.equal(existsSync(join(f.home, '.local/bin/.radar-install.lock')), false);
    assert.equal(existsSync(join(f.home, 'outside')), false);
  });
}

test('reruns safely and upgrades only the installer-owned binary', t => {
  const f = fixture(t);
  let result = f.run(); assert.equal(result.status, 0, result.output); installed(f);
  result = f.run(); assert.equal(result.status, 0, result.output); installed(f);
  f.publish('v1.2.4');
  result = f.run(); assert.equal(result.status, 0, result.output);
  assert.equal(installed(f).version, 'v1.2.4');
});

test('upgrades an epoch 1 owned installation without touching task data', t => {
  const f = fixture(t);
  assert.equal(f.run().status, 0);
  const receiptPath = join(f.home, '.local/bin/.radar-install.json');
  const receipt = JSON.parse(readFileSync(receiptPath));
  receipt.state_epoch = 1;
  writeFileSync(receiptPath, JSON.stringify(receipt));
  const statePath = join(f.home, '.local/state/radar/tasks.json');
  const state = '{"version":8,"task_records":[],"source_refs":[]}';
  f.put(statePath, state);
  f.publish('v1.2.4');
  const result = f.run();
  assert.equal(result.status, 0, result.output);
  assert.equal(installed(f).state_epoch, 2);
  assert.equal(readFileSync(statePath, 'utf8'), state);
});

test('refuses an installation receipt from an unknown future epoch', t => {
  const f = fixture(t);
  assert.equal(f.run().status, 0);
  const receiptPath = join(f.home, '.local/bin/.radar-install.json');
  const receipt = JSON.parse(readFileSync(receiptPath));
  receipt.state_epoch = 3;
  writeFileSync(receiptPath, JSON.stringify(receipt));
  const binary = join(f.home, '.local/bin/radar'), before = readFileSync(binary);
  const result = f.run();
  assert.notEqual(result.status, 0, result.output);
  assert.match(result.output, /incompatible Linux installation receipt/);
  assert.deepEqual(readFileSync(binary), before);
});

test('refuses a modified installer-owned executable', t => {
  const f = fixture(t); assert.equal(f.run().status, 0);
  const binary = join(f.home, '.local/bin/radar'); writeFileSync(binary, 'unrelated replacement');
  const result = f.run(); assert.notEqual(result.status, 0); assert.match(result.output, /not managed/);
  assert.equal(readFileSync(binary, 'utf8'), 'unrelated replacement');
});

test('refuses a replacement made while the download is in progress', t => {
  const f = fixture(t); assert.equal(f.run().status, 0);
  f.put(join(f.home, 'replace-during-download'), '');
  const result = f.run(); assert.notEqual(result.status, 0); assert.match(result.output, /changed during the download/);
  assert.equal(readFileSync(join(f.home, '.local/bin/radar'), 'utf8'), 'unrelated replacement');
});

test('atomic upgrade failure preserves the old executable and permits retry', t => {
  const f = fixture(t); assert.equal(f.run().status, 0);
  const binary = join(f.home, '.local/bin/radar'), before = readFileSync(binary);
  f.publish('v1.2.4'); f.put(join(f.home, 'fail-rename'), '');
  let result = f.run(); assert.notEqual(result.status, 0); assert.match(result.output, /atomic rename failure/);
  assert.deepEqual(readFileSync(binary), before);
  assert.equal(readdirSync(dirname(binary)).some(p => p.startsWith('.radar-stage-') || p === '.radar-install.lock'), false);
  rmSync(join(f.home, 'fail-rename'));
  result = f.run(); assert.equal(result.status, 0, result.output); assert.equal(installed(f).version, 'v1.2.4');
});

test('interrupted fresh activation permits a safe retry', t => {
  const f = fixture(t); f.put(join(f.home, 'fail-rename'), '');
  assert.notEqual(f.run().status, 0); assert.equal(existsSync(join(f.home, '.local/bin/radar')), false);
  rmSync(join(f.home, 'fail-rename'));
  const result = f.run(); assert.equal(result.status, 0, result.output); installed(f);
});

test('never downgrades to an older fully published release', t => {
  const f = fixture(t); f.publish('v1.2.4'); assert.equal(f.run().status, 0);
  delete f.routes['https://registry.npmjs.org/%40christianmoesl%2Fpi-radar/1.2.4'];
  f.put(join(f.home, 'routes.json'), JSON.stringify(f.routes));
  const result = f.run(); assert.notEqual(result.status, 0); assert.match(result.output, /Refusing to downgrade/);
  assert.equal(installed(f).version, 'v1.2.4');
});

test('explicit modes survive a restrictive umask', t => {
  const f = fixture(t), result = f.run({ umask: '077' });
  assert.equal(result.status, 0, result.output); installed(f);
});

for (const scenario of ['root', 'old Node', 'unsupported architecture', 'unknown argument']) {
  test(`actionable prerequisite error: ${scenario}`, t => {
    const f = fixture(t);
    if (scenario === 'root') f.shell('id', 'echo 0');
    if (scenario === 'old Node') f.shell('node', 'exit 1');
    if (scenario === 'unsupported architecture') f.shell('uname', 'if [ "$1" = -s ]; then echo Linux; else echo riscv64; fi');
    const result = f.run({ args: scenario === 'unknown argument' ? ['--modify-path'] : [] });
    assert.notEqual(result.status, 0);
    assert.match(result.output, scenario === 'root' ? /without sudo/ : scenario === 'old Node' ? /Node.js 24\+/ : scenario === 'unsupported architecture' ? /Unsupported Linux architecture/ : /usage:/);
    assert.equal(existsSync(join(f.home, 'requests')), false);
    assert.equal(existsSync(join(f.home, 'manager-calls')), false);
  });
}

test('shared verifier still authenticates and extracts macOS releases', t => {
  const f = fixture(t, { platform: 'darwin', arch: 'arm64' }), result = f.run();
  assert.equal(result.status, 0, result.output);
  assert.match(result.output, /publisher signature and notification companion/);
  const selected = JSON.parse(readFileSync(join(f.work, 'selected.json')));
  assert.equal(selected.version, 'v1.2.3');
  assert.notEqual(lstatSync(join(selected.directory, 'bin/radar')).mode & 0o111, 0);
  assert.equal(existsSync(join(f.home, '.local/bin/radar')), false);
});
