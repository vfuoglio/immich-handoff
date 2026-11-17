# The daily "On this day" message

**Built** in 1.4.0 (#199, built in #217, #218 and #219). How to use it: [A daily On this day message](../guides/memories.md). Immich asks for the same thing in discussions 3981 (push notifications for On this day) and 2836 (a memories showcase); this is the answer from outside, using only the public API.

## What Immich offers

- `GET /memories?for=YYYY-MM-DD` (permission `memory.read`) lists the memories for a day, each with its `assets`, `data.year`, `memoryAt`, `seenAt` and `type` (`on_this_day`). The filter takes a plain date: a timestamp is refused.
- Probed on Immich 3.4.1: **a key limited to `memory.read` and `asset.view` can list the memories and fetch thumbnails**. So the app needs a small key of its own for this, not a session, and not any of the permissions of the other keys.
- `POST /memories` (`memory.create`) makes one. The end-to-end test uses it to have a memory on any day.
- `seenAt` is set when the person has watched the memory in Immich; the message is skipped for a memory that has it.

## The design

- **A key per person**, made with their own session when they turn it on (`handoff: memories`, `memory.read` and `asset.view`), stored sealed in `memory_prefs` with their settings, and deleted when they turn it off. The administrator never has to choose anything: it exists only for a person who asked.
- **A pass** every ten minutes (`MEMORIES_CHECK_MINUTES`). For each person: their day is the day in their time zone; nothing happens before their time of day or after the day was done. It asks Immich for the day's memories with their key; drops the ones with `seenAt`, with no photo or not from an earlier year; and sends one message. A day with nothing to send is done at the first look. A day that failed (Immich unwell, the only channel down, a person with push chosen but no device) is not done, so the next pass tries again. A key Immich refuses (401) is marked broken, the person is told once, and nothing more is asked with it.
- **One message a day**, the years listed oldest first ("3 years ago, 5 years ago"), not one per year. The text is stored packed so each reader sees it in their own language.
- **Channels per person: notification and email.** ntfy and Home Assistant are the administrator's destinations, not a person's; a person's private memories are never sent to them. A notification carries the text and two buttons (open the memory, send to someone). The email carries the same two links and up to three thumbnails, the first of each year, **inside the mail** (a `multipart/related` part with `Content-ID`, so a mail program shows them without loading anything from the internet).
- **Activity.** One line per message ("On this day: ...") in the person's own Activity, written without a second notification.
- **Send to someone.** `/memories/<day>` lists the day's photos with the person's own session and hands the ticked ones to a service through the existing `HandOver` (the photos must be theirs; the tag goes on with the key they made when they joined). The photo thumbnails of that page are fetched with their own session and only for photos in that day's memories.

## What it costs

One request per person per pass in which their time has come and the day is not done, and three thumbnails when the email has pictures. After the day is done, nothing.

## Not done, on purpose

- A weekly summary, and one message per year: the questions chose one daily message.
- Memories of other kinds than on this day (trips, people): Immich's `type` could carry them later.
- Sending a person's memories to the administrator's ntfy or Home Assistant.
