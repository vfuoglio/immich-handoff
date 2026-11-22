// docs/redirects.txt: pages that moved. The old file is a one-line pointer for GitHub readers; the site skips it and redirects its address.
import fs from 'node:fs';

export function moved(docsRoot = '../docs') {
  const out = {};
  for (const line of fs.readFileSync(`${docsRoot}/redirects.txt`, 'utf8').split('\n')) {
    if (!line.trim() || line.startsWith('#')) continue;
    const [from, to] = line.trim().split(/\s+/);
    out[from.replace(/\.md$/, '')] = to.replace(/\.md$/, '');
  }
  return out;
}
