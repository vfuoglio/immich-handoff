package bridge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
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
	if r.Report != nil {
		r.Report(err, info.Version)
	}
	return err
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
func (r *Remote) Deliver(ctx context.Context, src *immich.API, a immich.AssetResponseDto, clip *immich.AssetResponseDto, job syncer.RemoteJob) (err error) {
	defer func() {
		if err != nil && r.Report != nil { // a success needs no word: Ready reported the server healthy at the start of the pass
			r.Report(err, "")
		}
	}()
	var motion *uuid.UUID
	if clip != nil {
		up, _, err := immich.TransferWith(ctx, src, r.api, *clip, immich.UploadOptions{Hidden: true})
		if err != nil {
			return r.classify(fmt.Errorf("motion clip: %w", err))
		}
		id := up.Id
		motion = &id
	}
	up, _, err := immich.TransferWith(ctx, src, r.api, a, immich.UploadOptions{MotionClip: motion, Details: job.Details})
	if err != nil {
		return r.classify(err)
	}
	tag, err := r.api.UpsertTag(ctx, TagFor(job.Sender))
	if err != nil {
		return r.classify(fmt.Errorf("make the tag: %w", err))
	}
	if err := r.api.TagAssets(ctx, tag, up.Id); err != nil {
		return r.classify(fmt.Errorf("tag the photo: %w", err))
	}
	if up.Status == immich.AssetMediaStatusCreated {
		retag := context.WithoutCancel(ctx)
		time.AfterFunc(retagAfter, func() {
			c, cancel := context.WithTimeout(retag, callTimeout)
			defer cancel()
			_ = r.api.TagAssets(c, tag, up.Id) // best effort: the photo is already there and tagged as far as can be told
		})
	}
	return nil
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
