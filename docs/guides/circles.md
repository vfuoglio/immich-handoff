# Circles: share every new album with a group

**Who it is for:** Someone who shares most albums with the same people (the family, a club) and is tired of adding them one album at a time. Immich has no user groups.

**What you get:** A named group of people on your Immich server. Each new album you make is offered to the group, and with your yes it is shared with them as viewers or editors. After you have said *Always* three times it can share new albums by itself.

## In a few steps

1. In **Settings**, under **Sharing with others**, open **Make a circle**.
2. **Name** the circle, say **who is in it** (below), whether they can **look at the album** or **look at it and add photos**, and, if you like, that only albums **whose name starts with** some text count (for example `Family `).
3. From now on, each album you make **after** the circle waits on the Overview as **N album for the circle …**, with the names of the people, **Share them**, **Not these** and **Always**. The app looks every ten minutes (`RULES_CHECK_MINUTES`), because Immich does not say when an album is made, and it waits a moment first so that you can name the album.
4. After you have answered **Always** three times, **Let it share by itself** shares the albums you make from then on without asking.
5. **Offer my existing albums** offers the albums you made before the circle (that match the name, and the people are not on yet). It only offers: nothing is shared until you say yes.
6. **Add a person** puts someone else in; the albums already shared are not shared with them until you offer the existing ones again. **Take out** takes a person out of the circle **and off the albums the app shared with them**, and only those: a person who was on an album before, or whom you added by hand, stays. **End the circle** stops the circle and leaves every shared album shared.

## Who is in a circle

The administrator chooses, on the administrator's page under **Circles**:

- **They type each person's email address** (the default). The address is the one Immich shows for the account. Nobody else's address is shown.
- **They pick from everyone on this server.** Easier, but every person who makes a circle then sees the name of every account on the server.

## What to know

- **Immich may email people.** When an album is shared with someone, Immich can send them an invitation email, if the server has mail set up and they have not turned it off. A circle shares one album at a time, so a person can get one email for each album. The card and the suggestion say so.
- **The app records what it shared**, album by album and person by person. That is how *Take out* removes only its own sharing, and how a person who has **left** an album is not added again. A person who was on an album already is recorded as such and never touched.
- **A key of your own.** The first time, the app makes a key named `handoff: circles` with your own sign-in. It can read your albums and who they are shared with, and add a person to an album or take one off. It cannot see a photo, change or delete an album, or do anything else. You can delete it in Immich whenever you like (the circle then says so), and the app deletes it when you end your last circle.
- A circle holds up to 20 people, and you can have up to 10.

How it works: [the design note](../design/circles-design.md).
