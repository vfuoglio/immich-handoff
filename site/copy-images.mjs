// The pictures that pages show as HTML (to give them a width on GitHub) are served from public/images, a copy of ../docs/images.
import fs from 'node:fs';
fs.rmSync(new URL('./public/images', import.meta.url), { recursive: true, force: true });
fs.cpSync(new URL('../docs/images', import.meta.url), new URL('./public/images', import.meta.url), { recursive: true });
