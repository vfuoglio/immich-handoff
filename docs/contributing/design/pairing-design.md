---
title: "Sharing a person's photos with another person, both ways"
audience: contributor
---

# Sharing a person's photos with another person, both ways

> *For contributors: this is a design note, not a user guide. The guides are in [the documentation index](../../README.md).*

**Decided** in #215, with the pet part in #227 and #228. A user of Immich told us what keeps them on Google Photos: a shared album that fills itself from face recognition, in both directions. Whenever one partner photographs the dog, or the other partner, the photo appears in the other's library without anyone remembering to share it. This note says what Handoff adds, what was measured to decide it, and how the build is split.

## Conclusion

**Go, in two parts.**

1. **A pairing for people.** One step makes a private two-member pool and a rule "photos of this person" for each side, grouped by a small record so that the pieces start, pause and stop together. The transfer is the one that exists (the sender's key tags, the receiver's pass copies): **no second hand-over path**.
2. **A pet rule, in two strengths.** The default needs nothing new: the person says "this is my pet" by picking a few example photos, and the rule finds look-alikes through Immich's own search and learns from the answers. An **optional sidecar container**, switched on by one environment variable, makes it much more accurate. Pet rules only ever suggest.

Immich's own face detection cannot do pets, so this is the only way to get close.

## What was checked

Immich 3.4.1 with its machine-learning container, on throwaway stacks; public-domain photos of four well-known pets (alone and with people), ten other dogs and eight cats, then a library of 3,077 photos (3,000 everyday photos plus 77 labelled ones, which include about 230 photos of other animals) for the cutoffs.

| Question | Result |
|---|---|
| Can Immich's own face detector see pets? | **No.** At the default threshold it found no animal in 18 animal-only photos (every detection was a person). At the lowest threshold it boxed 15 of the 18, but of 49 sampled crops about 6 were a real animal face (the rest leaves, fur, dirt and a collar) and nearly everything fell into one cluster. People in the same photos clustered fine. |
| Can the app tell that Immich has finished reading faces? | **No signal a person's key can read.** `GET /queues/<name>` answers 403 to a normal user even with `queue.read`: it is for the administrator. An asset says `hasMetadata`, never "faces done". So the delay stays a fixed wait, made a per-rule setting. |
| Can a key ask for "photos like this one"? | **Yes.** `POST /search/smart` with `queryAssetId` works with a key that has only `asset.read`, and `createdAfter` works with it. It returns the **whole library ranked, with no score**; the only distance setting is the administrator's. |
| How good is that for a pet? | Average precision 0.67 over four cases (0.79 for a dog that fills the frame, 0.25 for a small dog beside people, 0.51 for a cat among people, 1.0 for a burst of one cat). Two look-alike dogs are not told apart. |
| Which rank cutoff? | **It depends on how many photos the pet has, far more than on the size of the library.** A pet with 11 photos: top 20 gave recall 0.93 at 5.6 wrong photos per 1000. A pet with 34: top 20 gave 0.73, top 30 gave 0.86 (5.1 wrong per 1000), top 50 gave 0.93 (13.3 per 1000). A cutoff learned from answers (start at 10, then 1.5 times the worst rank the person accepted) reached recall 0.84 after five batches with 7.7 wrong per 1000 in total. |
| Can a small sidecar do better? | **Yes.** A detector (YOLOX-s) finds the animal and a small embedding model (DINOv2-small, int8) fingerprints the crop; both Apache-2.0, ONNX Runtime only. Mean average precision 0.80 (small dog beside people 0.25 to 0.71, cat among people 0.51 to 0.97). On the 3,077-photo library **one cutoff of about 0.4 to 0.5 worked for every pet**: recall 0.75 to 1.0 with 0.1 to 3.8 wrong photos per 1000 (the cat among people is the weak case: 11 wrong per 1000 at 0.4), and it does not depend on how many photos the pet has. At equal recall it offered about six times fewer wrong photos than the Immich search. |
| What does the sidecar cost? | Image **357 MB** (python slim 118 MB, about 180 MB of libraries, 60 MB of models baked in), memory **106 MB idle and 200 to 300 MB working**, about **370 ms per photo on 2 CPUs**. Handoff itself is a 25 MB image, so the sidecar is a second, separate, optional image. |
| A pet-specific model? | A DINOv2 fine-tuned on pet identity scored **worse** (0.65): it expects tight face crops and there is no permissively licensed pet-face detector to feed it. |
| What is still not solved? | Look-alike pets (a second black curly dog lands in the first one's results 70 to 98% of the time); a pet the detector misses (about 4 photos in 48, a dog lying under a person); a family library, where the same people and rooms are in every photo, which the test library does not copy. |

## What we are up against

Google Photos has two features that overlap, from its help pages and the press:

- **Partner sharing:** one partner only; all photos, or only photos of chosen people; one-way until the partner sets up their own; it depends on face grouping, which Google says is not available in all countries.
- **Live albums:** an album that adds photos of chosen people and pets by itself and can be shared; pets need face grouping and "Show pets with people"; reported caps of 10,000 photos (private) and 20,000 (shared); an automatic addition reaches a shared album at once.

No measured accuracy for its pet recognition could be found. Google said when it launched that two pets of the same breed can be confused and the owner must sort them, which is what we measured too. Recent comparisons say Google leads Immich on face accuracy and speed and on two-way collaborative albums.

**Where we are better:** more than two people (a pool), both directions in one step, no regional limit, no photo cap, and the photos stay on the person's own server. **Where we are not equal:** pets (Google has them natively), speed (30 to 60 minutes by design; Google's delay is not documented) and polish. So pets are offered as suggestions with a one-tap yes, never promised as "it just appears".

