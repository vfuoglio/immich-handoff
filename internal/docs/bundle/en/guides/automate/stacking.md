# Stacking photos that belong together

A camera that shoots RAW and JPEG leaves two files of each shot, an editor leaves an edit next to its original, and a burst leaves many frames: Immich shows each as a photo of its own, unless they are **stacked**. The app can offer to stack them. On the Overview, under **Stack photos that belong together**, open **Stack photos by a rule** and choose:

- **A RAW and its JPEG.** Two files with the same name in one folder (`IMG_0042.CR2` and `IMG_0042.JPG`) taken at the same moment.
- **An edit and its original.** A file named like another with `-edited`, `_edit` or a number in brackets (`IMG_0042-edited.jpg`, `IMG_0042 (1).jpg`), taken at the same moment. A RAW, its JPEG and an edit are one stack.
- **A burst.** Three or more shots from one camera, each a moment after the last. This is a guess, so it **always asks first**, whatever you answer.
- **The cover.** The JPEG (the default), the RAW, or the edit.
- **A folder** (optional), to stack only what is in it; empty means your whole library. As you choose, the form says how many of your photos it looked at and what it found.

Then the rule works like the others:

- **It asks first.** The groups wait on the Overview as **N groups of photos to stack**, with one photo of each and **Stack them**, **Not these** and **Always**; the card links to a page showing every photo of every group, where you can untick a group. After you have answered **Always** three times you can let the rule **stack by itself**: pairs and edits that arrive from then on are stacked without asking; bursts still ask. A rule that only stacks bursts cannot be let go on by itself.
- **It only stacks.** It never changes or deletes a photo. A photo that is already in a stack is left alone, and a group with a photo in a stack is skipped, never merged.
- **Undo.** For 15 minutes after the app has made stacks, the rule's row offers **Undo**: it takes apart the stacks the app made, and only those (the photos stay, and are not offered again). A stack you changed yourself in the meantime is left as it is.
- **A late RAW.** A RAW that comes an hour after its JPEG is still paired with it. A big library is looked at in passes of at most 500 photos; later passes look only at what is new.
- **A key of your own.** The first time, the app makes a key named `handoff: stacks` with your own sign-in. It can search your photos, make a stack, read it and take it apart, and it cannot delete or change a photo. You can delete it in Immich whenever you like (the rule then says so), and the app deletes it when you stop your last stacking rule.

Immich's phone app already stacks edits made in other apps when they are uploaded (since 3.3.0); this covers what that does not: a camera's RAW and JPEG, bursts, and edits already in the library. How it works: [the design note](../../contributing/design/stacks-design.md).
