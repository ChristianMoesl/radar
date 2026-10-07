import { createHash, createPrivateKey, createPublicKey, sign, verify } from 'node:crypto';
import { readFileSync, writeFileSync, readdirSync, lstatSync, realpathSync } from 'node:fs';
import { relative, join, basename } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('../', import.meta.url));
const sha = data => createHash('sha256').update(data).digest('hex');
const read = path => JSON.parse(readFileSync(path, 'utf8'));
const componentVersion = () => {
  const version = readFileSync(join(root, 'macos/RadarNotifier/VERSION'), 'utf8').trim();
  if (!/^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$/.test(version)) throw new Error('invalid notifier component version');
  return version;
};
function files(directory) {
  return readdirSync(directory).sort().flatMap(name => {
    const path = join(directory, name), stat = lstatSync(path);
    if (stat.isDirectory()) return files(path);
    if (!stat.isFile()) throw new Error(`not a regular file: ${path}`);
    return [path];
  }).sort();
}
export function treeDigest(directory) {
  return sha(files(directory).map(file => `${relative(directory, file)}\0${lstatSync(file).mode & 0o111 ? 1 : 0}\0${sha(readFileSync(file))}\n`).join(''));
}
function sourceDigest() {
  return sha([...files(join(root, 'macos/RadarNotifier')), join(root, 'scripts/build-notifier-app.sh')].sort().map(file => `${relative(root, file)}\0${sha(readFileSync(file))}\n`).join(''));
}
function signFile(file, value) {
  const bytes = Buffer.from(JSON.stringify(value, null, 2) + '\n');
  const keyId = process.env.RADAR_RELEASE_KEY_ID;
  const key = createPrivateKey(process.env.RADAR_RELEASE_SIGNING_KEY ?? '');
  if (key.asymmetricKeyType !== 'ed25519') throw new Error('release key must be Ed25519');
  const publicKey = createPublicKey(key).export({ type: 'spki', format: 'der' }).subarray(-32).toString('base64');
  if (!keyId || read(join(root, 'internal/update/keys.json'))[keyId] !== publicKey) throw new Error('signing key is not in the committed trust store');
  writeFileSync(file, bytes);
  writeFileSync(`${file}.sig`, JSON.stringify({ key_id: keyId, signature: sign(null, bytes, key).toString('base64') }) + '\n');
}
function verifyFile(file) {
  const bytes = readFileSync(file), signature = read(`${file}.sig`);
  const raw = Buffer.from(read(join(root, 'internal/update/keys.json'))[signature.key_id] ?? '', 'base64');
  if (raw.length !== 32) throw new Error('untrusted signing key');
  const key = createPublicKey({ key: Buffer.concat([Buffer.from('302a300506032b6570032100', 'hex'), raw]), format: 'der', type: 'spki' });
  if (!verify(null, bytes, key, Buffer.from(signature.signature, 'base64'))) throw new Error('invalid signature');
  return JSON.parse(bytes);
}
function artifact(directory, archive) {
  return { file: basename(archive), size: lstatSync(archive).size, sha256: sha(readFileSync(archive)), binary_sha256: sha(readFileSync(join(directory, 'bin/radar'))), notifier_version: componentVersion(), notifier_sha256: treeDigest(join(directory, 'libexec/radar/RadarNotifier.app')) };
}
function run([command, directory, argument]) {
  if (command === 'artifact') {
    writeFileSync(`${argument}.json`, JSON.stringify(artifact(directory, argument)) + '\n');
  } else if (command === 'release') {
    const pkg = read(join(root, 'package.json'));
    if (argument !== `v${pkg.version}` || !/^v\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$/.test(argument)) throw new Error('releases require a matching npm version');
    const artifacts = Object.fromEntries(['amd64', 'arm64'].map(arch => [arch, read(join(directory, `radar_${argument}_darwin_${arch}.tar.gz.json`))]));
    const minPi = pkg.peerDependencies['@earendil-works/pi-coding-agent'].match(/^>=([0-9]+\.[0-9]+\.[0-9]+)$/)?.[1];
    const minNode = Number(pkg.engines.node.match(/^>=([0-9]+)$/)?.[1]);
    const epoch = Number(readFileSync(join(root, 'internal/update/manifest.go'), 'utf8').match(/^const StateEpoch = ([0-9]+)/m)?.[1]);
    if (!minPi || !minNode || !epoch) throw new Error('unsupported prerequisite/epoch policy; update the release contract explicitly');
    signFile(join(directory, 'release.json'), { schema: 1, version: argument, state_epoch: epoch, min_macos: '13.0', pi_version: pkg.version, min_pi: minPi, min_node: minNode, artifacts });
  } else if (command === 'component') {
    const version = componentVersion();
    const artifacts = Object.fromEntries(['amd64', 'arm64'].map(arch => {
      const file = `radar-notifier_${version}_${arch}.tar.gz`, archive = join(directory, file);
      return [arch, { file, size: lstatSync(archive).size, sha256: sha(readFileSync(archive)), tree_sha256: treeDigest(join(directory, arch, 'RadarNotifier.app')) }];
    }));
    signFile(join(directory, 'notifier.json'), { schema: 1, version, source_sha256: sourceDigest(), artifacts });
  } else if (command === 'verify-component') {
    const manifest = verifyFile(join(directory, 'notifier.json'));
    if (manifest.schema !== 1 || manifest.version !== componentVersion() || manifest.source_sha256 !== sourceDigest()) throw new Error('notifier inputs changed: bump its component version instead of replacing an existing artifact');
    for (const arch of ['amd64', 'arm64']) {
      const a = manifest.artifacts[arch];
      if (a.file !== `radar-notifier_${manifest.version}_${arch}.tar.gz`) throw new Error('invalid notifier filename');
      const bytes = readFileSync(join(directory, a.file));
      if (bytes.length !== a.size || sha(bytes) !== a.sha256) throw new Error('notifier archive verification failed');
      if (argument === 'extracted' && treeDigest(join(directory, arch, 'RadarNotifier.app')) !== a.tree_sha256) throw new Error('notifier bundle identity mismatch');
    }
  } else throw new Error('usage: release-metadata.mjs artifact|release|component|verify-component <directory> [archive|tag|extracted]');
}
if (process.argv[1] && realpathSync(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try { run(process.argv.slice(2)); } catch (error) { console.error(error.message); process.exitCode = 1; }
}