## Design

### The pairing
A small record, `pairing`, holds the two people, the pool's service and the two rules (one per side, one person list each). It is made in one step by the person who starts it.

1. **Offer.** The person picks who from their own library goes to the other, and the other person (a user of the server who has joined, as today). The app makes a private two-member pool (by invitation, no end) and the person's own rule.
2. **Consent.** The other person gets the invitation link or QR code, opens it, and sees plainly that photos of the named people go **both ways**. They pick which of **their own** named people go back (a pet, as 3 to 8 example photos, may go instead of or with people; none at all is allowed: the person who says yes may only receive, and then has no rule of their own; the person who offers sends at least one) (face names are per Immich account: the person named in one library is named again in the other's), say yes, and join the pool. Their rule is made with their own key. Nothing is made for them before they say yes.
3. **The card.** Both see one card, "You and Sam: Anna and Rex", with pause, stop, look-back (older photos as suggestions, never sent without a yes) and the automatic switch below.
4. **Locked pieces.** The pool and the rules carry the pairing's id and **cannot be edited or deleted from Services or Rules**; the card is the only place. That is what keeps a pairing from being left half broken (one side deleting its rule and the pool still open).
5. **Stop.** Either person can stop it, and sending stops at once. **Every copy stays** where it is, as with leaving a pool today, and nothing is ever deleted from anyone's library. What is taken apart at that moment depends on who stops it, because a key in Immich can only be deleted by its owner's own sign-in:
   - **The starter stops it:** the pool, its key and workflow, both sides' rules and the record go at once. The other person sees the service as removed and cleans up their own key with the usual *Clean up*.
   - **The other person stops it (or declines, or lets the invitation lapse):** their own key, workflow and rules go at once, the pairing is marked stopped with the reason, and the starter's pool and rules are paused. They wait for the starter's next visit, where one press of *Stop* takes their side apart.

History: only photos that arrive from now on; the look-back button is the one way to older ones, always as suggestions.

### Speed
A rule looks every 10 minutes and a photo waits 5 minutes before it is offered, and six times that before it goes by itself, so that its faces have been read. That wait protects against sending a photo before a second person in it is known. Without a signal from Immich the wait becomes **a per-rule choice**: the default stays as it is, and a people rule may choose a shorter one, with the trade-off said plainly (a face found late means a photo may be offered without an "except" person having been seen; every pass looks again at the week's photos, so a late face still makes the photo appear, not vanish). Where Immich's Workflows offer a trigger on a person being found in a photo, it **wakes a pass at once** the way the tag workflow already does, instead of waiting for the timer; Workflows alone cannot make a copy in another person's library, so they do not replace the pairing.

### Automatic sending
For a rule with no phrase and no pet, the person may switch **"send these by themselves from now on"** on at once, naming who they go to, instead of answering *Always* three times. It covers only photos that arrive after the switch, once they have settled, within the service's limits and quiet hours, and they can be taken back for 24 hours like any hand-over. It can be switched off at any time, from the card or the rule.

This changes a promise in [the security notes](../../reference/security.md) (the paragraph on rules): *a rule never sends on its own account*. The new text says a rule sends by itself **only after the person has switched it on for a named person, can be switched off at any time, and that a rule with a phrase or a pet never sends by itself**. What it protects against is a wrong face match sending a private photo to someone else; the switch, the settle wait, the take-back and the limits are what stay.

### Pets
- **The rule.** One rule kind, "photos like these". The person picks **3 to 8 example photos** of the pet, sees the first batch of suggestions with pictures, and answers *Send* or *Not these*. Each *Send* becomes a new example.
- **Default, no new part.** The app asks Immich `queryAssetId` for each example, with `createdAfter`, and offers a photo that is in the top *m* of the library for at least one example. *m* starts at 10 and grows from the answers (1.5 times the worst rank of an accepted photo), because Immich gives no score. It starts noisy and improves with answers; the first batches say so. The key needs no permission beyond the one it has for people rules.
- **Optional sidecar.** `PET_SIDECAR_URL` (the name to be settled in the build) points at a second container. It is **stateless**: `POST /analyze` with a photo's bytes returns every cat or dog found, with its box and a 384-number fingerprint; Handoff keeps the fingerprints of the examples and compares them (one cosine), with a default cutoff near 0.45 that the answers move. Handoff sends a **preview, never the original**, so the pet rule's key also gets `asset.view`. The card shows the animal's box on each example so a photo with two animals is not ambiguous. The sidecar sees the rule person's photos and keeps nothing; the security notes say so.
- **Never automatic.** A pet rule only suggests, in both strengths. Immich cannot name pets, the sidecar cannot separate look-alikes, and a wrong offer is a private photo in someone else's library.

### Disk
Each library holds its own copy, because Immich's API gives no other way to make a photo someone's own. The docs say so plainly, with a rule of thumb (1,000 photos of 4 MB is about 4 GB in each library) and that Immich's quotas apply to each person.

## The test

On the end-to-end stack with two people and named faces made by hand (`POST /faces`, as `TestRules` does), because the stack runs without machine learning:

- **`TestPairing` (hub group).** A starts a pairing for "Anna" with B; B accepts and picks their own person. A photo of Anna in A's library reaches B's, tagged `from/<A>`, and B's photo of their person reaches A's. A photo of someone else does not move. Stopping removes the pool and both rules at once and leaves every copy. Editing or deleting a locked piece from Services or Rules is refused. The automatic switch sends a photo that arrives after it and not one that was there before.
- **Unit tests** for the pairing record, the locked-piece checks, the learned rank cutoff (against a stub of Immich's ranking) and the sidecar client (against a stub sidecar).
- **Smart search needs machine learning**, which the shared stack does not run, so the default pet tier is tested against the stub and by hand on a stack that has it; the sidecar has its own CI job that builds the image and posts the sample photos to it.

## Risks, plainly

- **Wrong faces and wrong pets.** Face grouping is imperfect and a pet match more so; that is why a pet never goes by itself and a person's rule waits for the photo to settle.
- **A noisy start for the default pet rule.** The first batches may hold many wrong photos until the cutoff has learned; the sidecar fixes most of it.
- **Cats and look-alikes** are weaker than one dog among others, in both strengths.
- **Two copies of every photo** use disk twice.
- **A pairing needs both people to have joined from the page** (the app holds a key for each), and the consent step is the only thing that makes the other person's side.
- **Immich's Workflows may grow** and cover part of this; what they do not do is the copy into another person's library.

## Go

Go for the pairing, the per-rule wait, the explicit automatic switch for people rules, the default pet rule, and the optional sidecar as a second image. The build is split in the issues under #215, in this order: #229 (the record, the pool and both rules, locked pieces), #230 (the consent page and the card), #231 (the per-rule wait and Workflows), #232 (the automatic switch and the security note), #233 (guide, disk wording, languages and the end-to-end test), #234 (the pet rule, default tier) and #235 (the optional sidecar). #227 and #228 hold the spikes and their numbers.
