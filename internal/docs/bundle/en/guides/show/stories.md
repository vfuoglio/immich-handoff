# Stories: a shareable animated recap

**Who it is for:** Someone who wants to turn a holiday, a year, a person's photos or a party into a short animated recap to send to family and friends, without anyone signing in or installing anything. Immich has memories for you, but nothing you can send.

**What you get:** A **story**: a title, a card for each chapter (a month, or a place on a short trip) with its dates, about forty of the best photos with their date and place, and an end, opened from a link on a phone or a TV with no account. You can edit it, share the link, and play it on a TV with the cast code.

## In a few steps

1. **Make it.** In **Settings**, under **My library**, **Stories**, open **Make a story** and choose what it is made from: an **album** (yours or one shared with you), a **person's photos**, **everything between two dates**, or a **drop box**. Dates are optional for the first three and narrow them. Give it a title (or let it use the name of the album or person) and choose how long the link lasts: 7, 30 (the default) or 90 days, or until you remove it.
2. **Wait a little.** The app looks at the photos once, in the background, with your own sign-in: it leaves out the dark, the blurred and the near-duplicates, spreads about forty of the best over the time they cover (a busy month does not take every slot), and cuts them into chapters. The card says how far it is. You can leave the page.
3. **Share the link.** When the story is **Ready**, the card has its link, with **Copy**, a **QR code** and **Share** (your phone's own share sheet, or the link copied). It opens full screen: **tap** to go on, tap the **left side** to go back, **swipe**, or **hold** the screen to pause; the arrow keys and the space bar work too. There is no sound. A TV or a computer works too: **double-click** for full screen.
4. **Edit it.** **Edit** opens a page where you can change the title, rename the chapters (an empty name shows the dates), take photos out (never the last one), choose how long the link lasts from now, and switch what the page shows. **Pick the photos again** chooses afresh, for example after you added photos to the album.
5. **Play it on a TV.** In **Cast to a TV**, choose **A story of mine**: the TV plays the story's photos in order, with the chapter, date and place as a caption. See [Cast to a TV](cast-to-tv.md).
6. **Remove it.** **Remove** deletes the story, stops its link at once and deletes the key the app made for it.

## What the page shows, and what it does not

- **Dates** and the **city and country** of each photo are on by default, because that is what makes a recap. You can switch each off. **Coordinates are never shown.**
- **Names of people** are **off** by default; switch them on in the editor and the page shows the names Immich has given the people in each photo. Faces are never shown apart from the photos.
- The page never says whose album it is or what the album is called, and it is not indexed by search engines.
- **Previews in chats** are **off** by default. Switch **Show a preview in chats** on and the title and the first photo are given to a chat service that fetches the link (it needs `PUBLIC_APP_URL`, the address people use to reach this app, see [Settings](../../reference/settings.md)). Anyone who gets the link can do that, so leave it off for a private story.

## The end-of-year story

In December (and until the middle of January for the year before), if you have at least 30 photos from the year, **Needs you** offers **Make your 2026 story**. **Make my story** makes one of the whole year, with the link lasting 30 days; **Not this year** hides the suggestion for that year. It is worked out when you open your page, with your own sign-in, so the app needs no extra access.

## What to know

- **The link is the only protection.** Anyone who has it can watch the story until it ends or you remove it. Share it as you would a drop box link. An ended or removed story looks the same as one that never was.
- **A key of your own for each story.** The app makes a key named `handoff: story <title>` with your own sign-in: it can look at pictures, and because Immich cannot limit a key to one album, it could see your whole library, so the app only hands the page the photos in the story, by ids that mean nothing outside it. A story whose end date has come stops at once, but its key stays in your Immich until you remove the story (only your own sign-in can delete it).
- **Up to 10 stories.** A story shows photos, not videos.
- **It is a snapshot.** The photos are chosen when the story is made (or picked again). A photo you delete from Immich disappears from the story; a photo you add does not appear until you pick again.

How it works: [the design note](../../contributing/design/stories-design.md).
