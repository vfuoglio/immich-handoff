# Smart albums that fill themselves

Immich has no album that keeps itself up to date from a rule. The app can: on the **Settings** view, under **My library**, **Smart albums**, open **Make a smart album** and choose:

- **The album.** One of your own, or a new one with the name you type (an album of yours with that name is used).
- **What belongs in it.** The same choices as any rule: people (and people to leave out), dates, a place, an album to look in (or leave out), a **camera** (make and model), **tags** (a photo must have all you pick) and a phrase that needs smart search. Everything you choose must hold.
- **Take out photos that no longer match** (off by default). Then, each time the rule looks, a photo **the app put in the album** that Immich no longer finds for the rule is taken out of the album (it stays in your library). A photo you added by hand is never touched, and a photo taken out is not offered again. It does not work with a phrase, because a phrase only finds the closest 100 photos.

Then the rule works like the others:

- **It asks first.** New matches wait on the Overview as **N new photos for the album …**, with **Put them in the album**, **Not these** and **Always**; the card links to a page where you can look at them one by one. A rule only looks at photos that reach Immich after it was made, and waits for each photo to settle, like any rule. After you have answered **Always** three times you can let the album **fill itself** with photos that arrive from then on; a rule with a phrase always asks first.
- **Older photos ask first too.** **Look for older photos** offers up to 500 of the photos that match and were in Immich before the rule (or before last week); press it again for the next 500. Photos already in the album are never offered.
- **It only adds,** unless you switched on taking out. It never deletes a photo, renames or deletes an album.
- **A key of your own.** The first time, the app makes a key named `handoff: smart albums` with your own sign-in. It can read your photos and albums, make an album and put photos in it; with taking out switched on it can also take photos out of an album. It cannot change, share or delete an album, or touch a photo. You can delete it in Immich whenever you like (the rule then says so), and the app deletes it when you stop your last smart album.

The camera and the tags can also be chosen in the form of an ordinary rule for a service.

How it works: [the design note](../../contributing/design/albums-design.md).
