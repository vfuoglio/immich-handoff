// Package dest is a place a photo can be handed over to that is not an Immich account: a folder, WebDAV, S3, SFTP. A Destination is
// the small interface each kind implements (it is also what importing from the same places reads through); Remote makes one a
// receiver of a service, streaming the original from the sender's Immich and checking the copy before it says the photo is held.
package dest

import (
	"context"
	"errors"
	"io"
	"time"
)

// ErrNotFound: nothing is stored under that reference.
var ErrNotFound = errors.New("not found")

// Stored is what a destination holds after Put.
type Stored struct {
	Ref     string // names the file there (a path, a key); what Stat, Open and Delete take
	Size    int64  // the size the destination holds
	Sum     string // the SHA-1 (hex) of what it holds when it knows it (computed while writing, or the server's checksum); empty when it cannot say
	Existed bool   // a file with that name and checksum was already there, so nothing new was written
}

// Info is what Stat says about a file.
type Info struct {
	Size     int64
	Sum      string // as in Stored; empty when unknown
	Modified time.Time
}

// Entry is one file in a listing.
type Entry struct {
	Ref      string
	Size     int64
	Modified time.Time
}

// Destination is one place files can be written to and read from. Names are paths relative to the destination's root, with "/" as
// the separator; a destination cleans them and never leaves its root.
type Destination interface {
	// Put writes the bytes of r under name, so that the file appears whole or not at all (a temporary name and a move where the store can; a
	// single write with the size checked where it cannot). size is the expected size, or -1 when unknown. A name already taken by a file
	// with the same checksum is not written again (Stored.Existed); a different file under the name gets a different one (the ref says which).
	Put(ctx context.Context, name string, r io.Reader, size int64) (Stored, error)
	// Stat says what is stored under ref, or ErrNotFound.
	Stat(ctx context.Context, ref string) (Info, error)
	// Delete removes ref; a ref that is not there is not an error.
	Delete(ctx context.Context, ref string) error
	// List says the files under a prefix, recursively.
	List(ctx context.Context, prefix string) ([]Entry, error)
	// Open reads ref; the caller closes it.
	Open(ctx context.Context, ref string) (io.ReadCloser, error)
	// Check says whether the destination can be reached and written to now (it writes and removes a test file where that is the only way to
	// know), and what it checked, for the doctor and for the start of a pass.
	Check(ctx context.Context) (string, error)
}
