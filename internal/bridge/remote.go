package bridge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/syncer"
)

// retagAfter is how long after an upload its tag is put on a second time. Immich now and then drops a tag put on in
// the first moments after an upload, and a key made for a bridge cannot read the photo to check (see
// docs/immich-api-notes.md). Putting it on again a little later costs nothing when it is there.
var retagAfter = 6 * time.Second

// Remote is a receiver on another Immich server, reached with a key that can only upload and tag. It implements
// syncer.Remote.
type Remote struct {
	Name    string // what the receiver chose to be called, for logs and messages
	Address string // as Policy.Address returned it
	Policy  Policy

	// Report is told how a check or a hand-over ended: nil for success, or the error. It is how the page and the
	// notifications learn the state. May be nil.
	Report func(err error, version string)

	// Gate is asked before every pass: a non-nil error holds the photos back without touching the other server (a
	// revoked key is not tried again, an unreachable server is left alone until its retry time). May be nil.
	Gate func() error

	api *immich.API
	key string
	hc  *http.Client

	takeBack atomic.Bool // the key held asset.delete at the last check
}

var _ syncer.Remote = (*Remote)(nil)

// NewRemote makes the receiver. The address must have been accepted by Policy.Address.
func NewRemote(name, address, key string, p Policy) (*Remote, error) {
	hc := p.HTTPClient()
	api, err := immich.New(address, immich.Auth{APIKey: key}, hc)
	if err != nil {
		return nil, err
	}
	return &Remote{Name: name, Address: address, Policy: p, api: api, key: key, hc: hc}, nil
}

// Label implements syncer.Remote.
func (r *Remote) Label() string { return r.Name }

// Ready implements syncer.Remote: the server must answer as Immich with a key that has exactly the four permissions.
func (r *Remote) Ready(ctx context.Context) error {
	if r.Gate != nil {
		if err := r.Gate(); err != nil {
			return err
		}
	}
	info, err := Check(ctx, r.hc, r.Address, r.key)
	if err == nil || errors.Is(err, ErrRevoked) || errors.Is(err, ErrKeyTooSmall) {
		r.takeBack.Store(info.CanTakeBack) // an unreachable server keeps what was last known
	}
	if r.Report != nil {
		r.Report(err, info.Version)
	}
	return err
}

// CanTakeBack says whether the key held asset.delete when the server was last checked: a sender can take a hand-over
// back from there. It is what the page shows; TakeBack asks the server again before it acts.
func (r *Remote) CanTakeBack() bool { return r.takeBack.Load() }

// ErrCannotTakeBack: the receiver's key has not got (or no longer has) the optional asset.delete permission.
var ErrCannotTakeBack = errors.New("the receiver's key on the other server cannot move photos to the trash (it has no asset.delete)")

