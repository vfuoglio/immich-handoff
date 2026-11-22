// Two things make the plain Markdown of ../docs work as site pages without changing how GitHub shows it:
//  - a link to another page ("../guides/show/stories.md#heading") becomes the page's address on the site, and a link to
//    something outside docs/ (the changelog, the demo) becomes a link into the repository;
//  - the page's own "# Title" is dropped, because Starlight shows the title from the front matter;
//  - a GitHub alert ("> [!NOTE]") becomes a Starlight aside (the alerts plugin only reads pages inside src/content/docs);
//  - the tabs fence (":::tabs", "### label" ... ":::") becomes the .doc-tabs HTML that doc-tabs.js turns into tabs.
import path from 'node:path';
import { visit } from 'unist-util-visit';

export default function remarkDocs({ base, docsRoot, repo }) {
  const root = path.resolve(docsRoot);
  return (tree, file) => {
    const here = path.dirname(file.path);

    const first = tree.children[0];
    if (first && first.type === 'heading' && first.depth === 1) tree.children.shift();

    visit(tree, 'link', (node) => {
      const url = node.url || '';
      if (/^[a-z][a-z0-9+.-]*:/i.test(url) || url.startsWith('#') || url.startsWith('/')) return;
      const [p, frag] = url.split('#');
      const target = path.resolve(here, p);
      const hash = frag ? '#' + frag : '';
      if (target.startsWith(root + path.sep) || target === root) {
        const rel = path.relative(root, target).split(path.sep).join('/');
        if (rel.endsWith('.md') || rel.endsWith('.mdx')) {
          let slug = rel.replace(/\.mdx?$/, '');
          if (slug === 'README' || slug === 'index') slug = '';
          else slug = slug.replace(/\/(README|index)$/, '');
          node.url = `${base}/${slug}${slug ? '/' : ''}${hash}`;
          return;
        }
        if (rel.startsWith('images/')) return;
      }
      const inRepo = path.relative(path.resolve(root, '..'), target).split(path.sep).join('/');
      if (!inRepo.startsWith('..')) node.url = `${repo}/blob/main/${inRepo}${hash}`;
    });

    // a picture written as HTML (to give it a width on GitHub) is not found by Astro; docs/images is copied to public/images
    visit(tree, 'html', (node) => {
      node.value = node.value.replace(/src="(?:\.\.\/)*images\/([^"]+)"/g, `src="${base}/images/$1"`);
    });

    const asides = { NOTE: ['note', 'Note'], TIP: ['tip', 'Tip'], IMPORTANT: ['note', 'Important'], WARNING: ['caution', 'Warning'], CAUTION: ['danger', 'Caution'] };
    const walkAlerts = (parent) => {
      // from the end, so that the nodes a splice adds do not move the ones still to be looked at
      for (let i = parent.children.length - 1; i >= 0; i--) {
        const n = parent.children[i];
        if (n.type !== 'blockquote') { if (n.children) walkAlerts(n); continue; }
        const p = n.children[0];
        const t = p && p.type === 'paragraph' && p.children[0] && p.children[0].type === 'text' ? p.children[0] : null;
        const m = t && t.value.match(/^\[!(NOTE|TIP|IMPORTANT|WARNING|CAUTION)\][ \t]*\n?/);
        if (!m) continue;
        const [kind, title] = asides[m[1]];
        t.value = t.value.slice(m[0].length);
        if (!t.value && p.children.length === 1) n.children.shift();
        parent.children.splice(i, 1,
          { type: 'html', value: `<aside aria-label="${title}" class="starlight-aside starlight-aside--${kind}"><p class="starlight-aside__title" aria-hidden="true">${title}</p><div class="starlight-aside__content">` },
          ...n.children,
          { type: 'html', value: '</div></aside>' });
      }
    };
    walkAlerts(tree);

    const kids = tree.children;
    for (let i = 0; i < kids.length; i++) {
      const open = kids[i];
      if (open.type !== 'paragraph' || kids[i].children.length !== 1 || open.children[0].value !== ':::tabs') continue;
      let close = -1;
      for (let j = i + 1; j < kids.length; j++) {
        if (kids[j].type === 'paragraph' && kids[j].children.length === 1 && kids[j].children[0].value === ':::') { close = j; break; }
      }
      if (close < 0) continue;
      const out = [{ type: 'html', value: '<div class="doc-tabs">' }];
      let inTab = false;
      for (const n of kids.slice(i + 1, close)) {
        if (n.type === 'heading' && n.depth === 3) {
          if (inTab) out.push({ type: 'html', value: '</div>' });
          const label = n.children.map((c) => c.value || '').join('').replace(/"/g, '&quot;');
          out.push({ type: 'html', value: `<div class="doc-tab" data-label="${label}">` });
          inTab = true;
        } else out.push(n);
      }
      if (inTab) out.push({ type: 'html', value: '</div>' });
      out.push({ type: 'html', value: '</div>' });
      kids.splice(i, close - i + 1, ...out);
    }
  };
}
