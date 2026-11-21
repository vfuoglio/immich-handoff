# Smart albums

**Built** in 1.4.0 (#271). Immich's discussion 1673 (rule-based smart albums, 188 votes) asks for it.

## What it does

A rule of kind `album` keeps one album of the person's own up to date with what a rule finds. It is the ordinary rule (people, dates, place, an album, a phrase) with two more conditions, a camera (make and model) and tags, and a target that is an album instead of a service.

## How

- **The rule.** Kind `album`; `Dest` is the album's id and `DestAlbum` its name; `Service` is empty, as for folder and stack rules. Conditions are checked against the person's own Immich when the rule is made (`ruleCond`, shared with rules for services), so nothing is made up. A new album is made at that moment with the rule's key, so the rule knows it by id.
- **The pass.** `lookAlbum` uses the same finder as every rule (`FindForRule`: Immich searches what it can; the app drops photos that are not the person's, archived, trashed, not settled, or already handled) with the rule's own key, and also drops what is already in the album. Matches are suggestions; an answer of yes puts them in the album (`albumForRule`, 250 at a time); *Always* three times lets the rule go on by itself for photos that arrive after the switch. Cameras and tags use Immich's own search filters (`make`, `model`, `tagIds`).
- **Older photos.** The rule only looks at photos that reached Immich after it was made, in the last week. `LookBack` is the one time it looks at older ones: up to 500 of them, as suggestions, so it asks first.
- **Taking out.** Off by default (`RemoveStale`). A pass searches everything that matches now, and takes out of the album the photos **this rule put there** (state `sent`) that are no longer found. It never touches what the rule did not put there. The photos taken out are marked declined, so they are not offered again. A phrase finds only the closest 100, which would take out the wrong photos, so the combination is refused.
- **The key.** `handoff: smart albums`, made with the person's own session on the first smart album: `user.read`, `asset.read`, `album.read`, `album.create`, `albumAsset.create`, plus `albumAsset.delete` when the person asked for taking out (given to an existing key through their own session). Stored sealed like the others (`album_keys`, vault context `albums:`), deleted with the last smart album.

## Not done

- The rule does not follow an album being renamed (it uses the id) and says so if the album is deleted.
- Tags are matched by id, so a tag that is deleted and made again is a different tag.
