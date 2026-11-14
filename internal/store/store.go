// Package store keeps the app's own state in one SQLite file: which people have joined which service.
// API keys are encrypted with the vault before they reach the disk.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite" // pure-Go driver, no CGO

	"github.com/vfuoglio/immich-handoff/internal/vault"
)

const (
	RoleSender   = "sender"
	RoleReceiver = "receiver"

	StateActive   = "active"
	StateOrphaned = "orphaned" // the service was removed by its owner; the member can still clean up
	StateBroken   = "broken"   // Immich rejected the key (it was deleted or revoked)

	VisListed = "listed" // shown to everyone signed in to Immich
	VisInvite = "invite" // reachable only through the owner's invitation link

	PolicyApproval = "approval"
	PolicyOpen     = "open"

	StatusPending  = "pending"
	StatusApproved = "approved"
	StatusDeclined = "declined"
)

// ErrExists is returned when a person already holds that role in that service.
var ErrExists = errors.New("store: already joined")

// ErrNotFound is returned when the membership or service asked about does not exist.
var ErrNotFound = errors.New("store: not found")

type Store struct {
	db    *sql.DB
	vault *vault.Vault
}

// Link is one person's membership in a service, with the key the app uses on their behalf.
type Link struct {
	ID           string
	Service      string
	Role         string
	ImmichUserID uuid.UUID
	Name, Email  string
	KeyID        uuid.UUID // the key's id in Immich, so it can be deleted later
	Key          string    // decrypted; never stored in clear
	State        string
	Note         string    // why the link is not active
	Mode         string    // "copy" keeps the sender's originals, "move" trashes them; chosen by the sender
	AlbumID      uuid.UUID // an album of the sender's whose photos are all handed over; zero: none
	AlbumName    string    // its name when it was linked, for display
	Paused       bool      // the sender paused: their tagged photos wait, tagged, until they resume
	CreatedAt    time.Time
}

