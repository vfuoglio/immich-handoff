# What is carried over

A hand-over copies the photo's file and a little more. The rule of thumb: **what is written inside the file travels; what Immich keeps only in its own database does not.**

Everything below was checked against a real Immich 3.2.0 by `test/e2e/carryover_test.go`, which builds each kind of photo in the sender's account, hands it over, and compares the receiver's copy with the original. Run `test/run.sh` and look at the "WHAT IS CARRIED OVER" report to see it again, for example against a newer Immich.

## What arrives

| | |
|---|---|
| **The file itself** | Byte for byte. It is compared by checksum before anything is removed from the sender. |
| **The file name** | Kept. |
| **The dates** | The file's created and modified dates are kept. A date the sender *changed in Immich* also arrived, as the date of the copy, in the test (for a photo with no date inside the file; a photo that has its own date inside the file was not tested with a changed date). |
| **The favourite mark** | Kept. |
| **Whatever is written inside the file** | Description, rating, location and the date taken, when they are in the file's own EXIF data: the receiver's Immich reads them from the file again. |
| **Videos** | Arrive as videos, and Immich works out the length again. |

## What does not arrive

| | What the receiver gets |
|---|---|
| **A description, rating or location added in Immich** | Nothing. Immich keeps these in its database, not in the file, so they are not in the copy. (If the same details are written inside the file, they do arrive; see above.) |
| **Albums** | The copy is in none of the sender's albums. If the service has an album, it goes into that one; otherwise it gets the inbox tag. |
| **Tags** | Only the inbox tag, which the app adds. The sender's other tags are not copied. |
| **An archived photo** | It arrives in the receiver's timeline. The archived state is not carried. |
| **Edits made in Immich** (rotate, crop, mirror) | The original, unedited photo. Immich keeps edits as instructions next to the file, so only the file itself moves. |
| **A live photo's motion** | Handed over with the still. The motion clip is a separate hidden video in Immich; it is uploaded to the receiver first, hidden, and the still is linked to it, so the receiver's copy plays. With **move**, the still and its clip go to the sender's trash together. If the clip cannot be read, only the still is handed over. If the receiver already had the still from an earlier hand-over without its clip, that copy is left as it was and the sender's clip is not removed. |
| **A stack** | Only the photo that was tagged, on its own. The receiver's copy is not in a stack, and photos that were not tagged are not handed over. |

## Not checked

- **People.** The receiver's own Immich runs face recognition on the copy. Names the sender gave are not carried.
- **Very large videos.** A transfer has no total time limit: it can take as long as it likes while data keeps moving, and is given up only after three minutes in which not a single byte moved (it is then retried at the next pass, and the original is kept). That was tested with a deliberately slow stream, not with a real multi-gigabyte file on a slow link.
- **Sidecar files and photos in a locked folder** were not tried.

Anything you need on the other side that is not in this list, such as a description, is best written inside the file before sending, or told to the receiver.
