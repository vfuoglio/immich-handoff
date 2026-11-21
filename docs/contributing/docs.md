---
title: "How the documentation is built"
audience: contributor
---

# How the documentation is built

The pages in `docs/` are the only source. They are plain Markdown that GitHub shows as it is, and two things are made from them: the public site (`site/`, Starlight) and the Help inside the app (`internal/docs`, embedded in the binary). Nothing is written twice.

## A page

```
---
title: "Stories: a shareable animated recap"
feature: stories          # a stable key, used by the "?" links; lower case with hyphens, one page each
audience: user            # user, admin or contributor (contributor pages are on the site, never in the app)
goal: show                # start, share, collect, automate, show or run
order: 40                 # position inside its goal
tip: "One line, 140 characters at most, shown as the tooltip."
---
```

Pages under `docs/contributing/` need only a `title`. Translations go in `docs/<language>/` with the same paths and a `source_rev` (the revision of the English page they were made from); a page that is not translated shows in English.

Write plain Markdown. GitHub's alerts (`> [!NOTE]`, `[!TIP]`, `[!IMPORTANT]`, `[!WARNING]`, `[!CAUTION]`) are the only addition, and so is one fence for tabs:

````
:::tabs
### Docker Compose
```
docker compose up -d
```
### docker run
Text and code for the second tab.
:::
````

On GitHub the fence reads as two headings. Do not use MDX in a page.

## What runs where

- `go generate ./...` runs `internal/docs/gen.go`: it checks every page's front matter (`docs.Validate`) and writes `internal/docs/bundle/` (the pages for users and administrators, `index.json` and `tips.json`). CI's "generated code is up to date" check fails when the bundle is behind the pages.
- `.github/scripts/doc_links_test.sh` checks that every link, image and `#anchor` points at something that exists.
- `cd site && npm ci && npm run build && npm run check` builds the site and checks its internal links. The site reads `../docs`; it has no content of its own.
