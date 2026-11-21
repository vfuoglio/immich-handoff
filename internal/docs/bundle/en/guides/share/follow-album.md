# Following an album shared with you

When someone shares an album with you, the photos stay in their account. Follow the album, and what they add is offered to you, with pictures, to copy into your own library.

![The Add to my library card: follow an album shared with me](../../images/follow-album-card.png)

## Follow one

1. Open **Settings**, find **Add to my library** and press **Follow an album shared with me**.
2. **Choose the album**, whether to start with **what is in it now** or only with **what is added from now on**, and **where the copies go**: in your library tagged with who shared it, in your library as it is, or in an album.
3. **Answer what is offered.** New photos wait on the Overview as **N new photos in Trip, shared by Anna**, with **Add to my library**, **Not these** and **Always**; the card links to a page where you can look at them one by one.
4. **Let it add by itself, if you like.** After you have answered **Always** three times you can let the album **add by itself**: photos added from then on are copied without asking, and you can turn that off at any time.

This is the same copy as the [Add to my library button](../../admin/find-handoff.md#add-to-my-library-from-inside-immich) in Immich, with the photo's description, rating, location and a live photo's clip.

## Good to know

- **A key of your own.** The first time, the app makes a key named `handoff: library` in your Immich account with your own sign-in. It can read what is shared with you and add to your own library and tag it; it cannot delete or change a photo. You can delete it in Immich whenever you like (the album then says so), and the app deletes it when you stop following your last album.
- **The person who shared the album is not told.** A copy takes as much room as the photo does.
- **Stop following** ends it; photos already copied stay in your library.

## If something is off

- **"The album is not shared with you any more."** The owner stopped sharing it. Nothing more is offered; what you copied stays.
- **Nothing is offered.** Photos are offered when the app next looks at the album, and only the ones added after you began if you chose "from now on".

## What next

- Copy single photos from inside Immich: [the Add to my library button](../../admin/find-handoff.md#add-to-my-library-from-inside-immich)
- Share your own albums with a group: [Circles](circles.md)

How it works: [the design note](../../contributing/design/library-design.md).
