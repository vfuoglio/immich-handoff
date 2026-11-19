package dest

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"sync/atomic"
	"time"

	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/syncer"
)

// Remote makes a Destination a receiver of a service: it streams the original (and a live photo's clip) from the sender's Immich into
// the destination, checks what the destination holds, and says so only when it holds the photo. It takes no part in what is written
// besides the bytes: the name comes from Path (the template, the sidecar and the record are the folder's, WebDAV's, ... own work).
type Remote struct {
	Ident   string      // the receiver's name within its service
	Name    string      // what logs and pages call it
	Service string      // the service it belongs to, for the record written beside each file
	D       Destination // where the files go
	// Path names the file for a photo, relative to the destination's root; nil: the original's file name. It must not leave the root: the
	// destination cleans it again.
	Path func(job syncer.RemoteJob, a immich.AssetResponseDto) string
	// Stall is how long a transfer may go without a byte before it is given up (zero: immich.StallTimeout).
	Stall time.Duration
}

var (
	_ syncer.Remote     = (*Remote)(nil)
	_ syncer.TakeBacker = (*Remote)(nil)
	_ syncer.Diagnoser  = (*Remote)(nil)
)

func (r *Remote) ID() string    { return r.Ident }
func (r *Remote) Label() string { return r.Name }

// Ready asks the destination to check itself (a write and a removal where that is the only way to know).
func (r *Remote) Ready(ctx context.Context) error {
	_, err := r.D.Check(ctx)
	return err
}

// Diagnose implements syncer.Diagnoser.
func (r *Remote) Diagnose(ctx context.Context) (string, error) { return r.D.Check(ctx) }

// ErrNotHeld: the destination did not hold what was sent when it was looked at.
var ErrNotHeld = errors.New("the destination does not hold the file as it was sent")

func (r *Remote) name(job syncer.RemoteJob, a immich.AssetResponseDto) string {
	if r.Path != nil {
		return r.Path(job, a)
	}
	return path.Base(strings.ReplaceAll(a.OriginalFileName, "\\", "/"))
}

// Deliver implements syncer.Remote. The photo is written first, then the XMP sidecar (the rating, location and description a bridge also
// carries, when details are carried) and a record of where it came from, both named after the photo; a live photo's clip follows with the
// photo's base name. The photo is held when the destination's own look at it (its size and, when it can say, its checksum) agrees with
// the bytes that were sent. When anything after the photo fails, what this delivery created is removed again, so that a retry starts clean.
func (r *Remote) Deliver(ctx context.Context, src *immich.API, a immich.AssetResponseDto, clip *immich.AssetResponseDto, job syncer.RemoteJob) (syncer.RemoteCopy, error) {
	var cp syncer.RemoteCopy
	st, err := r.put(ctx, src, a, r.name(job, a))
	if err != nil {
		return cp, err
	}
	undo := func() {
		if !st.Existed {
			r.removeAll(context.WithoutCancel(ctx), st.Ref)
		}
	}
	if err := r.aux(ctx, st, a, job); err != nil {
		undo()
		return cp, fmt.Errorf("the photo arrived but its sidecar or record could not be written: %w", err)
	}
	cp.Ref, cp.Created = st.Ref, !st.Existed
	if clip != nil {
		cst, err := r.put(ctx, src, *clip, clipName(st.Ref, *clip))
		if err != nil {
			undo()
			return syncer.RemoteCopy{}, fmt.Errorf("motion clip: %w", err)
		}
		cp.Clip, cp.ClipCreated = cst.Ref, !cst.Existed
	}
	return cp, nil
}

// clipName is the name of a live photo's clip: the photo's base name (as it was finally stored) and the clip's own extension.
func clipName(stillRef string, clip immich.AssetResponseDto) string {
	ext := path.Ext(strings.ReplaceAll(clip.OriginalFileName, "\\", "/"))
	if ext == "" {
		ext = ".mov"
	}
	return strings.TrimSuffix(stillRef, path.Ext(stillRef)) + ext
}

// record is what is written beside each file: where it came from, and what was written.
type record struct {
	File           string    `json:"file"`
	Original       string    `json:"original_name"`
	Sender         string    `json:"sender"`
	Service        string    `json:"service"`
	At             time.Time `json:"delivered_at"`
	SourceAsset    string    `json:"source_asset"`
	SourceChecksum string    `json:"source_checksum,omitempty"`
	SHA1           string    `json:"sha1"`
	Size           int64     `json:"size"`
}

const (
	sidecarExt = ".xmp"
	recordExt  = ".handoff.json"
)

