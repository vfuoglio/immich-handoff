// The sidebar comes from the pages' own front matter (goal, order), so adding a page needs no change here.
import fs from 'node:fs';
import path from 'node:path';
import YAML from 'yaml';
import { moved } from './moved.mjs';
import { groups } from './labels.mjs';

// docs/<language>/strings.yml: the title of every page in that language, for the sidebar where the page itself is not translated
function titles(root) {
  const out = {};
  for (const lang of ['de', 'fr', 'es', 'it', 'pt', 'ru']) {
    const file = path.join(root, lang, 'strings.yml');
    if (fs.existsSync(file)) for (const [feature, v] of Object.entries(YAML.parse(fs.readFileSync(file, 'utf8')) || {})) (out[feature] ||= {})[lang] = v.title;
  }
  return out;
}

function pages(root) {
  const out = [];
  const walk = (dir) => {
    for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
      const p = path.join(dir, e.name);
      if (e.isDirectory()) { if (e.name !== 'images') walk(p); continue; }
      if (!/\.mdx?$/.test(e.name)) continue;
      const rel = path.relative(root, p).split(path.sep).join('/');
      if (rel.replace(/\.mdx?$/, '') in moved()) continue; // a pointer to a page that moved
      if (rel === 'README.md' || /^(de|fr|es|it|pt|ru)\//.test(rel)) continue; // the translations are the same pages in another language
      const m = fs.readFileSync(p, 'utf8').match(/^---\n([\s\S]*?)\n---\n/);
      out.push({ rel, slug: rel.replace(/\.mdx?$/, ''), ...(m ? YAML.parse(m[1]) : {}) });
    }
  };
  walk(root);
  return out;
}

export function sidebar(docsRoot) {
  const all = pages(docsRoot).filter((p) => p.slug !== 'index');
  const names = titles(docsRoot);
  const by = (pred) => all.filter(pred).sort((a, b) => (a.order ?? 999) - (b.order ?? 999) || a.slug.localeCompare(b.slug)).map((p) => (p.feature && names[p.feature] ? { slug: p.slug, translations: names[p.feature] } : { slug: p.slug }));
  const group = (label, rest) => ({ label, translations: groups[label] || {}, ...rest });
  const goal = (g) => (p) => p.audience !== 'contributor' && p.goal === g && !p.slug.startsWith('admin/') && !p.slug.startsWith('reference/');
  return [
    group('Start here', { items: by(goal('start')) }),
    group('By goal', {
      items: [
        group('Share', { items: by(goal('share')) }),
        group('Collect', { items: by(goal('collect')) }),
        group('Automate', { items: by(goal('automate')) }),
        group('Show and be told', { items: by(goal('show')) }),
      ],
    }),
    group('Admin and reference', { items: by((p) => p.audience !== 'contributor' && (p.slug.startsWith('admin/') || p.slug.startsWith('reference/')) && p.slug !== 'reference/faq') }),
    group('Contributing', { collapsed: true, items: by((p) => p.audience === 'contributor' && !p.slug.startsWith('contributing/design/')).concat([group('Design notes', { collapsed: true, items: by((p) => p.slug.startsWith('contributing/design/')) })]) }),
  ];
}
