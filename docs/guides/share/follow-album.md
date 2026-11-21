---
title: "Following an album shared with you"
feature: follow-album
audience: user
goal: share
order: 80
tip: "Have what is added to an album shared with you offered to copy into your library."
---

# Following an album shared with you

An album someone shared with you can be **followed**: what they add to it is offered to you, with pictures, to copy into your own library, so photos do not stay only in their account. On the Overview, under **Add to my library**, open **Follow an album shared with me**, choose the album, whether to start with what is in it now or only with what is added from now on, and where the copies go (in your library tagged with who shared it, in your library as it is, or in an album). This is the same copy as the [Add to my library button](../../admin/find-handoff.md#add-to-my-library-from-inside-immich).

- **It asks first.** New photos wait on the Overview as **N new photos in Trip, shared by Anna**, with **Add to my library**, **Not these** and **Always**; the card links to a page where you can look at them one by one. After you have answered **Always** three times you can let the album **add by itself**: photos added from then on are copied without asking, and you can turn that off at any time.
- **A key of your own.** The first time, the app makes a key named `handoff: library` in your Immich account, with your own sign-in. It can read what is shared with you and add to your own library and tag it, and it cannot delete or change a photo. You can delete it in Immich whenever you like (the album then says so), and the app deletes it when you stop following your last album.
- **Stop following** ends it; photos already copied stay in your library. If the owner stops sharing the album, the page says "the album is not shared with you any more".
- The person who shared the album is not told. A copy takes as much room as the photo does.

How it works: [the design note](../../contributing/design/library-design.md).
