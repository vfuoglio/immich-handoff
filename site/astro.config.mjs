import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';
import { unified } from '@astrojs/markdown-remark';
import remarkDocs from './src/plugins/remark-docs.mjs';
import { sidebar } from './src/plugins/sidebar.mjs';
import { locales } from './src/plugins/labels.mjs';

const base = '/immich-handoff';
const repo = 'https://github.com/vfuoglio/immich-handoff';

export default defineConfig({
  site: 'https://vfuoglio.github.io',
  base,
  markdown: { processor: unified({ remarkPlugins: [[remarkDocs, { base, docsRoot: '../docs', repo }]] }) },
  integrations: [
    starlight({
      title: 'Handoff',
      defaultLocale: 'root',
      locales,
      description: 'Hand photos from one Immich account to another, just by tagging them.',
      logo: { src: './src/assets/icon.svg', alt: '' },
      favicon: '/icon.svg',
      social: [{ icon: 'github', label: 'GitHub', href: repo }],
      editLink: { baseUrl: `${repo}/edit/develop/docs/` },
      customCss: ['./src/styles/custom.css'],
      head: [{ tag: 'script', attrs: { src: `${base}/doc-tabs.js`, defer: true } }],
      sidebar: sidebar('../docs'),
    }),
  ],
});
