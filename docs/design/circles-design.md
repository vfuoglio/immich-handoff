# Circles

**Built** in 1.4.0 (#270). Immich's discussion 1633 (user groups, 146 votes) asks for it, and Immich froze changes to sharing (immich#12614).

## What it does

A circle is a named group of the owner's choice. Every album the owner makes after the circle (matching an optional start of the name) is offered to its people, and, once the owner says yes, they are added to the album with the owner's own key, as `viewer` or `editor`.

## How

- **Finding new albums.** Immich has no event for a new album (its workflow triggers are about assets, and the webhook is about assets). So `CirclesMaintenance` runs in the rules' round (`RulesMaintenance`, every `RULES_CHECK_MINUTES`) and lists the owner's albums with the circle key. An album counts when it was created after the circle and is older than the settle wait (so that it can be named and filled).
- **What is recorded.** `circle_shares` has a row for each circle, album and person: `offered`, `shared`, `declined`, `had` (the person was on the album already: never touched) and `left` (the app added them and they have since left: never added again). A row means the pair is never offered twice, so the sweep is safe to repeat.
- **Asking.** Offers wait as one Needs-you card for the circle: *Share them*, *Not these*, *Always*. After three *Always*, the owner can let the circle share by itself, only for albums created after the switch and older than six times the settle wait.
- **Taking a person out** calls `DELETE /albums/{id}/user/{userId}` for the rows in state `shared` only, then forgets the person. Ending a circle forgets the records and leaves the albums as they are.
- **The existing albums** are offered by the owner's own action (a catch-up), never automatically, and the same record rules apply.
- **Who can be in a circle.** The administrator chooses (a setting in the data file, `settings` table, key `circle-method`): the owner types an email (default; the same exact-match lookup as drop box co-hosts and pairings) or picks from the list Immich gives any user (`GET /users`), which shows every account's name and email to every person, so it is off by default.
- **The key.** `handoff: circles`, made with the person's own session: `user.read`, `album.read`, `albumUser.create`, `albumUser.delete`. Sealed in `circle_keys` (vault context `circles:`), deleted with the last circle. It cannot read a photo.

## Not done

- A person who is added to a circle later is not put on the albums the circle already shared; the owner offers the existing albums again.
- A renamed album is judged by its name when it is first seen.
- Immich may email each person for each album; the app cannot prevent that, and says so.