// migrations run in order; the number applied is kept in PRAGMA user_version.
var migrations = []string{
	`CREATE TABLE meta (k TEXT PRIMARY KEY, v BLOB NOT NULL);
	 CREATE TABLE links (
	   id             TEXT PRIMARY KEY,
	   service        TEXT NOT NULL,
	   role           TEXT NOT NULL CHECK (role IN ('sender','receiver')),
	   immich_user_id TEXT NOT NULL,
	   name           TEXT NOT NULL,
	   email          TEXT NOT NULL,
	   key_id         TEXT NOT NULL,
	   key_enc        BLOB NOT NULL,
	   state          TEXT NOT NULL DEFAULT 'active',
	   created_at     TEXT NOT NULL,
	   UNIQUE (service, role, immich_user_id)
	 );`,
	`CREATE TABLE services (
	   id          TEXT PRIMARY KEY,
	   name        TEXT NOT NULL UNIQUE,
	   tag         TEXT NOT NULL,
	   inbox_tag   TEXT NOT NULL,
	   refused_tag TEXT NOT NULL,
	   expiry_days INTEGER NOT NULL,
	   owner_id    TEXT NOT NULL,
	   owner_name  TEXT NOT NULL,
	   owner_email TEXT NOT NULL,
	   key_id      TEXT NOT NULL,
	   key_enc     BLOB NOT NULL,
	   policy      TEXT NOT NULL CHECK (policy IN ('approval','open')),
	   created_at  TEXT NOT NULL
	 );
	 CREATE TABLE requests (
	   id         TEXT PRIMARY KEY,
	   service    TEXT NOT NULL,
	   user_id    TEXT NOT NULL,
	   name       TEXT NOT NULL,
	   email      TEXT NOT NULL,
	   status     TEXT NOT NULL CHECK (status IN ('pending','approved','declined')),
	   created_at TEXT NOT NULL,
	   UNIQUE (service, user_id)
	 );`,
	`ALTER TABLE links ADD COLUMN note TEXT NOT NULL DEFAULT '';
	 ALTER TABLE services ADD COLUMN state TEXT NOT NULL DEFAULT 'active';
	 ALTER TABLE services ADD COLUMN note TEXT NOT NULL DEFAULT '';
	 CREATE TABLE events (
	   id      INTEGER PRIMARY KEY AUTOINCREMENT,
	   at      TEXT NOT NULL,
	   service TEXT NOT NULL,
	   user_id TEXT NOT NULL,
	   kind    TEXT NOT NULL,
	   text    TEXT NOT NULL
	 );`,
	`ALTER TABLE services ADD COLUMN visibility TEXT NOT NULL DEFAULT 'listed';
	 ALTER TABLE services ADD COLUMN invite TEXT NOT NULL DEFAULT '';`,
	`ALTER TABLE services ADD COLUMN mode TEXT NOT NULL DEFAULT 'move';
	 ALTER TABLE services ADD COLUMN sent_tag TEXT NOT NULL DEFAULT '';`,
	// The sender, not the service owner, decides what happens to their originals. Existing members keep
	// what they had: the mode of their service, or move for services from the configuration.
	`ALTER TABLE links ADD COLUMN mode TEXT NOT NULL DEFAULT 'move';
	 UPDATE links SET mode = COALESCE((SELECT mode FROM services WHERE services.name = links.service), 'move');`,
	// A service may name an album that handed-over photos are put in.
	`ALTER TABLE services ADD COLUMN album TEXT NOT NULL DEFAULT '';`,
	// A sender may link one of their own albums to a service: everything in it is handed over.
	`ALTER TABLE links ADD COLUMN album_id TEXT NOT NULL DEFAULT '';
	 ALTER TABLE links ADD COLUMN album_name TEXT NOT NULL DEFAULT '';`,
	// A sender or an owner may pause; an owner may cap what each sender hands over in a day. handovers records
	// what each sender handed over (for the cap), and only the last few days of it are kept.
	`ALTER TABLE links ADD COLUMN paused INTEGER NOT NULL DEFAULT 0;
	 ALTER TABLE services ADD COLUMN paused INTEGER NOT NULL DEFAULT 0;
	 ALTER TABLE services ADD COLUMN limit_photos INTEGER NOT NULL DEFAULT 0;
	 ALTER TABLE services ADD COLUMN limit_mb INTEGER NOT NULL DEFAULT 0;
	 CREATE TABLE handovers (
	   link_id TEXT NOT NULL,
	   at      TEXT NOT NULL,
	   bytes   INTEGER NOT NULL
	 );
	 CREATE INDEX handovers_link ON handovers (link_id, at);`,
	// The first-run guide on the Overview is shown until a person has finished or hidden it.
	`CREATE TABLE guide (
	   user_id TEXT PRIMARY KEY,
	   done_at TEXT NOT NULL
	 );`,
	// An owner may set quiet hours (minutes after midnight; from = to means none) and what the service accepts.
	`ALTER TABLE services ADD COLUMN quiet_from INTEGER NOT NULL DEFAULT 0;
	 ALTER TABLE services ADD COLUMN quiet_to INTEGER NOT NULL DEFAULT 0;
	 ALTER TABLE services ADD COLUMN quiet_large_mb INTEGER NOT NULL DEFAULT 0;
	 ALTER TABLE services ADD COLUMN accept_rating INTEGER NOT NULL DEFAULT 0;
	 ALTER TABLE services ADD COLUMN accept_photos INTEGER NOT NULL DEFAULT 0;
	 ALTER TABLE services ADD COLUMN accept_orient TEXT NOT NULL DEFAULT '';
	 ALTER TABLE services ADD COLUMN accept_max_mb INTEGER NOT NULL DEFAULT 0;`,
	// An owner may let the copies that arrive expire (lifetime in days; 0: they stay). item rows remember which copy
	// came from which photo, so a sender can take back their last hand-over.
	`ALTER TABLE services ADD COLUMN lifetime_days INTEGER NOT NULL DEFAULT 0;
	 CREATE TABLE handover_items (
	   id             INTEGER PRIMARY KEY AUTOINCREMENT,
	   link_id        TEXT NOT NULL,
	   batch          TEXT NOT NULL,
	   sender_asset   TEXT NOT NULL,
	   receiver_asset TEXT NOT NULL,
	   clip           INTEGER NOT NULL DEFAULT 0,
	   at             TEXT NOT NULL,
	   taken_back     INTEGER NOT NULL DEFAULT 0
	 );
	 CREATE INDEX handover_items_link ON handover_items (link_id, at);`,
	// A drop box is a link through which people without an Immich account send photos to its owner. Its key is
	// the owner's, made with the owner's own session, and sealed like every other key. The log keeps what happened.
	`CREATE TABLE dropboxes (
	   id          TEXT PRIMARY KEY,
	   token       TEXT NOT NULL UNIQUE,
	   name        TEXT NOT NULL,
	   message     TEXT NOT NULL DEFAULT '',
	   owner_id    TEXT NOT NULL,
	   owner_name  TEXT NOT NULL,
	   key_id      TEXT NOT NULL,
	   key_enc     BLOB NOT NULL,
	   inbox_tag   TEXT NOT NULL,
	   expires_at  TEXT NOT NULL DEFAULT '',
	   max_files   INTEGER NOT NULL DEFAULT 0,
	   max_mb      INTEGER NOT NULL DEFAULT 0,
	   max_file_mb INTEGER NOT NULL DEFAULT 0,
	   photos_only INTEGER NOT NULL DEFAULT 0,
	   password    TEXT NOT NULL DEFAULT '',
	   state       TEXT NOT NULL DEFAULT 'active',
	   note        TEXT NOT NULL DEFAULT '',
	   files       INTEGER NOT NULL DEFAULT 0,
	   bytes       INTEGER NOT NULL DEFAULT 0,
	   created_at  TEXT NOT NULL,
	   ended_at    TEXT NOT NULL DEFAULT ''
	 );
	 CREATE TABLE dropbox_events (
	   id     INTEGER PRIMARY KEY AUTOINCREMENT,
	   box_id TEXT NOT NULL,
	   at     TEXT NOT NULL,
	   kind   TEXT NOT NULL,
	   text   TEXT NOT NULL,
	   who    TEXT NOT NULL DEFAULT ''
	 );
	 CREATE INDEX dropbox_events_box ON dropbox_events (box_id, id);`,
	// Drop boxes grow: filing on arrival (an album, a tag of their own), approval before anything is stored, a start,
	// co-hosts, and what a summary needs (files per hour, which guests came).
	`ALTER TABLE dropboxes ADD COLUMN album TEXT NOT NULL DEFAULT '';
	 ALTER TABLE dropboxes ADD COLUMN extra_tag TEXT NOT NULL DEFAULT '';
	 ALTER TABLE dropboxes ADD COLUMN approve INTEGER NOT NULL DEFAULT 0;
	 ALTER TABLE dropboxes ADD COLUMN starts_at TEXT NOT NULL DEFAULT '';
	 ALTER TABLE dropboxes ADD COLUMN notified INTEGER NOT NULL DEFAULT 0;
	 CREATE TABLE dropbox_hosts (
	   box_id   TEXT NOT NULL,
	   user_id  TEXT NOT NULL,
	   name     TEXT NOT NULL,
	   email    TEXT NOT NULL,
	   role     TEXT NOT NULL,
	   added_at TEXT NOT NULL,
	   PRIMARY KEY (box_id, user_id)
	 );
	 CREATE TABLE dropbox_pending (
	   id     TEXT PRIMARY KEY,
	   box_id TEXT NOT NULL,
	   name   TEXT NOT NULL,
	   size   INTEGER NOT NULL,
	   kind   TEXT NOT NULL,
	   who    TEXT NOT NULL,
	   at     TEXT NOT NULL,
	   path   TEXT NOT NULL
	 );
	 CREATE INDEX dropbox_pending_box ON dropbox_pending (box_id, at);
	 CREATE TABLE dropbox_hours (
	   box_id TEXT NOT NULL,
	   hour   TEXT NOT NULL,
	   files  INTEGER NOT NULL,
	   PRIMARY KEY (box_id, hour)
	 );
	 CREATE TABLE dropbox_guests (
	   box_id TEXT NOT NULL,
	   who    TEXT NOT NULL,
	   PRIMARY KEY (box_id, who)
	 );`,
	// Drop boxes: a short link, the look of the guest page (colour, language, cover), a size photos are shrunk to in
	// the guest's browser, guests deleting what they sent, saved templates, and what each guest sent.
	`ALTER TABLE dropboxes ADD COLUMN short TEXT NOT NULL DEFAULT '';
	 ALTER TABLE dropboxes ADD COLUMN accent TEXT NOT NULL DEFAULT '';
	 ALTER TABLE dropboxes ADD COLUMN lang TEXT NOT NULL DEFAULT '';
	 ALTER TABLE dropboxes ADD COLUMN max_dim INTEGER NOT NULL DEFAULT 0;
	 ALTER TABLE dropboxes ADD COLUMN guest_delete INTEGER NOT NULL DEFAULT 0;
	 ALTER TABLE dropboxes ADD COLUMN removed INTEGER NOT NULL DEFAULT 0;
	 CREATE UNIQUE INDEX dropboxes_short ON dropboxes (short) WHERE short <> '';
	 CREATE TABLE dropbox_covers (
	   box_id TEXT PRIMARY KEY,
	   kind   TEXT NOT NULL,
	   data   BLOB NOT NULL
	 );
	 CREATE TABLE dropbox_templates (
	   id         TEXT PRIMARY KEY,
	   owner_id   TEXT NOT NULL,
	   name       TEXT NOT NULL,
	   data       TEXT NOT NULL,
	   created_at TEXT NOT NULL
	 );
	 CREATE TABLE dropbox_uploads (
	   id         TEXT PRIMARY KEY,
	   box_id     TEXT NOT NULL,
	   guest      TEXT NOT NULL,
	   asset_id   TEXT NOT NULL,
	   pending_id TEXT NOT NULL,
	   name       TEXT NOT NULL,
	   size       INTEGER NOT NULL,
	   at         TEXT NOT NULL
	 );
	 CREATE INDEX dropbox_uploads_guest ON dropbox_uploads (box_id, guest);`,
	// Drop boxes: a shot list (each prompt is a tag), a guest book of messages and voice notes, and sharing the result
	// back with the guests as an Immich shared link.
	`ALTER TABLE dropboxes ADD COLUMN prompts TEXT NOT NULL DEFAULT '[]';
	 ALTER TABLE dropboxes ADD COLUMN prompt_required INTEGER NOT NULL DEFAULT 0;
	 ALTER TABLE dropboxes ADD COLUMN guest_book INTEGER NOT NULL DEFAULT 0;
	 ALTER TABLE dropbox_pending ADD COLUMN prompt TEXT NOT NULL DEFAULT '';
	 ALTER TABLE dropbox_uploads ADD COLUMN prompt TEXT NOT NULL DEFAULT '';
	 CREATE TABLE dropbox_prompt_counts (
	   box_id TEXT NOT NULL,
	   prompt TEXT NOT NULL,
	   files  INTEGER NOT NULL,
	   PRIMARY KEY (box_id, prompt)
	 );
	 CREATE TABLE dropbox_messages (
	   id         TEXT PRIMARY KEY,
	   box_id     TEXT NOT NULL,
	   who        TEXT NOT NULL,
	   name       TEXT NOT NULL,
	   text       TEXT NOT NULL,
	   audio      BLOB,
	   audio_kind TEXT NOT NULL DEFAULT '',
	   at         TEXT NOT NULL
	 );
	 CREATE INDEX dropbox_messages_box ON dropbox_messages (box_id, at);
	 CREATE TABLE dropbox_shares (
	   box_id     TEXT PRIMARY KEY,
	   link_id    TEXT NOT NULL,
	   key        TEXT NOT NULL,
	   message    TEXT NOT NULL,
	   expires_at TEXT NOT NULL,
	   created_at TEXT NOT NULL
	 );`,
	// Drop boxes: the photos each box kept (for a wall, a best-of and a hand-over), a wall link, a best-of with its
	// reasons, and a service a box's photos are handed on to.
	`ALTER TABLE dropboxes ADD COLUMN wall TEXT NOT NULL DEFAULT '';
	 ALTER TABLE dropboxes ADD COLUMN onward_service TEXT NOT NULL DEFAULT '';
	 ALTER TABLE dropboxes ADD COLUMN onward_auto INTEGER NOT NULL DEFAULT 0;
	 ALTER TABLE dropbox_messages ADD COLUMN on_wall INTEGER NOT NULL DEFAULT 0;
	 CREATE UNIQUE INDEX dropboxes_wall ON dropboxes (wall) WHERE wall <> '';
	 CREATE TABLE dropbox_assets (
	   id       INTEGER PRIMARY KEY AUTOINCREMENT,
	   box_id   TEXT NOT NULL,
	   asset_id TEXT NOT NULL,
	   prompt   TEXT NOT NULL DEFAULT '',
	   at       TEXT NOT NULL,
	   UNIQUE (box_id, asset_id)
	 );
	 CREATE TABLE dropbox_highlights (
	   box_id     TEXT PRIMARY KEY,
	   strictness TEXT NOT NULL,
	   status     TEXT NOT NULL,
	   total      INTEGER NOT NULL,
	   done       INTEGER NOT NULL,
	   kept       INTEGER NOT NULL,
	   album      TEXT NOT NULL,
	   note       TEXT NOT NULL,
	   started_at TEXT NOT NULL,
	   finished_at TEXT NOT NULL
	 );
	 CREATE TABLE dropbox_verdicts (
	   box_id   TEXT NOT NULL,
	   asset_id TEXT NOT NULL,
	   verdict  TEXT NOT NULL,
	   detail   TEXT NOT NULL,
	   name     TEXT NOT NULL,
	   PRIMARY KEY (box_id, asset_id)
	 );
	 CREATE TABLE dropbox_onward (
	   id      INTEGER PRIMARY KEY AUTOINCREMENT,
	   box_id  TEXT NOT NULL,
	   at      TEXT NOT NULL,
	   service TEXT NOT NULL,
	   scope   TEXT NOT NULL,
	   count   INTEGER NOT NULL,
	   by_name TEXT NOT NULL
	 );`,
	// a receiver on another Immich server (a bridge): one per service, its key sealed like every other key
	`CREATE TABLE bridges (
	   service      TEXT PRIMARY KEY,
	   address      TEXT NOT NULL,
	   key_enc      BLOB NOT NULL,
	   display_name TEXT NOT NULL,
	   state        TEXT NOT NULL DEFAULT 'ok' CHECK (state IN ('ok','unreachable','revoked')),
	   note         TEXT NOT NULL DEFAULT '',
	   version      TEXT NOT NULL DEFAULT '',
	   checked_at   TEXT NOT NULL DEFAULT '',
	   failures     INTEGER NOT NULL DEFAULT 0,
	   retry_at     TEXT NOT NULL DEFAULT '',
	   created_at   TEXT NOT NULL
	 );`,
	// the link an owner sends to someone on another server; one open at a time per service
	`CREATE TABLE bridge_invites (
	   token      TEXT PRIMARY KEY,
	   service    TEXT NOT NULL,
	   created_at TEXT NOT NULL
	 );`,
	// A hand-over may also reach a receiver on another Immich server (a bridge): its copy is a row too, marked remote,
	// so a sender who takes the hand-over back reaches it if the receiver allowed it.
	`ALTER TABLE handover_items ADD COLUMN remote INTEGER NOT NULL DEFAULT 0;`,
	// A person who asked to be told by email what happens to their services. The address is the one Immich shows for
	// their account, taken when they turned it on and at each later sign-in.
	`CREATE TABLE person_mail (
	   user_id    TEXT PRIMARY KEY,
	   name       TEXT NOT NULL,
	   email      TEXT NOT NULL,
	   lang       TEXT NOT NULL,
	   mode       TEXT NOT NULL,
	   created_at TEXT NOT NULL,
	   last_sent  TEXT NOT NULL DEFAULT ''
	 );`,
	// A browser or phone that a person allowed to push notifications to. The address of the push service and the two
	// keys that encrypt for that browser are sealed together; the id (a hash of the address) lets the page tell which
	// device is "this one" without ever showing the address.
	`CREATE TABLE push_subs (
	   id         TEXT PRIMARY KEY,
	   user_id    TEXT NOT NULL,
	   sealed     BLOB NOT NULL,
	   lang       TEXT NOT NULL,
	   mode       TEXT NOT NULL,
	   label      TEXT NOT NULL,
	   created_at TEXT NOT NULL,
	   last_ok    TEXT NOT NULL DEFAULT '',
	   failures   INTEGER NOT NULL DEFAULT 0
	 );
	 CREATE INDEX push_subs_user ON push_subs (user_id);`,
	// One-tap actions offered in a mail or a notification (approve a request, resume a service). The token is the
	// whole credential: random, valid for a day, usable once; links made for one decision share a group, so using one
	// ends the others.
	`CREATE TABLE action_links (
	   token      TEXT PRIMARY KEY,
	   user_id    TEXT NOT NULL,
	   user_name  TEXT NOT NULL,
	   kind       TEXT NOT NULL,
	   service    TEXT NOT NULL,
	   target     TEXT NOT NULL DEFAULT '',
	   grp        TEXT NOT NULL DEFAULT '',
	   created_at TEXT NOT NULL,
	   expires_at TEXT NOT NULL,
	   used_at    TEXT NOT NULL DEFAULT ''
	 );
	 CREATE INDEX action_links_grp ON action_links (grp);`,
	// A weekly digest of what arrived in a service: the last one made, and the permissions the digest added to the
	// service's receiver key (taken off again when it is turned off).
	`CREATE TABLE digests (
	   service    TEXT PRIMARY KEY,
	   enabled    INTEGER NOT NULL DEFAULT 0,
	   added      TEXT NOT NULL DEFAULT '',
	   week       TEXT NOT NULL DEFAULT '',
	   made_at    TEXT NOT NULL DEFAULT '',
	   album      TEXT NOT NULL DEFAULT '',
	   link_id    TEXT NOT NULL DEFAULT '',
	   link_key   TEXT NOT NULL DEFAULT '',
	   link_ends  TEXT NOT NULL DEFAULT '',
	   photos     INTEGER NOT NULL DEFAULT 0,
	   people     INTEGER NOT NULL DEFAULT 0,
	   left_out   INTEGER NOT NULL DEFAULT 0,
	   left_json  TEXT NOT NULL DEFAULT '',
	   note       TEXT NOT NULL DEFAULT ''
	 );`,
	// A frame: a link that shows an album full screen on a device with no account. Its key can only look at pictures
	// and read albums, and is sealed like every other.
	`CREATE TABLE frames (
	   id         TEXT PRIMARY KEY,
	   token      TEXT NOT NULL UNIQUE,
	   owner_id   TEXT NOT NULL,
	   owner_name TEXT NOT NULL,
	   name       TEXT NOT NULL,
	   album_id   TEXT NOT NULL,
	   album_name TEXT NOT NULL,
	   key_id     TEXT NOT NULL,
	   key_enc    BLOB NOT NULL,
	   hold       INTEGER NOT NULL DEFAULT 15,
	   ends_at    TEXT NOT NULL DEFAULT '',
	   state      TEXT NOT NULL DEFAULT 'active',
	   created_at TEXT NOT NULL
	 );
	 CREATE INDEX frames_owner ON frames (owner_id);`,
	// A drop box can have a mail address: photos mailed to it arrive like uploads. The code is the part of the address
	// after the plus sign.
	`ALTER TABLE dropboxes ADD COLUMN mail_code TEXT NOT NULL DEFAULT '';
	 CREATE UNIQUE INDEX dropboxes_mail_code ON dropboxes (mail_code) WHERE mail_code <> '';`,
	// An event pool is a service where every member sends and receives, for a time. ends_at is when it stops (empty: until
	// the owner ends it).
	`ALTER TABLE services ADD COLUMN pool INTEGER NOT NULL DEFAULT 0;
	 ALTER TABLE services ADD COLUMN ends_at TEXT NOT NULL DEFAULT '';`,
	// What a drop box's password guards: the upload (the page can be seen) or the whole page. Boxes that had a password keep guarding the upload.
	`ALTER TABLE dropboxes ADD COLUMN password_scope TEXT NOT NULL DEFAULT 'upload';`,
	// How long after a box has ended what guests left with it (their names, their hearts, the gallery) is kept.
	`ALTER TABLE dropboxes ADD COLUMN keep_days INTEGER NOT NULL DEFAULT 0;`,
	// The gallery of a drop box: guests may look at what the box kept, heart it, and, if the owner allows, download it
	// and see who sent it. The name a guest gave comes with the photo; a heart is one guest's, kept as a hash.
	`ALTER TABLE dropboxes ADD COLUMN gallery INTEGER NOT NULL DEFAULT 0;
	 ALTER TABLE dropboxes ADD COLUMN gallery_download INTEGER NOT NULL DEFAULT 0;
	 ALTER TABLE dropboxes ADD COLUMN gallery_names INTEGER NOT NULL DEFAULT 0;
	 ALTER TABLE dropbox_assets ADD COLUMN guest_name TEXT NOT NULL DEFAULT '';
	 ALTER TABLE dropbox_pending ADD COLUMN guest_name TEXT NOT NULL DEFAULT '';
	 CREATE TABLE dropbox_hearts (
	   box_id   TEXT NOT NULL,
	   asset_id INTEGER NOT NULL,
	   guest    TEXT NOT NULL,
	   at       TEXT NOT NULL,
	   PRIMARY KEY (box_id, asset_id, guest)
	 );`,
	// A moderator link: a page where a helper with no account approves or rejects what waits in a box.
	`ALTER TABLE dropboxes ADD COLUMN mod_token TEXT NOT NULL DEFAULT '';
	 CREATE UNIQUE INDEX dropboxes_mod ON dropboxes (mod_token) WHERE mod_token <> '';`,
	// The host's picks among the photos a box kept: one can be pinned (always in a best-of) or hidden (from the guests'
	// gallery, the wall and the guests' ZIP, and left out of a best-of).
	`ALTER TABLE dropbox_assets ADD COLUMN hidden INTEGER NOT NULL DEFAULT 0;
	 ALTER TABLE dropbox_assets ADD COLUMN pinned INTEGER NOT NULL DEFAULT 0;`,
	// What makes a box's guest pages the host's own: a title (the box's name stays the internal one, which is also its
	// tag) and a logo.
	`ALTER TABLE dropboxes ADD COLUMN title TEXT NOT NULL DEFAULT '';
	 CREATE TABLE dropbox_logos (
	   box_id TEXT PRIMARY KEY,
	   kind   TEXT NOT NULL,
	   data   BLOB NOT NULL
	 );`,
	// A client room: a link for a client to look at a photographer's album through marked pictures, choose some and, once the
	// photographer releases them, take the originals. Its key is sealed like every other. Which photos are in it is not kept: it is
	// the album as it is read (see internal/hub/room.go), like a frame.
	`CREATE TABLE rooms (
	   id         TEXT PRIMARY KEY,
	   token      TEXT NOT NULL UNIQUE,
	   owner_id   TEXT NOT NULL,
	   owner_name TEXT NOT NULL,
	   name       TEXT NOT NULL,
	   title      TEXT NOT NULL DEFAULT '',
	   album_id   TEXT NOT NULL,
	   album_name TEXT NOT NULL,
	   key_id     TEXT NOT NULL,
	   key_enc    BLOB NOT NULL,
	   password   TEXT NOT NULL DEFAULT '',
	   ends_at    TEXT NOT NULL DEFAULT '',
	   state      TEXT NOT NULL DEFAULT 'active',
	   created_at TEXT NOT NULL
	 );`,
	// How a room marks its pictures: text repeated over them, the logo in a corner, or both; and the logo itself.
	`ALTER TABLE rooms ADD COLUMN wm_mode TEXT NOT NULL DEFAULT 'text';
	 ALTER TABLE rooms ADD COLUMN mark_text TEXT NOT NULL DEFAULT '';
	 ALTER TABLE rooms ADD COLUMN logo_kind TEXT NOT NULL DEFAULT '';
	 ALTER TABLE rooms ADD COLUMN logo BLOB;`,
	// Choosing in a client room: who chooses (one selection for everyone, one for each visitor, or one named client), how many
	// photos at most, and the picks. A pick is a photo (by its opaque id in the room), the round, and whose selection it is in
	// (who is empty for the shared selection and the named client's); the name is shown to the photographer. A selection is
	// finished with Done.
	`ALTER TABLE rooms ADD COLUMN pick_mode TEXT NOT NULL DEFAULT 'shared';
	 ALTER TABLE rooms ADD COLUMN pick_limit INTEGER NOT NULL DEFAULT 0;
	 ALTER TABLE rooms ADD COLUMN claim_who TEXT NOT NULL DEFAULT '';
	 ALTER TABLE rooms ADD COLUMN claim_name TEXT NOT NULL DEFAULT '';
	 CREATE TABLE room_picks (
	   room_id TEXT NOT NULL,
	   round   INTEGER NOT NULL,
	   who     TEXT NOT NULL,
	   photo   TEXT NOT NULL,
	   name    TEXT NOT NULL DEFAULT '',
	   at      TEXT NOT NULL,
	   PRIMARY KEY (room_id, round, who, photo)
	 );
	 CREATE TABLE room_done (
	   room_id TEXT NOT NULL,
	   round   INTEGER NOT NULL,
	   who     TEXT NOT NULL,
	   name    TEXT NOT NULL DEFAULT '',
	   at      TEXT NOT NULL,
	   PRIMARY KEY (room_id, round, who)
	 );`,
	// Releasing a client room's originals: by hand (the photographer's own session adds the key's download permission) or
	// automatically when a client sends a choice; until when they can be downloaded; how many ZIPs were made.
	`ALTER TABLE rooms ADD COLUMN auto_release INTEGER NOT NULL DEFAULT 0;
	 ALTER TABLE rooms ADD COLUMN release_days INTEGER NOT NULL DEFAULT 30;
	 ALTER TABLE rooms ADD COLUMN released_at TEXT NOT NULL DEFAULT '';
	 ALTER TABLE rooms ADD COLUMN release_until TEXT NOT NULL DEFAULT '';
	 ALTER TABLE rooms ADD COLUMN downloads INTEGER NOT NULL DEFAULT 0;`,
	// Notes on the photos of a client room: what a client writes to the photographer about a photo (one for each photo in a
	// selection) and, in a room that allows replies, the photographer's answer.
	`ALTER TABLE rooms ADD COLUMN notes_mode TEXT NOT NULL DEFAULT 'none';
	 CREATE TABLE room_notes (
	   room_id    TEXT NOT NULL,
	   round      INTEGER NOT NULL,
	   who        TEXT NOT NULL,
	   photo      TEXT NOT NULL,
	   name       TEXT NOT NULL DEFAULT '',
	   body       TEXT NOT NULL,
	   reply      TEXT NOT NULL DEFAULT '',
	   at         TEXT NOT NULL,
	   replied_at TEXT NOT NULL DEFAULT '',
	   PRIMARY KEY (room_id, round, who, photo)
	 );`,
	// Rounds in a client room: whether the room goes through several, and which round it is in. A round's picks, notes and sent
	// choices are kept under its number, so earlier rounds stay as history.
	`ALTER TABLE rooms ADD COLUMN multi_round INTEGER NOT NULL DEFAULT 0;
	 ALTER TABLE rooms ADD COLUMN round INTEGER NOT NULL DEFAULT 1;`,
	// Peers: the other apps this one shares albums with (docs/shared-albums-design.md). A peer is made by an invitation; its
	// secret (the one both apps sign with) is sealed like every key. their_id is the id the other app knows this app by.
	`CREATE TABLE peers (
	   id         TEXT PRIMARY KEY,
	   owner_id   TEXT NOT NULL,
	   owner_name TEXT NOT NULL,
	   name       TEXT NOT NULL,
	   address    TEXT NOT NULL,
	   secret_enc BLOB NOT NULL,
	   their_id   TEXT NOT NULL DEFAULT '',
	   role       TEXT NOT NULL,
	   state      TEXT NOT NULL DEFAULT 'active',
	   i_paused   INTEGER NOT NULL DEFAULT 0,
	   they_paused INTEGER NOT NULL DEFAULT 0,
	   version    INTEGER NOT NULL DEFAULT 1,
	   seen_at    TEXT NOT NULL DEFAULT '',
	   problem    TEXT NOT NULL DEFAULT '',
	   created_at TEXT NOT NULL
	 );
	 CREATE TABLE peer_invites (
	   token_hash TEXT PRIMARY KEY,
	   owner_id   TEXT NOT NULL,
	   owner_name TEXT NOT NULL,
	   expires_at TEXT NOT NULL,
	   used_by    TEXT NOT NULL DEFAULT ''
	 );`,
	// A shared album: an album of this person's that is kept the same as an album of the person on the other server (a peer).
	// It starts as a proposal (from either side), becomes active when the other accepts. The app's key for this person's Immich is
	// sealed like every key. history is what this side agreed to share when joining ("all" what is in the album, or "new" only
	// what is added from now on), deletion is the rule both agreed ("nothing" travels, or "follow": removal from the album). The
	// ledger remembers, by checksum, what is in the album and where it came from, so nothing is sent twice or sent back.
	`CREATE TABLE shared_albums (
	   id          TEXT PRIMARY KEY,
	   owner_id    TEXT NOT NULL,
	   peer_id     TEXT NOT NULL,
	   their_id    TEXT NOT NULL DEFAULT '',
	   name        TEXT NOT NULL,
	   album_id    TEXT NOT NULL DEFAULT '',
	   album_name  TEXT NOT NULL DEFAULT '',
	   key_id      TEXT NOT NULL DEFAULT '',
	   key_enc     BLOB,
	   state       TEXT NOT NULL,
	   proposer    INTEGER NOT NULL DEFAULT 0,
	   history     TEXT NOT NULL DEFAULT 'new',
	   their_history TEXT NOT NULL DEFAULT 'new',
	   deletion    TEXT NOT NULL DEFAULT 'nothing',
	   created_at  TEXT NOT NULL
	 );
	 CREATE TABLE shared_ledger (
	   album_id TEXT NOT NULL,
	   checksum TEXT NOT NULL,
	   asset_id TEXT NOT NULL DEFAULT '',
	   origin   TEXT NOT NULL,
	   sent     INTEGER NOT NULL DEFAULT 0,
	   at       TEXT NOT NULL,
	   PRIMARY KEY (album_id, checksum)
	 );`,
	// How a shared album is doing: when it was last looked at, what went wrong, and how many photos went each way.
	`ALTER TABLE shared_albums ADD COLUMN last_sync TEXT NOT NULL DEFAULT '';
	 ALTER TABLE shared_albums ADD COLUMN problem TEXT NOT NULL DEFAULT '';
	 ALTER TABLE shared_albums ADD COLUMN sent INTEGER NOT NULL DEFAULT 0;
	 ALTER TABLE shared_albums ADD COLUMN received INTEGER NOT NULL DEFAULT 0;`,
	// Taking photos out of a shared album (the "follow" rule): the ledger notes whether a photo is in the album now, and a large
	// removal waits for the person to say yes (pending holds its checksums).
	`ALTER TABLE shared_ledger ADD COLUMN present INTEGER NOT NULL DEFAULT 1;
	 ALTER TABLE shared_albums ADD COLUMN pending TEXT NOT NULL DEFAULT '';`,
}

