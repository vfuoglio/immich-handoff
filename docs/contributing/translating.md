---
title: "Translating the documentation"
audience: contributor
---

# Translating the documentation

The app's own screens are translated into six languages (de, fr, es, it, pt, ru). The documentation follows the same model, but it is much longer, so only a core is translated and kept up to date: the Quick start, "Finding your way around the page", the FAQ, "Using it", the tour, the button tips, and the title and the one-line tip of every page. Every other page shows in English, with a note, until someone translates it. This page says how to add or update one.

## What is where

- `docs/<language>/<same path as the English page>.md` is a translated page (`docs/de/start/quickstart.md`).
- `docs/<language>/strings.yml` has the title and the tip of every page (`feature: {title, tip}`), so the lists in Help and the tooltips are in the language even for pages that are not translated. It must cover every feature, or none.
- A page that is not there is read in English.

## A translated page

```
---
title: "Schnellstart"                 # the same as the title in strings.yml
tip: "In zehn Minuten zum ersten Foto."
source_rev: 3fa9c1d2                  # the revision of the English page this was made from (see below)
review: needed                        # needed until a native speaker has checked it, then done
---

> [!NOTE]
> This page was translated by machine and has not been checked yet. ...   (in the language of the page)

# Schnellstart
...
```

Only `title`, `tip`, `tips` (the button tips, by the same keys as the English), `tour` (the stops of the tour, by `page` and `target`, only in `start/the-page.md`), `source_rev` and `review` belong in the front matter. `feature`, `audience`, `goal` and `order` come from the English page.

- **Translate the words, not the shape.** The page has the same headings, tables, pictures and links as the English one; the generator refuses one that does not.
- **Use the app's own words.** A **bold** name of a button or a setting must be exactly what the app shows in that language (it is in `internal/i18n/lang/<language>.json`); a test fails when it is not. Use the glossary below for the rest.
- **Links.** Link to another page by its path as for the English page, with no `#anchor` (headings are translated, so their ids are not the same). A page that is translated is linked inside the language folder (`the-page.md`); one that is not is linked as the English file (`../../guides/share/household.md`).
- **Pictures** are the English screenshots; the paths are the same as in the English page.
- **`source_rev`** is the short hash of the English page's text when you translated it. Get it with `cd internal/docs && go run gen.go -rev start/quickstart.md`. When the English page changes, the page is listed as stale in CI (a warning, never a failure) and shows a note in Help, until you update it and its `source_rev`.
- **`review: done`** is set by a native speaker, in the same change that removes the machine-translation note.

## Glossary

Keep these words the same in every page of a language, and as the app has them.

| English | Meaning |
|---|---|
| service | A named place photos are handed to, with one tag |
| sender, receiver, owner | Who sends, who receives, who offers a service |
| tag, inbox | The label that sends a photo, and the label on what arrived |
| drop box, wall, gallery | A link guests send photos to, a live screen, the guests' view |
| pool | A service where everybody sends and everybody receives |
| rule, suggestion | A search that offers photos; what it offers |
| key | The restricted API key the app makes in the person's own Immich account |
| hand over, take back | Send a photo to a service; undo the last hand-over |

## Checking

```
go generate ./...                 # validates every translation and makes the bundle
go test ./internal/docs           # the app's own words, the shape, the fallback
bash .github/scripts/doc_links_test.sh
cd site && npm run build && npm run check
```
