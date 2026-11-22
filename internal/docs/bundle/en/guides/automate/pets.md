# Rules for a pet, and the pet helper

Immich cannot tell animals apart, so a pet has no name to give a rule. Pick three to eight photos of your own of the animal, and a rule offers the photos most like them.

![The Pets card in Settings: what a pet rule is, whether the helper is on, and where to make one](../../images/pets-card.png)

## Make a rule for a pet

1. In **Settings**, under **Pets**, open **Make a rule for a pet** (or on a service's card, under **Make a rule for a pet**).
2. Pick **3 to 8 photos of your own** of the animal from your newest ones. With the pet helper on, each must show the animal (the app says which photo shows none).
3. The rule asks Immich for the photos it finds most like each example and offers those that arrived after the rule was made, with pictures, as suggestions: **Send** or **Not these**. It starts with the ten nearest to each example and learns from you: every photo you send becomes one more example (up to twelve) and widens how far down Immich's list the rule looks.
4. **Also look back** suggests older photos in the same way.

A pet rule **always asks first**, has no *Always* and never sends by itself. It needs Immich's smart search (machine learning) to be on, and says so when it is not. A pet can also be part of [sharing a person's photos](../share/share-people.md).

Be ready for a **noisy start**: Immich gives no score, only an order, so the first batches may hold wrong photos. A pet that looks like another, or a small one beside people, is found badly; a dog that fills the frame is found best.

## The optional pet helper

The pet rule is much more accurate with a small second container, the **pet helper**. It finds the cat or dog in each photo and compares **that animal** with the animal in your examples, so a pet that is small beside people is not lost among them, and about six times fewer wrong photos are offered at the same recall (measured in [the design note](../../contributing/design/pairing-design.md)). It is optional: **without it the rule still works**, with Immich's own search (the form says so). The person who runs the app switches it on once for everybody:

1. In `docker-compose.yml`, remove the `#` from the `pets` service (it is there, commented out). The image is `vfuoglio/immich-handoff-pets`, tagged `nightly` today and `1` from the next release. To build it from this folder instead, remove the `#` from the `pets` lines of `docker-compose.build.yml` and run `docker compose -f docker-compose.yml -f docker-compose.build.yml up -d --build`; the build downloads its models.
2. Add `PET_SIDECAR_URL=http://pets:8090` to your `.env`, and run `docker compose up -d`.
3. Check: the administrator's page has a **Pet helper** card that says *On* when it answers, and `doctor` checks the address too. The pet form then says "This server has the pet helper on".

```yaml
services:
  immich-handoff:
    environment:
      PET_SIDECAR_URL: http://pets:8090
  pets:
    image: vfuoglio/immich-handoff-pets:nightly   # :1 from the next release
    restart: unless-stopped
    mem_limit: 512m
    # nothing outside the app needs to reach it, and it needs no network of its own beyond the app
```

## Good to know

- **What it costs:** an image of about 400 MB, about 110 MB of memory when idle and 200 to 300 MB at work, and about 0.4 seconds a photo on two processors; the pass looks at up to 30 new photos at a time, and a look back at up to 150.
- **What it does with photos:** the app sends it a **preview** of each photo of a person who has a pet rule, never the original; it keeps nothing, writes nothing to disk and calls nobody. Give it no other network access. The pet rule's key gets the permission to look at previews (`asset.view`) through your own sign-in when you make the rule, and loses it again when the helper is switched off and you next open the page.
- **What changes in the rule:** each example must show the animal and the rule's card shows a box round the animal it found in each one, so a photo with two animals is not ambiguous. A photo is offered when the animal in it is alike enough (a cutoff near 0.45 that your answers move: it goes down to what you accepted, and up when you turn a whole batch down).
- **What it does not do:** it cannot tell two look-alike pets apart, a cat is found less well than a dog, and a pet rule still only suggests.

## If something is off

- **"The pet helper is not answering."** The rule falls back to Immich's own search until it is back. Check the container, and run `doctor`.
- **Too many wrong photos.** That is the noisy start: answer **Not these** and the rule moves away from them; the pet helper makes it much better.
- **The form says smart search is off.** Switch on machine learning in your Immich.

## What next

- How rules and suggestions work: [Rules](rules.md)
- Share a pet's photos with someone: [Share a person's photos](../share/share-people.md)