// Open opens (creating if needed) the database at path and checks that v is the key it was made with.
func Open(path string, v *vault.Vault) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // a handful of rows; one connection avoids lock contention entirely
	s := &Store{db: db, vault: v}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.checkKey(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Backup writes a consistent copy of the whole database to dest, which must not exist, while the app keeps
// using it. The keys in it stay encrypted with the store key. The copy is readable by its owner only.
func (s *Store) Backup(ctx context.Context, dest string) error {
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("store: %s already exists", dest)
	}
	// VACUUM INTO does not take parameters for the file name, so it is quoted as an SQL string
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO '`+strings.ReplaceAll(dest, `'`, `''`)+`'`); err != nil {
		os.Remove(dest)
		return fmt.Errorf("store: backup: %w", err)
	}
	return os.Chmod(dest, 0o600)
}

// Check runs SQLite's own integrity check and reports what it found when it is not "ok".
func (s *Store) Check(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA integrity_check`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var problems []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return err
		}
		if line != "ok" {
			problems = append(problems, line)
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("store: the file is damaged: %s", strings.Join(problems, "; "))
	}
	return rows.Err()
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	var have int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&have); err != nil {
		return err
	}
	if have > len(migrations) {
		return fmt.Errorf("store: database is from a newer version (schema %d, this build knows %d)", have, len(migrations))
	}
	for i := have; i < len(migrations); i++ {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("store: migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// checkKey seals a known value on first use and verifies it afterwards, so starting with the wrong
// store key fails loudly instead of silently producing undecryptable rows.
func (s *Store) checkKey() error {
	var sealed []byte
	err := s.db.QueryRow(`SELECT v FROM meta WHERE k = 'canary'`).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		sealed, err = s.vault.Seal([]byte("immich-handoff"), "canary")
		if err != nil {
			return err
		}
		_, err = s.db.Exec(`INSERT INTO meta (k, v) VALUES ('canary', ?)`, sealed)
		return err
	}
	if err != nil {
		return err
	}
	if _, err := s.vault.Open(sealed, "canary"); err != nil {
		return errors.New("store: the store key does not match this database (was HANDOFF_STORE_KEY changed?)")
	}
	return nil
}

func (s *Store) AddLink(ctx context.Context, l Link) (Link, error) {
	if l.Mode == "" {
		l.Mode = "copy"
	}
	l.ID = uuid.NewString()
	l.State = StateActive
	l.CreatedAt = time.Now().UTC()
	enc, err := s.vault.Seal([]byte(l.Key), l.ID)
	if err != nil {
		return l, err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO links (id, service, role, immich_user_id, name, email, key_id, key_enc, state, mode, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		l.ID, l.Service, l.Role, l.ImmichUserID.String(), l.Name, l.Email, l.KeyID.String(), enc, l.State, l.Mode, l.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		if isUnique(err) {
			return l, ErrExists
		}
		return l, err
	}
	return l, nil
}

// SetLinkAlbum links an album of the sender's to this membership (the zero id unlinks it).
func (s *Store) SetLinkAlbum(ctx context.Context, id string, album uuid.UUID, name string) error {
	albumID := ""
	if album != uuid.Nil {
		albumID = album.String()
	} else {
		name = ""
	}
	res, err := s.db.ExecContext(ctx, `UPDATE links SET album_id = ?, album_name = ? WHERE id = ?`, albumID, name, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ForgetLinkKey overwrites the stored API key of a membership with nothing. The membership stays (so the person
// still sees why it ended and can clean up), but the app no longer holds a key that works.
func (s *Store) ForgetLinkKey(ctx context.Context, id string) error {
	enc, err := s.vault.Seal(nil, id)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE links SET key_enc = ? WHERE id = ?`, enc, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) RemoveLink(ctx context.Context, id string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM handovers WHERE link_id = ?`, id); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM links WHERE id = ?`, id)
	return err
}

