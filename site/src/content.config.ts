import { defineCollection, z } from 'astro:content';
import { glob } from 'astro/loaders';
import { docsSchema } from '@astrojs/starlight/schema';
import { moved } from './plugins/moved.mjs';

// The site has no content of its own: it reads the Markdown of ../docs, the same files GitHub shows and the app embeds.
// README.md (the index for GitHub readers) and images/README.md are not pages; the landing page is docs/index.mdx.
export const collections = {
  docs: defineCollection({
    loader: glob({ base: '../docs', pattern: ['**/*.{md,mdx}', '!README.md', '!images/**', ...Object.keys(moved()).map((p) => `!${p}.md`)] }),
    schema: docsSchema({
      extend: z.object({
        feature: z.string().optional(),
        audience: z.enum(['user', 'admin', 'contributor']).optional(),
        goal: z.string().optional(),
        order: z.number().optional(),
        tip: z.string().optional(),
        source_rev: z.string().optional(),
      }),
    }),
  }),
};
