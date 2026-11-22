# Smart albums that fill themselves

Immich has no album that keeps itself up to date from a rule. The app can: describe what belongs, and each new matching photo is offered to your album.

![The Smart albums card with the form open: the album, what belongs in it, and the switches](../../images/smart-album-form.png)

## Make one

1. In **Settings**, find **Smart albums** and open **Make a smart album**.
2. Choose **the album**: one of your own, or a new one with the name you type (an album of yours with that name is used).
3. Choose **what belongs in it**: the same choices as any rule: people (and people to leave out), dates, a place, an album to look in (or leave out), a **camera** (make and model), **tags** (a photo must have all you pick) and a phrase that needs smart search. Everything you choose must hold.
4. **Take out photos that no longer match** (off by default). Then, each time the rule looks, a photo **the app put in the album** that Immich no longer finds for the rule is taken out of the album (it stays in your library). A photo you added by hand is never touched, and a photo taken out is not offered again. It does not work with a phrase, because a phrase only finds the closest 100 photos.

The camera and the tags can also be chosen in the form of an ordinary rule for a service.

## Then it works like the other rules

- **It asks first.** New matches wait on the Overview as **N new photos for the album …**, with **Put them in the album**, **Not these** and **Always**; the card links to a page where you can look at them one by one. A rule only looks at photos that reach Immich after it was made, and waits for each photo to settle. After you have answered **Always** three times you can let the album **fill itself** with photos that arrive from then on; a rule with a phrase always asks first.
- **Older photos ask first too.** **Look for older photos** offers up to 500 of the photos that match and were in Immich before the rule (or before last week); press it again for the next 500. Photos already in the album are never offered.
- **It only adds,** unless you switched on taking out. It never deletes a photo, renames or deletes an album.

## Good to know

- **A key of your own.** The first time, the app makes a key named `handoff: smart albums` with your own sign-in. It can read your photos and albums, make an album and put photos in it; with taking out switched on it can also take photos out of an album. It cannot change, share or delete an album, or touch a photo. You can delete it in Immich whenever you like (the rule then says so), and the app deletes it when you stop your last smart album.

## If something is off

- **Nothing is offered.** The rule only looks at photos that arrive after it was made (use **Look for older photos**), and each photo must settle first.
- **The rule says its key is gone.** You deleted it in Immich: stop the smart album and make it again.

## What next

- Rules for a service: [Rules](rules.md)
- Albums from folders: [Albums from folders](folder-albums.md)

How it works: [the design note](../../contributing/design/albums-design.md).