// SetLinkPaused records that a sender paused or resumed their membership.
func (s *Store) SetLinkPaused(ctx context.Context, id string, paused bool) error {
	res, err := s.db.ExecContext(ctx, `UPDATE links SET paused = ? WHERE id = ?`, b2i(paused), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetServicePaused records that the owner paused or resumed a service.
func (s *Store) SetServicePaused(ctx context.Context, name string, paused bool) error {
	res, err := s.db.ExecContext(ctx, `UPDATE services SET paused = ? WHERE name = ?`, b2i(paused), name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetServiceLifetime sets how long the copies that arrive stay before they are moved to the owner's trash (0: for ever).
func (s *Store) SetServiceLifetime(ctx context.Context, name string, days int) error {
	res, err := s.db.ExecContext(ctx, `UPDATE services SET lifetime_days = ? WHERE name = ?`, days, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetServiceRules sets a service's quiet hours and what it accepts.
func (s *Store) SetServiceRules(ctx context.Context, name string, v Service) error {
	res, err := s.db.ExecContext(ctx, `UPDATE services SET quiet_from = ?, quiet_to = ?, quiet_large_mb = ?, accept_rating = ?, accept_photos = ?, accept_orient = ?, accept_max_mb = ? WHERE name = ?`,
		v.QuietFrom, v.QuietTo, v.QuietLargeMB, v.AcceptRating, b2i(v.AcceptPhotos), v.AcceptOrient, v.AcceptMaxMB, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetServiceLimits sets the most a sender may hand over to the service in a day: photos and megabytes, 0 for none.
func (s *Store) SetServiceLimits(ctx context.Context, name string, photos, megabytes int) error {
	res, err := s.db.ExecContext(ctx, `UPDATE services SET limit_photos = ?, limit_mb = ? WHERE name = ?`, photos, megabytes, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// HandoverRetention is how long a record of a hand-over is kept: the daily cap looks back one day and the
// Overview's chart a week.
const HandoverRetention = 8 * 24 * time.Hour

// AddHandover records a photo a sender handed over, so the daily cap can be checked and the Overview can chart
// the week. Entries older than HandoverRetention are dropped.
func (s *Store) AddHandover(ctx context.Context, linkID string, bytes int64) error {
	now := time.Now().UTC()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO handovers (link_id, at, bytes) VALUES (?,?,?)`, linkID, now.Format(time.RFC3339Nano), bytes); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM handovers WHERE at < ?`, now.Add(-HandoverRetention).Format(time.RFC3339Nano))
	return err
}

// Handovers is what the sender handed over since the given time: how many photos, how many bytes, and when
// the earliest of them was (the zero time when there were none).
func (s *Store) Handovers(ctx context.Context, linkID string, since time.Time) (photos int, bytes int64, oldest time.Time, err error) {
	var first sql.NullString
	var total sql.NullInt64
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(bytes), 0), MIN(at) FROM handovers WHERE link_id = ? AND at >= ?`,
		linkID, since.UTC().Format(time.RFC3339Nano)).Scan(&photos, &total, &first)
	if err != nil {
		return 0, 0, time.Time{}, err
	}
	bytes = total.Int64
	if first.Valid {
		oldest, _ = time.Parse(time.RFC3339Nano, first.String)
	}
	return photos, bytes, oldest, nil
}

// Links returns every membership with its key decrypted.
func (s *Store) Links(ctx context.Context) ([]Link, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, service, role, immich_user_id, name, email, key_id, key_enc, state, note, mode, album_id, album_name, paused, created_at FROM links ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Link
	for rows.Next() {
		var l Link
		var user, keyID, albumID, created string
		var enc []byte
		if err := rows.Scan(&l.ID, &l.Service, &l.Role, &user, &l.Name, &l.Email, &keyID, &enc, &l.State, &l.Note, &l.Mode, &albumID, &l.AlbumName, &l.Paused, &created); err != nil {
			return nil, err
		}
		l.AlbumID, _ = uuid.Parse(albumID)
		l.ImmichUserID, _ = uuid.Parse(user)
		l.KeyID, _ = uuid.Parse(keyID)
		l.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		plain, err := s.vault.Open(enc, l.ID)
		if err != nil {
			return nil, fmt.Errorf("link %s: %w", l.ID, err)
		}
		l.Key = string(plain)
		out = append(out, l)
	}
	return out, rows.Err()
}

func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE")
}

// Service is a service someone created from the web page. Its receiver key lives in the owner's account.
type Service struct {
	ID, Name               string
	Tag, InboxTag, Refused string
	ExpiryDays             int
	OwnerID                uuid.UUID
	OwnerName, OwnerEmail  string
	KeyID                  uuid.UUID // the receiver key's id in Immich
	Key                    string    // decrypted
	Policy                 string
	State, Note            string    // healthy ("active") or why not
	Visibility, Invite     string    // who can see it; the invitation token (empty until one is made)
	Mode, SentTag          string    // "move" or "copy"; the tag put on originals that were copied
	Album                  string    // name of the album handed-over photos are put in, in the owner's account; empty: none
	Paused                 bool      // the owner paused the service: nothing is handed over until they resume
	LimitPhotos, LimitMB   int       // most a sender may hand over in a day (photos, megabytes); 0: no limit
	QuietFrom, QuietTo     int       // quiet hours, in minutes after midnight; equal: none
	QuietLargeMB           int       // with quiet hours: only files larger than this wait for them (0: every file)
	AcceptRating           int       // the least rating a photo needs (0: any)
	AcceptPhotos           bool      // no videos
	AcceptOrient           string    // "", "landscape" or "portrait"
	AcceptMaxMB            int       // the largest file (0: no limit)
	LifetimeDays           int       // copies that arrive are moved to the owner's trash after this many days unless filed (0: they stay)
	Pool                   bool      // an event pool: every member sends and receives
	EndsAt                 time.Time // a pool stops handing over at this time (zero: until the owner ends it)
	CreatedAt              time.Time
}

// Request is someone asking to send to a service that wants approval.
type Request struct {
	ID, Service string
	UserID      uuid.UUID
	Name, Email string
	Status      string
	CreatedAt   time.Time
}

func (s *Store) AddService(ctx context.Context, v Service) (Service, error) {
	if v.Visibility == "" {
		v.Visibility = VisListed
	}
	v.ID = uuid.NewString()
	v.CreatedAt = time.Now().UTC()
	enc, err := s.vault.Seal([]byte(v.Key), v.ID)
	if err != nil {
		return v, err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO services (id, name, tag, inbox_tag, refused_tag, expiry_days, owner_id, owner_name, owner_email, key_id, key_enc, policy, visibility, invite, mode, sent_tag, album, limit_photos, limit_mb, pool, ends_at, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		v.ID, v.Name, v.Tag, v.InboxTag, v.Refused, v.ExpiryDays, v.OwnerID.String(), v.OwnerName, v.OwnerEmail, v.KeyID.String(), enc, v.Policy, v.Visibility, v.Invite, v.Mode, v.SentTag, v.Album, v.LimitPhotos, v.LimitMB, b2i(v.Pool), tstr(v.EndsAt), v.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		if isUnique(err) {
			return v, ErrExists
		}
		return v, err
	}
	return v, nil
}

func (s *Store) Services(ctx context.Context) ([]Service, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, tag, inbox_tag, refused_tag, expiry_days, owner_id, owner_name, owner_email, key_id, key_enc, policy, state, note, visibility, invite, mode, sent_tag, album, paused, limit_photos, limit_mb, quiet_from, quiet_to, quiet_large_mb, accept_rating, accept_photos, accept_orient, accept_max_mb, lifetime_days, pool, ends_at, created_at FROM services ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Service
	for rows.Next() {
		var v Service
		var owner, keyID, created, ends string
		var enc []byte
		if err := rows.Scan(&v.ID, &v.Name, &v.Tag, &v.InboxTag, &v.Refused, &v.ExpiryDays, &owner, &v.OwnerName, &v.OwnerEmail, &keyID, &enc, &v.Policy, &v.State, &v.Note, &v.Visibility, &v.Invite, &v.Mode, &v.SentTag, &v.Album, &v.Paused, &v.LimitPhotos, &v.LimitMB, &v.QuietFrom, &v.QuietTo, &v.QuietLargeMB, &v.AcceptRating, &v.AcceptPhotos, &v.AcceptOrient, &v.AcceptMaxMB, &v.LifetimeDays, &v.Pool, &ends, &created); err != nil {
			return nil, err
		}
		v.OwnerID, _ = uuid.Parse(owner)
		v.KeyID, _ = uuid.Parse(keyID)
		v.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		v.EndsAt, _ = time.Parse(time.RFC3339Nano, ends)
		plain, err := s.vault.Open(enc, v.ID)
		if err != nil {
			return nil, fmt.Errorf("service %s: %w", v.ID, err)
		}
		v.Key = string(plain)
		out = append(out, v)
	}
	return out, rows.Err()
}

// RemoveService deletes the service and its requests, and marks its members' links orphaned.
func (s *Store) RemoveService(ctx context.Context, name string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`UPDATE links SET state = 'orphaned', paused = 0 WHERE service = ?`,
		`DELETE FROM requests WHERE service = ?`,
		`DELETE FROM bridges WHERE service = ?`,
		`DELETE FROM bridge_invites WHERE service = ?`,
		`DELETE FROM services WHERE name = ?`,
	} {
		if _, err := tx.ExecContext(ctx, q, name); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) AddRequest(ctx context.Context, r Request) (Request, error) {
	r.ID = uuid.NewString()
	r.Status = StatusPending
	r.CreatedAt = time.Now().UTC()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO requests (id, service, user_id, name, email, status, created_at) VALUES (?,?,?,?,?,?,?)`,
		r.ID, r.Service, r.UserID.String(), r.Name, r.Email, r.Status, r.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		if isUnique(err) {
			return r, ErrExists
		}
		return r, err
	}
	return r, nil
}

func (s *Store) Requests(ctx context.Context) ([]Request, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, service, user_id, name, email, status, created_at FROM requests ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Request
	for rows.Next() {
		var r Request
		var user, created string
		if err := rows.Scan(&r.ID, &r.Service, &user, &r.Name, &r.Email, &r.Status, &created); err != nil {
			return nil, err
		}
		r.UserID, _ = uuid.Parse(user)
		r.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) SetRequestStatus(ctx context.Context, id, status string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE requests SET status = ? WHERE id = ?`, status, id)
	return err
}

func (s *Store) RemoveRequest(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM requests WHERE id = ?`, id)
	return err
}

// AllLinks returns memberships in every state (Links is the same; kept for readability at call sites).
func (s *Store) AllLinks(ctx context.Context) ([]Link, error) { return s.Links(ctx) }

// SetLinkState records a link's health. It reports whether a link with that id exists.
func (s *Store) SetLinkState(ctx context.Context, id, state, note string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE links SET state = ?, note = ? WHERE id = ?`, state, note, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// SetServiceState records a service's health. It reports whether a service with that id exists.
func (s *Store) SetServiceState(ctx context.Context, id, state, note string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE services SET state = ?, note = ? WHERE id = ?`, state, note, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ReplaceServiceKey swaps in a new receiver key and marks the service healthy again.
func (s *Store) ReplaceServiceKey(ctx context.Context, id string, keyID uuid.UUID, key string) error {
	enc, err := s.vault.Seal([]byte(key), id)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE services SET key_id = ?, key_enc = ?, state = 'active', note = '' WHERE id = ?`, keyID.String(), enc, id)
	return err
}

// Event is one line of the activity log.
type Event struct {
	At      time.Time
	Service string
	UserID  uuid.UUID // who it is about
	Kind    string
	Text    string
}

const maxEvents = 500

// AddEvent appends to the activity log, which keeps only the most recent entries.
func (s *Store) AddEvent(ctx context.Context, e Event) error {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO events (at, service, user_id, kind, text) VALUES (?,?,?,?,?)`,
		time.Now().UTC().Format(time.RFC3339Nano), e.Service, e.UserID.String(), e.Kind, e.Text); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM events WHERE id <= (SELECT MAX(id) FROM events) - ?`, maxEvents)
	return err
}

// Events returns the most recent entries, newest first.
func (s *Store) Events(ctx context.Context, limit int) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT at, service, user_id, kind, text FROM events ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var at, user string
		if err := rows.Scan(&at, &e.Service, &user, &e.Kind, &e.Text); err != nil {
			return nil, err
		}
		e.At, _ = time.Parse(time.RFC3339Nano, at)
		e.UserID, _ = uuid.Parse(user)
		out = append(out, e)
	}
	return out, rows.Err()
}

// SetServiceVisibility changes who can see a service and sets its invitation token.
func (s *Store) SetServiceVisibility(ctx context.Context, id, visibility, invite string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE services SET visibility = ?, invite = ? WHERE id = ?`, visibility, invite, id)
	return err
}

// HandoverDays counts, per day, the photos handed over through these memberships since the given time. The days
// are UTC dates ("2026-11-06"); a day without any is simply absent.
func (s *Store) HandoverDays(ctx context.Context, linkIDs []string, since time.Time) (map[string]int, error) {
	out := map[string]int{}
	if len(linkIDs) == 0 {
		return out, nil
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(linkIDs)), ",")
	args := make([]any, 0, len(linkIDs)+1)
	for _, id := range linkIDs {
		args = append(args, id)
	}
	args = append(args, since.UTC().Format(time.RFC3339Nano))
	rows, err := s.db.QueryContext(ctx, `SELECT substr(at, 1, 10), COUNT(*) FROM handovers WHERE link_id IN (`+marks+`) AND at >= ? GROUP BY 1`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var day string
		var n int
		if err := rows.Scan(&day, &n); err != nil {
			return nil, err
		}
		out[day] = n
	}
	return out, rows.Err()
}

// GuideDone reports whether the person has finished or hidden the first-run guide.
func (s *Store) GuideDone(ctx context.Context, user uuid.UUID) (bool, error) {
	var at string
	err := s.db.QueryRowContext(ctx, `SELECT done_at FROM guide WHERE user_id = ?`, user.String()).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// SetGuideDone remembers that the person finished or hid the first-run guide.
func (s *Store) SetGuideDone(ctx context.Context, user uuid.UUID) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO guide (user_id, done_at) VALUES (?, ?)`, user.String(), time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// HandoverItem is one photo of a hand-over: the sender's original and the receiver's copy of it.
type HandoverItem struct {
	ID                         int64
	Batch                      string
	SenderAsset, ReceiverAsset uuid.UUID
	Clip                       bool // a live photo's motion clip (not counted as a photo)
	Remote                     bool // the copy is on another Immich server (a bridge), not in the receiver's own library
	At                         time.Time
}

// AddHandoverItem remembers that the receiver's copy came from the sender's photo, in the hand-over named batch (one
// per sender per pass), so the sender can take it back. Only a few days are kept.
func (s *Store) AddHandoverItem(ctx context.Context, linkID, batch string, sender, receiver uuid.UUID, clip, remote bool) error {
	now := time.Now().UTC()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO handover_items (link_id, batch, sender_asset, receiver_asset, clip, remote, at) VALUES (?,?,?,?,?,?,?)`,
		linkID, batch, sender.String(), receiver.String(), b2i(clip), b2i(remote), now.Format(time.RFC3339Nano)); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM handover_items WHERE at < ?`, now.Add(-HandoverRetention).Format(time.RFC3339Nano))
	return err
}

// LastHandover is the photos of the sender's most recent hand-over since the given time that were not taken back yet.
func (s *Store) LastHandover(ctx context.Context, linkID string, since time.Time) ([]HandoverItem, error) {
	var batch sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT batch FROM handover_items WHERE link_id = ? AND at >= ? AND taken_back = 0 ORDER BY at DESC, id DESC LIMIT 1`,
		linkID, since.UTC().Format(time.RFC3339Nano)).Scan(&batch); err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	if !batch.Valid {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, batch, sender_asset, receiver_asset, clip, remote, at FROM handover_items WHERE link_id = ? AND batch = ? AND taken_back = 0 ORDER BY id`, linkID, batch.String)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HandoverItem
	for rows.Next() {
		var it HandoverItem
		var snd, rcv, at string
		if err := rows.Scan(&it.ID, &it.Batch, &snd, &rcv, &it.Clip, &it.Remote, &at); err != nil {
			return nil, err
		}
		it.SenderAsset, _ = uuid.Parse(snd)
		it.ReceiverAsset, _ = uuid.Parse(rcv)
		it.At, _ = time.Parse(time.RFC3339Nano, at)
		out = append(out, it)
	}
	return out, rows.Err()
}

// MarkTakenBack records that these items were taken back, so they are not offered again.
func (s *Store) MarkTakenBack(ctx context.Context, ids []int64) error {
	for _, id := range ids {
		if _, err := s.db.ExecContext(ctx, `UPDATE handover_items SET taken_back = 1 WHERE id = ?`, id); err != nil {
			return err
		}
	}
	return nil
}

// SetServiceEnd sets when a pool stops handing over (zero: until the owner ends it).
func (s *Store) SetServiceEnd(ctx context.Context, name string, at time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE services SET ends_at = ? WHERE name = ?`, tstr(at), name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