// TakeBack moves copies the app handed over to the receiver's trash, and nothing else: it knows the ids it was given
// and reads nothing from the other server. Immich keeps the trash for as long as its own settings say, so nothing is
// deleted for good. It asks the server about the key first, so a permission the receiver removed is noticed now.
func (r *Remote) TakeBack(ctx context.Context, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	info, err := Check(ctx, r.hc, r.Address, r.key)
	if err == nil || errors.Is(err, ErrRevoked) || errors.Is(err, ErrKeyTooSmall) {
		r.takeBack.Store(info.CanTakeBack)
	}
	if err != nil {
		return err // Check's own reasons (revoked, unreachable, ...), as Ready returns them
	}
	if !info.CanTakeBack {
		return ErrCannotTakeBack
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	if err := r.api.DeleteAssets(ctx, ids, false); err != nil {
		var he *immich.HTTPError
		if errors.As(err, &he) && he.Code == http.StatusForbidden {
			r.takeBack.Store(false)
			return ErrCannotTakeBack
		}
		return r.classify(err)
	}
	return nil
}

// TagFor is the tag a photo from a sender gets on the other server: "from/<name>", one inside another, so the receiver
// finds everything from one person together.
func TagFor(sender string) string {
	name := strings.TrimSpace(strings.ReplaceAll(sender, "/", "-"))
	if name == "" {
		name = "someone"
	}
	return "from/" + name
}

// Deliver implements syncer.Remote. It streams the original (a live photo's hidden clip first), treats a duplicate as
// success, and puts the tag on. It reads nothing from the other server's library.
func (r *Remote) Deliver(ctx context.Context, src *immich.API, a immich.AssetResponseDto, clip *immich.AssetResponseDto, job syncer.RemoteJob) (cp syncer.RemoteCopy, err error) {
	defer func() {
		if err != nil && r.Report != nil { // a success needs no word: Ready reported the server healthy at the start of the pass
			r.Report(err, "")
		}
	}()
	var motion *uuid.UUID
	if clip != nil {
		up, _, err := immich.TransferWith(ctx, src, r.api, *clip, immich.UploadOptions{Hidden: true})
		if err != nil {
			return cp, r.classify(fmt.Errorf("motion clip: %w", err))
		}
		id := up.Id
		motion = &id
		cp.Clip, cp.ClipCreated = id, up.Status == immich.AssetMediaStatusCreated
	}
	up, _, err := immich.TransferWith(ctx, src, r.api, a, immich.UploadOptions{MotionClip: motion, Details: job.Details})
	if err != nil {
		return cp, r.classify(err)
	}
	cp.ID, cp.Created = up.Id, up.Status == immich.AssetMediaStatusCreated
	tag, err := r.api.UpsertTag(ctx, TagFor(job.Sender))
	if err != nil {
		return cp, r.classify(fmt.Errorf("make the tag: %w", err))
	}
	if err := r.api.TagAssets(ctx, tag, up.Id); err != nil {
		return cp, r.classify(fmt.Errorf("tag the photo: %w", err))
	}
	if up.Status == immich.AssetMediaStatusCreated {
		retag := context.WithoutCancel(ctx)
		time.AfterFunc(retagAfter, func() {
			c, cancel := context.WithTimeout(retag, callTimeout)
			defer cancel()
			_ = r.api.TagAssets(c, tag, up.Id) // best effort: the photo is already there and tagged as far as can be told
		})
	}
	return cp, nil
}

// classify turns what Immich answered into the reasons Check uses, so that one place decides revoked or unreachable.
// The other server's address is taken out of the message: it ends up in notifications and on pages.
func (r *Remote) classify(err error) error {
	var he *immich.HTTPError
	say := func() string { return r.clean(err.Error()) }
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrBadAddress):
		return err
	case errors.As(err, &he) && he.Code == http.StatusUnauthorized:
		return fmt.Errorf("%w", ErrRevoked)
	case errors.As(err, &he) && he.Code >= 500:
		return fmt.Errorf("%w: %s", ErrUnreachable, say())
	case errors.As(err, &he):
		return errors.New(say()) // a refusal of this photo or this key: not a connection problem
	default:
		return fmt.Errorf("%w: %s", ErrUnreachable, say())
	}
}

func (r *Remote) clean(msg string) string {
	if r.Address != "" {
		msg = strings.ReplaceAll(msg, r.Address, "the other server")
	}
	if u, err := url.Parse(r.Address); err == nil && u.Host != "" {
		msg = strings.ReplaceAll(msg, u.Host, "the other server")
	}
	return msg
}

// Diagnose is Check for the doctor: it asks the same two questions and changes nothing (no state, no gate, no report).
// The address is judged again against the current rules first, because the administrator may have tightened them
// since the receiver was connected.
func (r *Remote) Diagnose(ctx context.Context) (Info, error) {
	if _, err := r.Policy.Address(r.Address); err != nil {
		return Info{}, err
	}
	return Check(ctx, r.hc, r.Address, r.key)
}
