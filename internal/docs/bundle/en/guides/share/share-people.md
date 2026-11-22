# Share a person's photos with your partner, both ways

One offer, one yes. From then on the photos of the people you choose go to your partner, and theirs come to you, tagged `from/<name>`, offered first with pictures.

![The card of a sharing in Settings: who sends what, Pause, Stop, Send by themselves and Suggest older photos](../../images/pairing-card.png)

This is for two people on the **same Immich server** who want the photos of the people they care about (a child, a parent, each other) to reach the other's library without anyone remembering to share.

## Set it up

1. **Offer.** In **Settings**, under **Share photos with someone**, give the other person's email (as Immich knows it) and tick the people **of your own library** whose photos go to them. Optionally choose how long a rule waits before it offers a photo.
2. **Give them the link.** The card shows a link and a QR code. Nothing is made for the other person until they say yes, and an offer lapses after 7 days.
3. **They say yes.** They open the link, read what it means (photos of the named people go both ways, only photos that arrive from then on, every copy stays if it ends) and tick which of **their own** people go back to you, if any. Saying yes asks nothing in return: they may send nothing.
4. **Photos are offered.** Each of you sees *Needs you* suggestions with pictures of the other person's photos to be sent: **Send**, **Not these** or **Always**. **Suggest older photos** looks back, again as suggestions.
5. **Send by themselves, if you like.** On your side of the card, this sends without asking, only photos that arrive afterwards and have settled. The confirmation says a wrong photo can reach the other person; you can switch it off at any time, and each of you chooses for your own side.
6. **Pause or stop** from the card, either of you. Sending stops at once.

## Good to know

- **Face names are per account.** The person you call "Anna" in your library is named separately in theirs, which is why each of you picks your own people. A wrong face match could send a wrong photo, so photos wait before they are offered: 5 minutes by default, 1 to 60 as you choose (a rule that sends by itself waits six times as long). Immich does not say when it has finished finding faces, so this wait is the only protection.
- **Each library keeps its own copy.** Immich gives no way to make a photo someone's own other than copying it, so shared photos use disk in **both** libraries and count against each person's quota. As a rule of thumb, 1,000 photos of 4 MB is about 4 GB in each.
- **When it stops, every copy stays** and nothing is deleted from anyone's library. If the person who offered it stops it, everything is taken apart at once and the other person cleans up their own key with **Clean up**. If the other person stops it, sending stops at once and the one who offered presses **Stop** on their next visit.
- **A pet can be part of it.** Under **Also share photos of a pet**, pick 3 to 8 of your own photos of the animal; the person who says yes can do the same. Immich cannot tell animals apart, so this is a [rule for a pet](../automate/pets.md): it offers the photos most like your examples, **always asks first** (no *Always*, and the sharing's switch to send by itself leaves it alone) and starts noisy. The optional [pet helper](../automate/pets.md#the-optional-pet-helper) makes it much more accurate. The person who offers sends at least a person or a pet.
- **Limits.** At most five sharings at once for a person, and at most ten people in one.
- **The pieces are locked.** The private pool and the two rules of a sharing do not show in Services or Rules and cannot be changed there; the card is the only place, so a sharing is never left half broken.
- **Slower than a partner share in a photo app, on purpose.** Photos are offered after a wait of minutes, not at once, and a pet is found by a rule that suggests photos you answer, not by a name. In return it works for any two people, both ways in one step, with no regional limit and no photo cap, and the photos stay on your own server.

## If something is off

- **Nothing is offered.** The wait has not passed yet, or the people you chose have no photos arriving after the offer. A rule only looks at photos that reach Immich after the offer; **Suggest older photos** looks back.
- **The other person does not see the offer.** Check that the email you typed is the one Immich shows for their account, and that they open the link signed in to Immich.
- **I stopped it and want it back.** Make a new offer; copies already made stay where they are.

## What next

- How rules and suggestions work: [Rules](../automate/rules.md)
- Pet photos: [Rules for a pet](../automate/pets.md)
- Everybody sends and receives on a trip: [Pool a trip](trip-pool.md)

Details: [Security](../../reference/security.md#sharing-photos-with-another-person) and the [design note](../../contributing/design/pairing-design.md).
