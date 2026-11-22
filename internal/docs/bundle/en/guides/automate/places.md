# Places for photos that have none

A camera with no GPS leaves no place on its photos, so they are missing from the map and from searches by place. This offers a photo with no place the place of a photo taken minutes from it, by the same camera on the same day, and writes nothing until you say yes.

![The Places card in Settings: the button that offers places to photos that have none](../../images/places-card.png)

## Turn it on

1. In **Settings**, under **Photos**, **Places for photos that have none**, press **Offer places to my photos that have none**. The app makes a key named `handoff: places` in your Immich (it can read your photos and write a place; you can see it and delete it there).
2. Photos that reach Immich after that, and have no place, are looked at every ten minutes. The ones that qualify wait on the **Overview** as **N photos can get a place from the photos next to them**, with **Give them these places**, **Not these** and **Always**. The card links to a page that shows each photo next to the photo its place comes from, the place's name, why it matched, and a **Show on a map** link.
3. **Look for older photos** offers the photos you already had that qualify, up to what one look reads.
4. After you have answered **Always** three times you can let the app write a place **by itself**, but only for a photo with a photo that has a place on **both sides**; a photo with a neighbour on one side only always asks, whatever you chose. Only photos that arrive after the switch are handled this way.

## When a photo qualifies

- The **same camera** (make and model, and the lens when both photos name one), on the **same day** and, when both say, in the **same time zone**.
- A photo with a place **within 15 minutes on both sides**, whose two places are no more than **2 km** apart and at a walking or driving speed (60 km/h at most) from each other, so a flight or a train never matches; or a photo with a place **within 10 minutes on one side**. The place is the nearer one's.
- Never a photo that has a place already, or that is in the trash or archived, or is not yours (a partner's, or one in an album shared with you), or is a video, or has no camera or no time of shooting recorded.
- The app waits a few minutes after a photo arrives (the same wait as the other rules, `RULES_SETTLE_MINUTES`) so Immich has finished reading it.

## Good to know

- **A written place cannot be taken back by the app.** Immich 3.4.1 refuses to clear a place through its programming interface (a null is rejected), so there is no undo. That is why the first answer is always asked for, why the buttons ask once more, and why the review page shows the source of every place. If a place is wrong, remove it in Immich (the photo's information panel).
- **Photos of an external library may not keep a place.** Immich does not change the files of an external library, and on Immich 3.4.1 with read-only files a place written into such a photo is accepted and then not kept. The app notices on its next look and says so on the card (**The last look found a problem**); it does not offer that photo again. A place is kept for photos uploaded to Immich.
- **Immich finds the city and the country itself**, a little after the place is written; it may name a neighbourhood instead of a city.
- **The map link** goes to OpenStreetMap and loads nothing until you click it: the app shows no map and sends no coordinates anywhere by itself.
- **The key can do more than the app does.** Immich's `asset.update` permission also allows changing a photo's description, rating or date; the app only writes a place, and you can delete the key at any time.

## If something is off

- **Nothing is offered.** Only photos that reached Immich after you turned it on are looked at, and only if a photo with a place was taken within the windows above; **Look for older photos** offers the ones you already had.
- **The card says "The last look found a problem."** A photo of an external library may not keep a place: see above. It is not offered again.
- **A place is wrong.** Remove it in Immich (the photo's information panel): the app cannot take it back.

## What next

- Rules for people, places and dates: [Rules](rules.md)
- See your albums as folders: [Album tree](../show/album-tree.md)

How it works: [the design note](../../contributing/design/places-design.md).
