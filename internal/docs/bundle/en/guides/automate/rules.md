# Rules: photos by who, where and when

Instead of tagging, let a rule look in your Immich for the photos you would tag, and ask you before anything is handed over: photos of named people, taken between two dates, in a place, in an album, or looking like a phrase.

![A suggestion on the Overview: three new photos for Family album found by a rule, with Send, Not these and Always](../../images/rules-suggestion.png)

## Make a rule

On the card of a service you joined from this page, under **Rules**, press **Make a rule** and choose what to look for. Everything you choose must hold for a photo to match:

![The form that makes a rule: dates, albums, tags and a phrase (people and places appear once Immich has named them)](../../images/rules-form.png)

- **Photos of** one or more people Immich has **named** in your library (with their faces), and **but not photos with** other people;
- **Taken between** two dates;
- a **place**, a country and optionally a city, picked from the places your own library holds photos from (Immich matches them exactly, and sometimes names a neighbourhood instead of a city);
- **In the album** one of yours, and **but not in the album** some others;
- a **phrase** ("a beach at sunset"), which needs smart search on your Immich server (machine learning). A rule with a phrase always asks you first.

The rule is shown as a sentence ("photos of Nora without Leo taken from 2026-05-03 to 2026-05-09 in Portugal") and can be paused or ended; to change one, end it and make another.

## Answer what it finds

Every few minutes (`RULES_CHECK_MINUTES`, ten by default) each rule asks your Immich for matching photos among those that arrived in the last week and keeps the ones that really match: yours (not another person's in a shared album), not archived, trashed or hidden, with none of the people or albums you left out, a few minutes old, and not handled by this rule before. Each match is remembered with the reason it matched ("Nora in it, in Portugal"), and a photo is never found twice by the same rule. Matches wait for you in a batch, shown on the Overview under **Needs you** as "3 new photos for Family" with small pictures and the rule that found them, with three answers:

- **Send** tags those photos with the service's tag in your Immich, exactly as if you had done it by hand, so everything else follows as usual (daily limits, quiet hours, take-back, the live scene). The tag is the one the service already uses, and it is taken off again when the photo is handed over.
- **Not these** remembers those photos as declined for that rule: they are never suggested by it again, and the rule goes on.
- **Always** sends them and counts. After a few, the rule can be let go on by itself (see below). A rule with a phrase has no **Always**.

A card also has **Look at them one by one**: a page with every photo large, the reason under each and a tick on each. **Send the ticked photos** sends those and leaves the unticked out for good; there is **Not these** for the whole batch too. When a batch begins, you are told once (in your Activity, and on your devices or by email if you asked), with **Send** and **Not these** as one-tap buttons in the message: the link asks first and works once. A photo that joins a batch later does not tell you again.

## Let a rule send by itself

A rule that sends photos of people to a service offers **Send by itself** from the start, with no need to answer **Always** first; the rules that make albums, add photos to your library or stack photos still offer it after three **Always**. Before it turns on, the app says which photos go where and that a wrong photo can reach the service. From then on it sends, without asking, the photos that reach Immich **after you switched it on**, once they have been there half an hour (six times the rule's wait, so every face has been found and a person you left out has shown), within the service's daily limit and quiet hours. You can still take a hand-over back. What was already waiting stays a suggestion, a line in your Activity says what was sent, and **Ask me first again** turns it back at any time. A rule with a phrase never sends by itself.

## Good to know

- **A rule looks only at photos that reach Immich from now on,** never at your old ones, and never at archived, hidden, locked or trashed photos, nor photos that are not yours. **Also look back** on a rule's row suggests the **older** photos that match too, up to 200, newest first, as one batch like any other: nothing old is sent without your yes, and the button waits until you have answered what is already waiting.
- **How long a rule waits.** Immich finds the faces in a photo a little after it arrives and never says when it has finished, so a rule waits before it offers a photo: five minutes by default (`RULES_SETTLE_MINUTES`). When you make a rule you can choose from 1 to 60 minutes. A shorter wait shows new photos sooner, but a person found late may be missing when the photo is offered, or a person you left out may not have shown yet.
- **One rule for several services.** In the form, **Make the same rule for these services too** makes a copy of the rule for each other service you send to. Each copy is a rule of its own, answered on its own. **A photo goes to a service once**, however many of your rules for that service find it.
- **Ending a rule never touches photos already handed over,** and leaving the service ends its rules. The permissions the rule needs are the ones your key for the service has; for the albums it asks you to give the key **read albums**, through your own sign-in, as when you link an album.
- **A rule for a pet** works differently: see [Rules for a pet](pets.md).
- If something goes wrong (Immich does not accept the app's key, or a phrase is used on a server without smart search) the rule says so.

## If something is off

- **Nothing is suggested.** The wait has not passed yet, the people are not named in Immich, or the photos arrived before the rule was made (use **Also look back**).
- **The rule says Immich does not accept the key.** The key was deleted or lost a permission: leave and join again, or open the page so it fixes the permission.
- **A phrase finds nothing.** The server has no smart search (machine learning) switched on; the rule says so.
- **A photo went where I did not want.** Take the hand-over back: see [Control what is handed over](controls.md#taking-back-the-last-hand-over).

## What next

- Photos of your cat or dog: [Rules for a pet](pets.md)
- Hand over from a button inside Immich: [the button](immich-button.md)
- Stop sending, limit or pause: [Control what is handed over](controls.md)
