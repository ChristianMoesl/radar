import { readFileSync } from 'node:fs';

// A missing tag endpoint does not mean a component version is unused: deleting
// a tag can turn its previously published release into a draft. Inventory every
// page, including drafts, before selecting existing immutable bytes or building.
try {
  const [file, tag, field = 'id', ...extra] = process.argv.slice(2);
  if (extra.length || !/^notifier-v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$/.test(tag ?? '')) {
    throw new Error('usage: notifier-release-state.mjs <paginated-release-json> notifier-vX.Y.Z [id|assets|draft]');
  }
  const pages = JSON.parse(readFileSync(file, 'utf8'));
  if (!Array.isArray(pages) || !pages.every(Array.isArray)) throw new Error('invalid paginated release inventory');
  const releases = pages.flat();
  if (releases.some(r => !r || typeof r.tag_name !== 'string')) throw new Error('invalid release inventory');
  const matches = releases.filter(r => r.tag_name === tag);
  if (matches.some(r => !Number.isSafeInteger(r.id) || r.id <= 0 || typeof r.draft !== 'boolean'
    || !(r.published_at === null || typeof r.published_at === 'string' && r.published_at.length))) {
    throw new Error('invalid notifier release history');
  }
  const published = matches.filter(r => !r.draft && r.published_at !== null);
  let release;
  if (matches.length) {
    if (published.length === 1) release = published[0];
    else if (matches.length === 1 && matches[0].draft && matches[0].published_at === null) release = matches[0];
    else throw new Error(`${tag} has unavailable or ambiguous release history; restore its exact tag/release, never rebuild this version`);
  }
  if (field === 'id') console.log(release?.id ?? 'new');
  else if (field === 'draft' && release) console.log(release.draft);
  else if (field === 'assets' && release) {
    const version = tag.slice('notifier-v'.length);
    const expected = ['notifier.json', 'notifier.json.sig', ...['amd64', 'arm64'].map(a => `radar-notifier_${version}_${a}.tar.gz`)];
    const assets = release.assets;
    if (!Array.isArray(assets) || assets.length !== expected.length
      || assets.some(a => !a || !Number.isSafeInteger(a.id) || a.id <= 0 || !expected.includes(a.name))
      || new Set(assets.map(a => a.id)).size !== expected.length
      || new Set(assets.map(a => a.name)).size !== expected.length) {
      throw new Error(`${tag} has incomplete or unexpected assets; inspect the existing release, never replace its bytes`);
    }
    for (const name of expected) console.log(`${assets.find(a => a.name === name).id}\t${name}`);
  } else throw new Error('invalid notifier release state request');
} catch (error) {
  console.error(error.message);
  process.exitCode = 1;
}
