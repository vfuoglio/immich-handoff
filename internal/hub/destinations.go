package hub

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/dest"
	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/live"
	"github.com/vfuoglio/immich-handoff/internal/notify"
	"github.com/vfuoglio/immich-handoff/internal/store"
	"github.com/vfuoglio/immich-handoff/internal/syncer"
)

// Destinations (docs/design/destinations-design.md): places a service hands photos to that are not an Immich account. The administrator
// names some (DESTINATIONS_FILE); with DESTINATIONS_USER_ADD a person may enter network destinations of their own on a service card.
// A service binds destinations with where each puts files, its own daily limit and its own pause.

var (
	ErrNoDestination    = errors.New("there is no such destination")
	ErrDestinationTaken = errors.New("this service already uses that destination")
	ErrUserDestsOff     = errors.New("the administrator has not allowed people to add destinations of their own")
	ErrNoFolderByUser   = errors.New("a folder can only be one the administrator named: choose it from the list")
	ErrBadDestination   = errors.New("that destination is not valid")
	ErrBadDestLimit     = errors.New("a daily limit is a whole number of photos and megabytes, 0 for none")
	ErrTooManyDests     = errors.New("a service can use at most 8 destinations")
)

const maxServiceDestinations = 8

// DestChoice is a destination a service owner may bind: the administrator's by name, or one of their own.
type DestChoice struct {
	Ref, Name, Kind string
	Own             bool
}

// DestStatus is one destination of a service, for the owner's card.
type DestStatus struct {
	ID, Name, Kind, Template, Example string
	CapPhotos, CapMB                  int
	UsedPhotos                        int
	UsedMB                            int64
	Paused                            bool
	State, Note                       string
	Since                             time.Time
	Own                               bool
}

func adminRef(name string) string { return "admin:" + name }
func ownRef(id string) string     { return "own:" + id }

