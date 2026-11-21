// Every internal link in the built site points at a page or file that exists. Run after `npm run build`.
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
const dist = fileURLToPath(new URL('./dist/', import.meta.url));
const base = '/immich-handoff';
const bad = [];
const walk = (d) => fs.readdirSync(d, { withFileTypes: true }).flatMap((e) => (e.isDirectory() ? walk(path.join(d, e.name)) : [path.join(d, e.name)]));
const exists = (url) => {
  let p = decodeURIComponent(url.split('#')[0].split('?')[0]);
  if (!p.startsWith(base)) return true;
  p = path.join(dist, p.slice(base.length));
  return fs.existsSync(p) && (fs.statSync(p).isFile() || fs.existsSync(path.join(p, 'index.html')));
};
for (const f of walk(dist).filter((f) => f.endsWith('.html'))) {
  const html = fs.readFileSync(f, 'utf8').replace(/<pre[\s\S]*?<\/pre>/g, '').replace(/<code[\s\S]*?<\/code>/g, '');
  if (/\[!(NOTE|TIP|IMPORTANT|WARNING|CAUTION)\]/.test(html)) bad.push(`${path.relative(dist, f)}: an alert (> [!NOTE]) was left as text`);
  for (const m of html.matchAll(/(?:href|src)="([^"]+)"/g)) {
    const u = m[1];
    if (/^[a-z][a-z0-9+.-]*:/i.test(u) || u.startsWith('#') || u.startsWith('//')) continue;
    if (!u.startsWith('/')) { bad.push(`${path.relative(dist, f)}: relative ${u}`); continue; }
    if (!exists(u)) bad.push(`${path.relative(dist, f)}: ${u}`);
  }
}
if (bad.length) { console.log([...new Set(bad)].join('\n')); process.exit(1); }
console.log('every internal link of the site exists');
