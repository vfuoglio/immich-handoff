// The sidebar comes from the pages' own front matter (goal, order), so adding a page needs no change here.
import fs from 'node:fs';
import path from 'node:path';
import YAML from 'yaml';

function pages(root) {
  const out = [];
  const walk = (dir) => {
    for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
      const p = path.join(dir, e.name);
      if (e.isDirectory()) { if (e.name !== 'images') walk(p); continue; }
      if (!/\.mdx?$/.test(e.name)) continue;
      const rel = path.relative(root, p).split(path.sep).join('/');
      if (rel === 'README.md') continue;
      const m = fs.readFileSync(p, 'utf8').match(/^---\n([\s\S]*?)\n---\n/);
      out.push({ rel, slug: rel.replace(/\.mdx?$/, ''), ...(m ? YAML.parse(m[1]) : {}) });
    }
  };
  walk(root);
  return out;
}

export function sidebar(docsRoot) {
  const all = pages(docsRoot).filter((p) => p.slug !== 'index');
  const by = (pred) => all.filter(pred).sort((a, b) => (a.order ?? 999) - (b.order ?? 999) || a.slug.localeCompare(b.slug)).map((p) => ({ slug: p.slug }));
  const goal = (g) => (p) => p.audience !== 'contributor' && p.goal === g && !p.slug.startsWith('admin/') && !p.slug.startsWith('reference/');
  return [
    { label: 'Start here', items: by(goal('start')) },
    {
      label: 'By goal',
      items: [
        { label: 'Share', items: by(goal('share')) },
        { label: 'Collect', items: by(goal('collect')) },
        { label: 'Automate', items: by(goal('automate')) },
        { label: 'Show and be told', items: by(goal('show')) },
      ],
    },
    { label: 'Admin and reference', items: by((p) => p.audience !== 'contributor' && (p.slug.startsWith('admin/') || p.slug.startsWith('reference/')) && p.slug !== 'reference/faq') },
    { label: 'Contributing', collapsed: true, items: by((p) => p.audience === 'contributor' && !p.slug.startsWith('contributing/design/')).concat([{ label: 'Design notes', collapsed: true, items: by((p) => p.slug.startsWith('contributing/design/')) }]) },
  ];
}
