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

A page may also carry `tips`, the tooltips of its buttons by key (`tips: {send: "Send photos from this device"}`, each on one line). A template asks for one with `tipOf(ctx, "the-page", "send")` and shows it as `data-tip`; the tooltip is translated through the app's catalogue by its English text, and a test fails when a template asks for a key that no page has. A card's "?" is `@HelpLink("feature")`, which opens that feature's guide in a drawer and shows nothing for a feature without a guide.

`docs/start/the-page.md` also carries the stops of the first-run tour (`tour:`: `page` user or admin, `target`, `title`, `text`, optional `feature`). A `target` is one of `docs.TourTargets`; `internal/web/tour.go` says where each one is on the page (a selector, and the view, part or category of Settings to show first), and a test fails when the two lists differ. A stop whose element is not on the page (Settings with nothing on, the backups card without backups) is left out when the tour runs.

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

## How a guide is written

The voice is warm and concrete: second person, short sentences, real people and places in the examples (Alice and Bob, a week in Lisbon), the benefit before the mechanism, and no pile of parenthetical asides. Call a button by the words on it, in bold, and say where to find it as it is today.

A feature guide has this shape:

1. **A title, one sentence that says what you get, and a picture** of the real thing.
2. **Make it** (or **Set it up**): three to six numbered steps, each one action and one result, with a picture where it helps.
3. **Good to know**: limits, privacy, keys and what is stored.
4. **If something is off**: what the person sees when it goes wrong and what to do.
5. **What next**: two or three related guides.

Merge two guides only when they say the same thing; keep both when the reader differs (an owner setting up, a sender using) and link instead of repeating.

Pictures are taken from the real app with `tools/screenshots` (see its README) against a throwaway Immich, in the English light variant of the default colour scheme; a new scene goes in `main.go` or a file of its own (`stories.go` is a small example that makes its own data through the app's own requests). Retake them when the layout changes.

## What runs where

- `go generate ./...` runs `internal/docs/gen.go`: it checks every page's front matter (`docs.Validate`) and writes `internal/docs/bundle/` (the pages for users and administrators, `index.json` and `tips.json`). CI's "generated code is up to date" check fails when the bundle is behind the pages.
- In the app, signed-in people read the same pages at `/help` (an administrator's pages only for administrators): `internal/docs` renders them, rewrites their links (to Help, to the public site, or to the repository) and serves slimmed copies of their pictures, and `ts/help.ts` opens the drawer. The bundle is made by `go generate`, and keeps the pictures the pages show (JPEG, 900 px at most; a GIF is its first frame).
- `.github/scripts/doc_links_test.sh` checks that every link, image and `#anchor` points at something that exists.
- `cd site && npm ci && npm run build && npm run check` builds the site and checks its internal links. The site reads `../docs`; it has no content of its own.
