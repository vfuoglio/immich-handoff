# Stories: a shareable animated recap

Turn an album into a short animated story and send the link. Whoever gets it watches on a phone or a TV, with no account and nothing to install.

<p align="center"><img src="../../images/story-title.png" alt="A story: the title card, A week in Lisbon, with its dates" width="380"> <img src="../../images/story-photo.png" alt="A photo of the story, full screen, with its date under it" width="380"></p>

Immich has memories for you. A story is the one you can send: a title, a card for each chapter (a month, or a place on a short trip), about forty of the best photos with their date and place, and an end.

## Make one

1. **Choose what it is made from.** In **Settings**, open **Sharing**, find **Stories** and press **Make a story**. Pick an **album** (yours, or one shared with you), a **person's photos**, **everything between two dates** or a **drop box**. Give it a title (or let it borrow the name of the album or person) and decide how long the link lasts: 7, 30 (the default) or 90 days, or until you remove it.

   ![The Stories card with the form open: what to make it from, a title and how long the link lasts](../../images/story-form.png)

2. **Wait a moment.** The card says **Being made** and how far it has got. You can leave the page. In the background the app looks at the photos once, with your own sign-in, skips the dark, the blurred and the near-duplicates, spreads about forty of the best over the time they cover (a busy month does not take every slot) and cuts them into chapters.
3. **Share the link.** When the card says **Ready**, it shows the story's link with **Copy**, **QR code** and **Share** (your phone's own share sheet, or the link copied). Anyone with the link can watch.

   ![A story that is ready: its link, the QR code, and the buttons to edit, pick again or remove it](../../images/story-card.png)

4. **Watch it.** The story opens full screen. **Tap** to go on, tap the **left side** to go back, **swipe**, or **hold** the screen to pause; the arrow keys and the space bar work too. There is no sound. On a TV or a computer, **double-click** for full screen.
5. **Tidy it up, if you like.** **Edit** lets you change the title, rename chapters (an empty name shows the dates), take photos out (never the last one), change how long the link lasts from now, and switch what the page shows. **Pick the photos again** chooses afresh, for example after you added photos to the album.
6. **Play it on a TV.** In **Cast to a TV**, choose **A story of mine** and the TV plays its photos in order, with the chapter, date and place as a caption. See [Cast to a TV](cast-to-tv.md).

## Good to know

- **The link is the only protection.** Anyone who has it can watch until the story ends or you remove it, so share it as you would a drop box link. A story that has ended or been removed looks the same as one that never was.
- **What the page shows.** The **date** and the **city and country** of each photo are on, because that is what makes a recap, and you can switch each off. **Coordinates are never shown.** **Names of people** are off; switch them on in the editor and the page shows the names Immich has given the people in each photo. Faces are never shown apart from their photos. The page never says whose album it is or what the album is called, and search engines do not index it.
- **Previews in chats are off.** Switch **Show a preview in chats** on and a chat service that fetches the link is given the title and the first photo (this needs `PUBLIC_APP_URL`, the address people use to reach the app: see [Settings](../../reference/settings.md)). Anyone who gets the link can do that, so leave it off for a private story.
- **A key of your own for each story.** The app makes a key named `handoff: story <title>` with your own sign-in. It can look at pictures, and because Immich cannot limit a key to one album it could see your whole library, so the app only hands the page the photos in the story, by ids that mean nothing outside it. When a story's end date comes the link stops at once, but its key stays in your Immich until you remove the story, because only your own sign-in can delete it.
- **It is a snapshot.** The photos are chosen when the story is made (or picked again). A photo you delete from Immich disappears from the story; a photo you add does not appear until you pick again.
- **Up to 10 stories,** and a story shows photos, not videos.
- **Remove** deletes the story, stops its link at once and deletes its key.

**Your year, made for you.** In December (and until the middle of January for the year before), if you have at least 30 photos from the year, **Needs you** offers **Make your 2026 story**. **Make my story** makes one of the whole year with a link that lasts 30 days; **Not this year** hides the offer for that year. It is worked out when you open the page, with your own sign-in, so the app needs no extra access.

## If something is off

- **The card says it could not be made.** It gives the reason under the title. Press **Pick the photos again** once it is fixed (for example after the album has photos in it).
- **The link says the story is not available.** It has ended, been removed or never existed, and the page deliberately says no more. Make a new story or ask for a new link.
- **No preview shows in a chat.** Previews are off by default, and need **Show a preview in chats** switched on and `PUBLIC_APP_URL` set.
- **A photo I added is missing.** Press **Pick the photos again**.

## What next

- Show it on the big screen: [Cast to a TV](cast-to-tv.md)
- Make a story of an event: [Collect photos from guests](../collect/events.md)
- Get a message about today's memories: [On this day](memories.md)

How it works: [the design note](../../contributing/design/stories-design.md).