// DestinationChoices are the destinations the person may bind to a service: the administrator's, then their own.
func (h *Hub) DestinationChoices(ctx context.Context, owner uuid.UUID) []DestChoice {
	var out []DestChoice
	names := make([]string, 0, len(h.AdminDests))
	for n := range h.AdminDests {
		names = append(names, n)
	}
	sortStrings(names)
	for _, n := range names {
		out = append(out, DestChoice{Ref: adminRef(n), Name: n, Kind: h.AdminDests[n].Kind})
	}
	if h.Store != nil {
		if own, err := h.Store.OwnDestinationsOf(ctx, owner); err == nil {
			for _, d := range own {
				out = append(out, DestChoice{Ref: ownRef(d.ID), Name: d.Name, Kind: d.Kind, Own: true})
			}
		}
	}
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// OwnDestParams is a destination a person enters: a kind, its settings and its secrets (a password, a secret key).
type OwnDestParams struct {
	Name, Kind string
	Settings   map[string]string
	Secrets    map[string]string
}

// AddOwnDestination keeps a network destination a person entered, when the administrator allows it: its address is checked against the
// rules for network addresses, its secrets are sealed and never shown again, and it is not used until the person binds it to a service.
func (h *Hub) AddOwnDestination(ctx context.Context, owner immich.User, p OwnDestParams) (DestChoice, error) {
	if !h.SelfService() {
		return DestChoice{}, ErrNotJoinable
	}
	if !h.UserDests {
		return DestChoice{}, ErrUserDestsOff
	}
	kind := strings.ToLower(strings.TrimSpace(p.Kind))
	if kind == "folder" {
		return DestChoice{}, ErrNoFolderByUser
	}
	spec := dest.Spec{Name: strings.TrimSpace(p.Name), Kind: kind, Settings: map[string]string{}}
	secrets := map[string]string{}
	for k, v := range p.Settings {
		if dest.SecretKeys[k] {
			secrets[k] = v
		} else {
			spec.Settings[k] = strings.TrimSpace(v)
		}
	}
	for k, v := range p.Secrets {
		secrets[k] = v
		spec.Settings[k] = v
	}
	if err := spec.Validate(); err != nil {
		return DestChoice{}, fmt.Errorf("%w: %v", ErrBadDestination, err)
	}
	if err := dest.CheckAddress(spec, h.DestNet); err != nil {
		return DestChoice{}, fmt.Errorf("%w: %v", ErrBadDestination, err)
	}
	for _, c := range h.DestinationChoices(ctx, owner.ID) {
		if strings.EqualFold(c.Name, spec.Name) {
			return DestChoice{}, fmt.Errorf("%w: you already have a destination called %s", ErrBadDestination, spec.Name)
		}
	}
	for k := range secrets {
		delete(spec.Settings, k)
	}
	d, err := h.Store.AddOwnDestination(ctx, store.OwnDestination{OwnerID: owner.ID, Name: spec.Name, Kind: kind, Settings: spec.Settings, Secrets: secrets})
	if err != nil {
		return DestChoice{}, err
	}
	h.Log.Info("a destination was added by a person", "owner", owner.Email, "kind", kind)
	return DestChoice{Ref: ownRef(d.ID), Name: d.Name, Kind: kind, Own: true}, nil
}

// ForgetOwnDestination removes a destination a person entered, with its credentials, when no service uses it.
func (h *Hub) ForgetOwnDestination(ctx context.Context, owner immich.User, ref string) error {
	id, ok := strings.CutPrefix(ref, "own:")
	if !ok {
		return ErrNoDestination
	}
	d, err := h.Store.OwnDestination(ctx, id)
	if err != nil || d.OwnerID != owner.ID {
		return ErrNoDestination
	}
	all, _ := h.Store.AllServiceDestinations(ctx)
	for _, b := range all {
		if b.Ref == ref {
			return ErrDestinationTaken
		}
	}
	return h.Store.RemoveOwnDestination(ctx, id)
}

func (h *Hub) checkDestLimits(photos, mb int) error {
	if photos < 0 || mb < 0 || photos > 1_000_000 || mb > 10_000_000 {
		return ErrBadDestLimit
	}
	return nil
}

// AttachDestination makes a destination one of a service's receivers. From then on a tagged photo also goes there, and the original is
// moved or marked only when every receiver holds it.
func (h *Hub) AttachDestination(ctx context.Context, owner immich.User, service, ref, template string, capPhotos, capMB int) error {
	if !h.SelfService() {
		return ErrNotJoinable
	}
	v, err := h.ownedService(ctx, owner, service)
	if err != nil {
		return err
	}
	if err := h.checkDestLimits(capPhotos, capMB); err != nil {
		return err
	}
	template = strings.TrimSpace(template)
	if err := dest.ValidTemplate(template); err != nil {
		return fmt.Errorf("%w: %v", ErrBadDestination, err)
	}
	name, err := h.destName(ctx, owner.ID, ref)
	if err != nil {
		return err
	}
	have, err := h.Store.ServiceDestinations(ctx, service)
	if err != nil {
		return err
	}
	if len(have) >= maxServiceDestinations {
		return ErrTooManyDests
	}
	if _, err := h.Store.AddServiceDestination(ctx, store.ServiceDestination{Service: service, Ref: ref, Template: template, CapPhotos: capPhotos, CapMB: capMB}); err != nil {
		if errors.Is(err, store.ErrExists) {
			return ErrDestinationTaken
		}
		return err
	}
	h.event(ctx, service, owner.ID, "created", i18n.Pack("{name} made {destination} a destination of {service}", "name", owner.Name, "destination", name, "service", v.Name))
	if err := h.Reload(ctx); err != nil {
		return err
	}
	if h.Wake != nil {
		go h.Wake("destination added: " + service)
	}
	return nil
}

// destName is the name of a destination the person may use (the administrator's, or their own), or ErrNoDestination.
func (h *Hub) destName(ctx context.Context, owner uuid.UUID, ref string) (string, error) {
	switch {
	case strings.HasPrefix(ref, "admin:"):
		if _, ok := h.AdminDests[strings.TrimPrefix(ref, "admin:")]; ok {
			return strings.TrimPrefix(ref, "admin:"), nil
		}
	case strings.HasPrefix(ref, "own:"):
		if d, err := h.Store.OwnDestination(ctx, strings.TrimPrefix(ref, "own:")); err == nil && d.OwnerID == owner {
			return d.Name, nil
		}
	}
	return "", ErrNoDestination
}

// binding is one of the owner's service's destinations.
func (h *Hub) binding(ctx context.Context, owner immich.User, service, id string) (store.ServiceDestination, error) {
	if _, err := h.ownedService(ctx, owner, service); err != nil {
		return store.ServiceDestination{}, err
	}
	b, err := h.Store.ServiceDestination(ctx, id)
	if err != nil || b.Service != service {
		return store.ServiceDestination{}, ErrNoDestination
	}
	return b, nil
}

// SetDestinationPaused pauses or resumes one destination of a service; what it would receive waits, still tagged, while the others go on.
func (h *Hub) SetDestinationPaused(ctx context.Context, owner immich.User, service, id string, paused bool) error {
	b, err := h.binding(ctx, owner, service, id)
	if err != nil {
		return err
	}
	if err := h.Store.SetServiceDestinationPaused(ctx, b.ID, paused); err != nil {
		return err
	}
	word := "resumed"
	if paused {
		word = "paused"
	}
	h.event(ctx, service, owner.ID, "paused", i18n.Pack("{name} "+word+" a destination of {service}", "name", owner.Name, "service", service))
	if !paused && h.Wake != nil {
		go h.Wake("destination resumed: " + service)
	}
	return nil
}

// SetDestinationLimit sets a destination's own daily limit (photos and megabytes, 0 for none), besides the service's.
func (h *Hub) SetDestinationLimit(ctx context.Context, owner immich.User, service, id string, photos, mb int) error {
	b, err := h.binding(ctx, owner, service, id)
	if err != nil {
		return err
	}
	if err := h.checkDestLimits(photos, mb); err != nil {
		return err
	}
	return h.Store.SetServiceDestinationCap(ctx, b.ID, photos, mb)
}

// SetDestinationTemplate changes where a destination puts files; photos already delivered stay where they were put.
func (h *Hub) SetDestinationTemplate(ctx context.Context, owner immich.User, service, id, template string) error {
	b, err := h.binding(ctx, owner, service, id)
	if err != nil {
		return err
	}
	template = strings.TrimSpace(template)
	if err := dest.ValidTemplate(template); err != nil {
		return fmt.Errorf("%w: %v", ErrBadDestination, err)
	}
	return h.Store.SetServiceDestinationTemplate(ctx, b.ID, template)
}

// RemoveDestination stops a service using a destination. What it delivered stays there. Photos that were waiting for it are no longer
// waited for: a move goes on without it.
func (h *Hub) RemoveDestination(ctx context.Context, owner immich.User, service, id string) error {
	b, err := h.binding(ctx, owner, service, id)
	if err != nil {
		return err
	}
	if err := h.Store.RemoveServiceDestination(ctx, b.ID); err != nil {
		return err
	}
	h.destMu.Lock()
	delete(h.destRemotes, b.ID)
	h.destMu.Unlock()
	h.event(ctx, service, owner.ID, "deleted", i18n.Pack("{name} removed a destination of {service}", "name", owner.Name, "service", service))
	if err := h.Reload(ctx); err != nil {
		return err
	}
	if h.Wake != nil {
		go h.Wake("destination removed: " + service)
	}
	return nil
}

// DestinationsOf describes the destinations of a service for its owner's card: where each puts files (with an example), its limit and
// what it used in the last 24 hours, whether it is paused, and how it is doing.
func (h *Hub) DestinationsOf(ctx context.Context, service string) []DestStatus {
	if h.Store == nil {
		return nil
	}
	list, err := h.Store.ServiceDestinations(ctx, service)
	if err != nil {
		return nil
	}
	var out []DestStatus
	for _, b := range list {
		name, kind, own := h.refInfo(ctx, b.Ref)
		photos, bytes, _ := h.Store.DestinationUse(ctx, b.ID, h.now().Add(-syncer.LimitWindow))
		out = append(out, DestStatus{ID: b.ID, Name: name, Kind: kind, Template: b.Template, CapPhotos: b.CapPhotos, CapMB: b.CapMB, UsedPhotos: photos, UsedMB: bytes >> 20,
			Paused: b.Paused, State: b.State, Note: b.Note, Since: b.Since, Own: own,
			Example: dest.Path(b.Template, dest.Vars{Service: service, Sender: "Anna", Name: "IMG_1234.jpg", Taken: time.Date(2026, 3, 9, 12, 0, 0, 0, time.UTC)})})
	}
	return out
}

// refInfo is what a binding's reference names: the destination's name and kind (and whether it is a person's own).
func (h *Hub) refInfo(ctx context.Context, ref string) (name, kind string, own bool) {
	switch {
	case strings.HasPrefix(ref, "admin:"):
		n := strings.TrimPrefix(ref, "admin:")
		return n, h.AdminDests[n].Kind, false
	case strings.HasPrefix(ref, "own:"):
		if d, err := h.Store.OwnDestination(ctx, strings.TrimPrefix(ref, "own:")); err == nil {
			return d.Name, d.Kind, true
		}
	}
	return ref, "", false
}

// specOf is the destination a reference names, with its secrets.
func (h *Hub) specOf(ctx context.Context, ref string) (dest.Spec, error) {
	switch {
	case strings.HasPrefix(ref, "admin:"):
		if s, ok := h.AdminDests[strings.TrimPrefix(ref, "admin:")]; ok {
			return s, nil
		}
	case strings.HasPrefix(ref, "own:"):
		if d, err := h.Store.OwnDestination(ctx, strings.TrimPrefix(ref, "own:")); err == nil {
			s := dest.Spec{Name: d.Name, Kind: d.Kind, Settings: map[string]string{}}
			for k, v := range d.Settings {
				s.Settings[k] = v
			}
			for k, v := range d.Secrets {
				s.Settings[k] = v
			}
			return s, nil
		}
	}
	return dest.Spec{}, ErrNoDestination
}

// destinationsFor builds the receivers of a service from its bindings (and, for a service from the routes file, the destinations it names).
func (h *Hub) destinationsFor(ctx context.Context, service string, static []syncer.DestRef) []syncer.Remote {
	if h.Store == nil && len(static) == 0 {
		return nil
	}
	var out []syncer.Remote
	if h.Store != nil {
		bindings, err := h.Store.ServiceDestinations(ctx, service)
		if err != nil {
			return nil
		}
		for _, b := range bindings {
			if r := h.boundRemote(ctx, service, b); r != nil {
				out = append(out, r)
			}
		}
	}
	for _, d := range static {
		b := store.ServiceDestination{ID: "static:" + service + ":" + d.Name, Service: service, Ref: adminRef(d.Name), Template: d.Template}
		if r := h.boundRemote(ctx, service, b); r != nil {
			out = append(out, r)
		}
	}
	return out
}

func (h *Hub) boundRemote(ctx context.Context, service string, b store.ServiceDestination) syncer.Remote {
	name, _, _ := h.refInfo(ctx, b.Ref)
	spec, specErr := h.specOf(ctx, b.Ref)
	h.destMu.Lock()
	defer h.destMu.Unlock()
	if h.destRemotes == nil {
		h.destRemotes = map[string]*boundRemote{}
	}
	key := fmt.Sprintf("%v", spec)
	if r, ok := h.destRemotes[b.ID]; ok && r.specKey == key {
		r.mu.Lock()
		r.b = b
		r.mu.Unlock()
		return r
	}
	r := &boundRemote{h: h, service: service, name: name, b: b, specKey: key}
	if specErr != nil {
		r.buildErr = specErr
	} else if d, err := dest.Build(spec, h.DestNet); err != nil {
		r.buildErr = err
	} else {
		r.inner = &dest.Remote{Ident: b.ID, Name: name, D: d, Path: r.path}
	}
	h.destRemotes[b.ID] = r
	return r
}

// boundRemote is a service's use of a destination: the receiver that writes the files (dest.Remote), with the binding's own pause and
// daily limit, and its state told once when it changes. A paused or capped destination is not ready, so what it would receive waits, still
// tagged, while the others go on (a move waits for it).
type boundRemote struct {
	h        *Hub
	service  string
	name     string
	specKey  string
	buildErr error
	inner    *dest.Remote

	mu sync.Mutex
	b  store.ServiceDestination
}

var _ syncer.Remote = (*boundRemote)(nil)
var _ syncer.TakeBacker = (*boundRemote)(nil)
var _ syncer.Diagnoser = (*boundRemote)(nil)

func (r *boundRemote) ID() string { return r.b.ID }
func (r *boundRemote) Label() string {
	return r.name
}

func (r *boundRemote) binding() store.ServiceDestination {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.b
}

// path is where a photo goes: the binding's template filled in for the photo.
func (r *boundRemote) path(job syncer.RemoteJob, a immich.AssetResponseDto) string {
	taken := a.LocalDateTime
	if taken.IsZero() {
		taken = a.FileCreatedAt
	}
	return dest.Path(r.binding().Template, dest.Vars{Service: r.service, Sender: job.Sender, Name: path.Base(strings.ReplaceAll(a.OriginalFileName, "\\", "/")), Taken: taken})
}

var (
	errDestPaused = errors.New("paused by the service's owner")
)

func (r *boundRemote) Ready(ctx context.Context) error {
	if fresh, err := r.h.Store.ServiceDestination(ctx, r.b.ID); err == nil {
		r.mu.Lock()
		r.b = fresh
		r.mu.Unlock()
	} else if !strings.HasPrefix(r.b.ID, "static:") {
		return ErrNoDestination
	}
	b := r.binding()
	if b.Paused {
		return errDestPaused
	}
	if !strings.HasPrefix(b.ID, "static:") && (b.CapPhotos > 0 || b.CapMB > 0) {
		photos, bytes, _ := r.h.Store.DestinationUse(ctx, b.ID, r.h.now().Add(-syncer.LimitWindow))
		if b.CapPhotos > 0 && photos >= b.CapPhotos {
			return fmt.Errorf("its daily limit of %d photos is reached", b.CapPhotos)
		}
		if b.CapMB > 0 && bytes>>20 >= int64(b.CapMB) {
			return fmt.Errorf("its daily limit of %d MB is reached", b.CapMB)
		}
	}
	if r.buildErr != nil {
		r.failed(ctx, r.buildErr)
		return r.buildErr
	}
	if err := r.inner.Ready(ctx); err != nil {
		r.failed(ctx, err)
		return err
	}
	r.recovered(ctx)
	return nil
}

func (r *boundRemote) Deliver(ctx context.Context, src *immich.API, a immich.AssetResponseDto, clip *immich.AssetResponseDto, job syncer.RemoteJob) (syncer.RemoteCopy, error) {
	if r.inner == nil {
		return syncer.RemoteCopy{}, r.buildErr
	}
	cp, err := r.inner.Deliver(ctx, src, a, clip, job)
	if err != nil {
		r.failed(ctx, err)
		return cp, err
	}
	if cp.Created && !strings.HasPrefix(r.b.ID, "static:") {
		var size int64
		if a.ExifInfo != nil && a.ExifInfo.FileSizeInByte != nil {
			size = int64(*a.ExifInfo.FileSizeInByte)
		}
		if err := r.h.Store.LogDestinationUse(context.WithoutCancel(ctx), r.b.ID, size); err != nil {
			r.h.Log.Warn("could not record what a destination received", "err", err)
		}
	}
	return cp, nil
}

func (r *boundRemote) TakeBackRefs(ctx context.Context, refs []string) error {
	if r.inner == nil {
		return r.buildErr
	}
	return r.inner.TakeBackRefs(ctx, refs)
}

func (r *boundRemote) Diagnose(ctx context.Context) (string, error) {
	if r.buildErr != nil {
		return "", r.buildErr
	}
	return r.inner.Diagnose(ctx)
}

// failed records that the destination did not answer or refused a file, and tells the owner once when it begins to fail.
func (r *boundRemote) failed(ctx context.Context, err error) {
	if strings.HasPrefix(r.b.ID, "static:") {
		return
	}
	b := r.binding()
	if b.State == store.DestFailing {
		return
	}
	note := err.Error()
	if len(note) > 300 {
		note = note[:300]
	}
	if r.h.Store.SetServiceDestinationState(context.WithoutCancel(ctx), b.ID, store.DestFailing, note, r.h.now()) != nil {
		return
	}
	r.mu.Lock()
	r.b.State, r.b.Note, r.b.Since = store.DestFailing, note, r.h.now()
	r.mu.Unlock()
	owner := r.h.ownerOf(ctx, r.service)
	r.h.event(ctx, r.service, owner, "broken", i18n.Pack("{destination} of {service} is not answering: {reason}", "destination", r.name, "service", r.service, "reason", note))
	r.h.show(ctx, live.Lost, r.service, "", "service", true)
	r.h.tell(notify.Bridge, fmt.Sprintf("The destination %s of the service %q is not answering (%s); photos for it wait, still tagged.", r.name, r.service, note))
	r.h.Log.Warn("a destination is not answering; photos for it wait", "service", r.service, "destination", r.name, "err", err)
}

func (r *boundRemote) recovered(ctx context.Context) {
	if strings.HasPrefix(r.b.ID, "static:") {
		return
	}
	b := r.binding()
	if b.State != store.DestFailing {
		return
	}
	if r.h.Store.SetServiceDestinationState(context.WithoutCancel(ctx), b.ID, store.DestOK, "", time.Time{}) != nil {
		return
	}
	r.mu.Lock()
	r.b.State, r.b.Note, r.b.Since = store.DestOK, "", time.Time{}
	r.mu.Unlock()
	owner := r.h.ownerOf(ctx, r.service)
	r.h.event(ctx, r.service, owner, "restored", i18n.Pack("{destination} of {service} answers again", "destination", r.name, "service", r.service))
	r.h.show(ctx, live.Restored, r.service, "", "service", true)
	r.h.tell(notify.Bridge, fmt.Sprintf("The destination %s of the service %q answers again; waiting photos are going.", r.name, r.service))
}

// ownerOf is the person who owns a stored service (the zero id for one from the routes file).
func (h *Hub) ownerOf(ctx context.Context, service string) uuid.UUID {
	if svc := h.service(service); svc != nil {
		return svc.OwnerID
	}
	return uuid.Nil
}