// aux writes the sidecar and the record beside a stored photo. For a photo that was already there (a repeat) the record of the first
// delivery is kept, and the pieces are written only if they are missing.
func (r *Remote) aux(ctx context.Context, st Stored, a immich.AssetResponseDto, job syncer.RemoteJob) error {
	if st.Existed {
		if _, err := r.D.Stat(ctx, st.Ref+recordExt); err == nil {
			return nil
		}
	}
	if job.Details {
		if x := immich.Sidecar(a); x != nil {
			if err := r.replace(ctx, st.Ref+sidecarExt, x); err != nil {
				return err
			}
		}
	}
	b, _ := json.MarshalIndent(record{File: st.Ref, Original: a.OriginalFileName, Sender: job.Sender, Service: r.Service, At: time.Now().UTC(), SourceAsset: a.Id.String(), SourceChecksum: a.Checksum, SHA1: st.Sum, Size: st.Size}, "", " ")
	return r.replace(ctx, st.Ref+recordExt, append(b, '\n'))
}

// replace writes a small file under exactly this name, replacing one that is there.
func (r *Remote) replace(ctx context.Context, ref string, b []byte) error {
	if err := r.D.Delete(ctx, ref); err != nil {
		return err
	}
	st, err := r.D.Put(ctx, ref, bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return err
	}
	if st.Ref != ref {
		r.D.Delete(ctx, st.Ref)
		return fmt.Errorf("%s was taken", ref)
	}
	return nil
}

// removeAll deletes a photo and what was written beside it.
func (r *Remote) removeAll(ctx context.Context, ref string) error {
	var first error
	for _, x := range []string{ref, ref + sidecarExt, ref + recordExt} {
		if err := r.D.Delete(ctx, x); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (r *Remote) put(ctx context.Context, src *immich.API, a immich.AssetResponseDto, name string) (Stored, error) {
	stall := r.Stall
	if stall <= 0 {
		stall = immich.StallTimeout
	}
	ctx, cancel := context.WithCancel(ctx) // cancelled when the transfer stalls: it ends the download and the write
	defer cancel()
	resp, err := src.Original(ctx, a.Id)
	if err != nil {
		return Stored{}, err
	}
	defer resp.Body.Close()
	body := &stallReader{r: resp.Body, d: stall}
	body.timer = time.AfterFunc(stall, func() { body.hit.Store(true); cancel() })
	defer body.timer.Stop()
	h := sha1.New()
	var n atomic.Int64
	size := resp.ContentLength // -1 when the server did not say
	tee := io.TeeReader(body, io.MultiWriter(h, countWriter{&n}))
	st, err := r.D.Put(ctx, name, tee, size)
	if err != nil {
		if body.stalled() {
			return Stored{}, fmt.Errorf("the transfer stalled: no data for %s", stall)
		}
		return Stored{}, err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	switch {
	case size >= 0 && n.Load() != size:
		return Stored{}, fmt.Errorf("%w: %d of %d bytes were read from Immich", ErrNotHeld, n.Load(), size)
	case st.Size != n.Load():
		return Stored{}, fmt.Errorf("%w: it holds %d bytes, %d were sent", ErrNotHeld, st.Size, n.Load())
	case st.Sum != "" && st.Sum != sum:
		return Stored{}, fmt.Errorf("%w: its checksum differs", ErrNotHeld)
	}
	// and the destination's own answer when asked again, not only what it said while writing
	info, err := r.D.Stat(ctx, st.Ref)
	switch {
	case errors.Is(err, ErrNotFound):
		return Stored{}, fmt.Errorf("%w: it is not there", ErrNotHeld)
	case err != nil:
		return Stored{}, err
	case info.Size != n.Load():
		return Stored{}, fmt.Errorf("%w: it holds %d bytes, %d were sent", ErrNotHeld, info.Size, n.Load())
	case info.Sum != "" && info.Sum != sum:
		return Stored{}, fmt.Errorf("%w: its checksum differs", ErrNotHeld)
	}
	return st, nil
}

// TakeBackRefs implements syncer.TakeBacker: the files are deleted (a destination has no trash).
func (r *Remote) TakeBackRefs(ctx context.Context, refs []string) error {
	for _, ref := range refs {
		if err := r.removeAll(ctx, ref); err != nil {
			return err
		}
	}
	return nil
}

type countWriter struct{ n *atomic.Int64 }

func (c countWriter) Write(p []byte) (int, error) { c.n.Add(int64(len(p))); return len(p), nil }

// stallReader gives up a transfer that moves no byte for a while by cancelling its context.
type stallReader struct {
	r     io.Reader
	timer *time.Timer
	d     time.Duration
	hit   atomic.Bool
}

func (s *stallReader) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	if n > 0 {
		s.timer.Reset(s.d)
	}
	return n, err
}

func (s *stallReader) stalled() bool { return s.hit.Load() }
