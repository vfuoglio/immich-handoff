# Stop tagging by hand: rules and the button inside Immich

**Who it is for:** Senders who hand over often.

**What you get:** Rules that suggest photos of named people, places and dates, with one tap to send, and a **Hand over...** button inside Immich's own pages.

**What you see:** Suggestions with pictures on the Overview, and a message with **Undo** in Immich after a press.

![A suggestion on the Overview: three new photos for Family album found by a rule, with Send, Not these and Always](../images/rules-suggestion.png)

![Immich's selection bar with the Hand over button and the list of services](../images/immich-button-selection-list.jpg)

## Rules: photos by who, where and when

Instead of tagging, a **rule** looks in your Immich for photos by **who is in them, where and when**, and asks you before anything is handed over. On the service's card (a service you joined from this page), under **Rules**, press **Make a rule** and choose what to look for. Everything you choose must hold for a photo to match:

- **Photos of** one or more people Immich has **named** in your library (with their faces), and **but not photos with** other people;
- **Taken between** two dates;
- a **place**, a country and optionally a city, picked from the places your own library holds photos from (Immich matches them exactly, and sometimes names a neighbourhood instead of a city);
- **In the album** one of yours, and **but not in the album** some others;
- a **phrase** ("a beach at sunset"), which needs smart search on your Immich server (machine learning). A rule with a phrase always asks you first.

The rule is shown as a sentence ("photos of Nora without Leo taken from 2026-05-03 to 2026-05-09 in Portugal") and can be paused or ended; to change one, end it and make another. A rule looks only at photos that reach Immich **from now on**, never at your old ones. Archived, hidden, locked and trashed photos never match, nor photos that are not yours. Photos wait a few minutes after they arrive in Immich before a rule can offer them, so that Immich has time to find the faces; a rule keeps looking at the past week of new photos, so a face found later still makes the photo appear.

**What a rule does with what it finds.** Every few minutes (`RULES_CHECK_MINUTES`, ten by default) each rule asks your Immich for matching photos among those that arrived in the last week and keeps the ones that really match: yours (not another person's in a shared album), not archived, trashed or hidden, with none of the people or albums you left out, a few minutes old, and not handled by this rule before. Each match is remembered with the reason it matched ("Nora in it, in Portugal"), and a photo is never found twice by the same rule. If something goes wrong (Immich does not accept the app's key, or a phrase is used on a server without smart search) the rule says so. Matches wait for you in a batch, shown on the Overview under **Needs you** as "3 new photos for Family" with small pictures and the rule that found them, with three answers:

- **Send** tags those photos with the service's tag in your Immich, exactly as if you had done it by hand, so everything else follows as usual (daily limits, quiet hours, take-back, the live scene). The tag is the one the service already uses, and it is taken off again when the photo is handed over.
- **Not these** remembers those photos as declined for that rule: they are never suggested by it again, and the rule goes on.
- **Always** sends them and counts. After a few, the rule can be let go on by itself (see below). A rule with a phrase has no **Always**.

**One rule for several services.** In the form, **Make the same rule for these services too** makes a copy of the rule for each other service you sent to, so "photos of Nora" can go to one service and to another. Each copy is a rule of its own, answered on its own (and can be paused, made automatic or ended on its own). **A photo goes to a service once**, however many of your rules for that service find it: when one has sent it, the others no longer offer it and a second yes does not hand it over again.

**Also look back.** A new rule only looks at photos that reach Immich from now on. **Also look back** on a rule's row suggests the **older** photos that match too, up to 200, newest first, as one batch of suggestions like any other: nothing old is sent without your yes, and the button waits until you have answered what is already waiting.

**Letting a rule send by itself.** After you have answered **Always** to a rule three times, its row offers **Send by itself**. From then on it sends, without asking, the photos that reach Immich **after you switched it on**, once they have been there half an hour (so every face has been found and a person you left out has shown), within the service's daily limit and quiet hours; you can still take a hand-over back. What was already waiting stays a suggestion, a line in your Activity says what was sent, and **Ask me first again** turns it back at any time. A rule with a phrase never sends by itself.

A card also has **Look at them one by one**: a page with every photo large, the reason under each and a tick on each. **Send the ticked photos** sends those and leaves the unticked out for good (this rule will not offer them again); there is **Not these** for the whole batch too. When a batch begins, you are told once (in your Activity, and on your devices or by email if you asked for those), with **Send** and **Not these** as one-tap buttons in the message: the link asks first and works once, as the other one-tap links do. A photo that joins a batch later does not tell you again.

Ending a rule never touches photos already handed over, and leaving the service ends its rules. The permissions the rule needs are the ones your key for the service has; for the albums it asks you to give the key **read albums**, through your own sign-in, as when you link an album.

## Following an album shared with you

An album someone shared with you can be **followed**: what they add to it is offered to you, with pictures, to copy into your own library, so photos do not stay only in their account. On the Overview, under **Add to my library**, open **Follow an album shared with me**, choose the album, whether to start with what is in it now or only with what is added from now on, and where the copies go (in your library tagged with who shared it, in your library as it is, or in an album). This is the same copy as the [Add to my library button](../find-handoff.md#add-to-my-library-from-inside-immich).

- **It asks first.** New photos wait on the Overview as **N new photos in Trip, shared by Anna**, with **Add to my library**, **Not these** and **Always**; the card links to a page where you can look at them one by one. After you have answered **Always** three times you can let the album **add by itself**: photos added from then on are copied without asking, and you can turn that off at any time.
- **A key of your own.** The first time, the app makes a key named `handoff: library` in your Immich account, with your own sign-in. It can read what is shared with you and add to your own library and tag it, and it cannot delete or change a photo. You can delete it in Immich whenever you like (the album then says so), and the app deletes it when you stop following your last album.
- **Stop following** ends it; photos already copied stay in your library. If the owner stops sharing the album, the page says "the album is not shared with you any more".
- The person who shared the album is not told. A copy takes as much room as the photo does.

How it works: [the design note](../design/library-design.md).

## Albums from the folders of an external library

If part of your Immich is an **external library** (folders on the server that Immich reads in place), its photos are not in any album. The app can offer an album for each folder, named after it, and put the photos in it. On the Overview, under **Albums from my folders**, open **Make albums from a folder** and fill in:

- **The folder, as Immich knows it**, such as `/external/photos`. Immich lets only its administrator list libraries, so you type it: it is the path Immich shows for one of your photos from the library, up to the folder you want. As you type, the form says how many of your photos it finds under that folder and which albums it would make. Only your own photos count, and `/external/photos` does not also take `/external/photos-old`.
- **Which folder is an album.** By default the folder each photo is in. Or the first, second or third folder below the one you typed, so `/photos/2025-02/paris/a.jpg` goes in `paris`, `2025-02`, or `paris` again. A photo with no folder at that level (directly in the folder you typed, say) has no album and is left alone.
- **The name.** After the folder (`paris`) or after the folder and the one above it (`2025-02 / paris`). With the first, two folders with the same name in different places make **one** album; the second keeps them apart.

Then the rule works like the others:

- **It asks first.** What is there now waits on the Overview as **N photos for M albums from your folders**, with the album names, and **Make the albums**, **Not these** and **Always**; the card links to a page where you can look at the photos one by one, each with its album. After you have answered **Always** three times you can let the rule **make albums by itself** for photos that arrive from then on.
- **It only adds.** An album of yours with that name is used; otherwise it is made. Nothing is ever removed from an album, renamed or deleted. If you rename a folder, the new name makes a new album and the old one stays.
- **A big folder takes its time.** The first look at a folder of hundreds of thousands of photos is spread over several passes (at most 500 new photos each); after that a pass only lists what came to Immich since the last one.
- **A key of your own.** The first time, the app makes a key named `handoff: folders` with your own sign-in. It can read your photos and your albums and make an album and put photos in it, and it cannot change, share or delete an album, or touch a photo. You can delete it in Immich whenever you like (the rule then says so), and the app deletes it when you stop your last folder rule.

How it works, and what Immich does with an external library: [the design note](../design/folders-design.md).

## Handing over from inside Immich

If your administrator has put the Handoff button in Immich's pages ([Make Handoff easy to find](../find-handoff.md#hand-over-from-inside-immich)), you can hand photos over without tags: open a photo, or select several, and press **Hand over...** in the toolbar (or choose **Hand over...** in the three-dot menu). Pick the service; Immich's page then says how many photos were handed over, with **Undo** for a moment. The list shows only services you send to from this page, says whether your original stays or goes to your trash, and warns when photos will wait (a pause, a daily limit). The service you used last is first in the list; **Forget my choice** clears it. A service whose owner decides is listed under **Ask to join**; pressing it asks, and the list then says **Waiting for approval**. Only your own photos can be handed over, up to 200 at once. It does the same as putting the tag on, so everything above about limits, quiet hours and taking back applies.
