package syncer

import (
	"context"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/immich"
)

// Account is one Immich account the app can act for, through one API key.
type Account struct {
	Name   string
	API    *immich.API
	UserID uuid.UUID // the Immich user behind the key; set by ResolveUsers or when a person joins
	Mode   string    // what happens to this sender's originals (ModeMove / ModeCopy); empty: the service's setting

	// WatchAlbum is an album of this sender's whose photos are all handed over, now and later, without a tag.
	// Zero: none. WatchAlbumName is its name, for messages. Set when the account is built, never changed after.
	WatchAlbum     uuid.UUID
	WatchAlbumName string

	// Paused: the sender paused their membership. Their tagged photos are left alone, still tagged, and are
	// handed over when they resume. Set when the account is built, never changed after.
	Paused bool
}

const (
	ModeMove = "move" // the original goes to the sender's trash once every receiver has a verified copy
	ModeCopy = "copy" // the original stays; it loses the service tag and gets the sent tag so it is not handed over again
)

// Service says: photos that a sender tags with Tag are handed to every receiver.
// The original is removed only after all receivers hold a verified copy.
type Service struct {
	Name       string // "" is the single default service of a plain .env install
	Tag        string // tag a sender adds
	InboxTag   string // tag put on new copies in each receiver (unless Album is set and works)
	Album      string // optional: name of an album, in each receiver's account, that new copies are put in
	RefusedTag string // replaces Tag on an original that a receiver refused
	Mode       string // ModeMove (default) or ModeCopy
	SentTag    string // copy mode: put on an original after it was handed over
	ExpiryDays int    // unfiled copies lose InboxTag after this many days
	Senders    []*Account
	Receivers  []*Account
	Remote     Remote // a receiver on another Immich server, in addition to Receivers (nil: none)
	AllowJoin  bool   // people may add themselves as senders from the web page

	NeedsApproval bool      // people may ask to send; the owner decides, then the sender activates
	OwnerID       uuid.UUID // the Immich user who created and owns a service made on the web page (zero when configured)
	Unlisted      bool      // hidden from the directory; reachable only through Invite
	Invite        string    // the owner's invitation token (only used by the hub)
	Paused        bool      // the owner paused the service: nothing is handed over, and tagged photos stay tagged, until they resume
	LimitPhotos   int       // most photos one sender may hand over in a day; 0: no limit
	LimitMB       int       // most megabytes one sender may hand over in a day; 0: no limit
	LifetimeDays  int       // copies that arrive are moved to the receiver's trash after this many days unless filed in an album (0: they stay); needs RECEIVER_DELETE
	Quiet         Quiet     // when the service hands photos over (empty: any time); the app-wide default applies when this is empty
	Accept        Accept    // what the service takes in
	Broken        string    // why the service cannot work right now (for example its receiver key was deleted); empty when healthy
}

// Remote is a receiver that lives on another Immich server (a bridge). The app holds a key there that can only upload
// and tag, so none of what a same-server receiver supports (stacks, albums, expiry, taking back, checking the copy by
// reading it) applies. It is in addition to the service's own receivers: a photo goes to all of them, and the original
// is touched only after every one holds it.
type Remote interface {
	// Label names the receiver in logs.
	Label() string
	// Ready says whether photos may be handed to it now; a non-nil error is why not, and the photos wait, still
	// tagged, as for a paused service. It is called once at the start of every pass.
	Ready(ctx context.Context) error
	// Deliver hands over one photo (and its motion clip, for a live photo) streamed from src. It returns nil only when
	// the other server has the photo and its tag.
	Deliver(ctx context.Context, src *immich.API, a immich.AssetResponseDto, clip *immich.AssetResponseDto, job RemoteJob) error
}

// RemoteJob carries what Remote.Deliver needs besides the photo.
type RemoteJob struct {
	Sender  string // the sender's display name: the tag on the other side says who it is from
	Details bool   // carry the description, rating, location and archived state
}

// Label is the name used in logs and on the status page.
// Copies reports whether the service keeps originals by its own setting.
func (s *Service) Copies() bool { return s.Mode == ModeCopy }

// CopiesFor reports whether this sender's originals are kept. A person who joined chose for themselves
// when they agreed to take part; accounts set up by an admin follow the service's setting.
func (s *Service) CopiesFor(a *Account) bool {
	if a != nil && a.Mode != "" {
		return a.Mode == ModeCopy
	}
	return s.Copies()
}

func (s *Service) Label() string {
	if s.Name == "" {
		return "default"
	}
	return s.Name
}

// WorkflowName is the name of the Immich workflow this app keeps in each sender's account.
// The default service keeps the original name so existing installs are not duplicated on upgrade.
func (s *Service) WorkflowName() string {
	if s.Name == "" {
		return "immich-handoff"
	}
	return "immich-handoff: " + s.Name
}

// Provider supplies the current services when they can change while the app runs.
type Provider interface {
	Services() []*Service
}

// Watcher is implemented by a Provider that wants to know when Immich rejects an account's key.
type Watcher interface {
	KeyRejected(acct *Account)
}

// Current returns the services to work on right now.
func (s *Syncer) Current() []*Service {
	if s.Provider != nil {
		return s.Provider.Services()
	}
	return s.Services
}

// ResolveUsers looks up which Immich user is behind each account's key. Accounts whose key is
// rejected are returned so the caller can report them; they simply never match a signed-in person.
func ResolveUsers(ctx context.Context, services []*Service) (failed []*Account) {
	seen := map[*Account]bool{}
	for _, svc := range services {
		for _, group := range [][]*Account{svc.Senders, svc.Receivers} {
			for _, a := range group {
				if seen[a] {
					continue
				}
				seen[a] = true
				if u, err := a.API.Me(ctx); err == nil {
					a.UserID = u.ID
				} else {
					failed = append(failed, a)
				}
			}
		}
	}
	return failed
}
