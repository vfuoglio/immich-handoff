package hub_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/dest"
	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
	"github.com/vfuoglio/immich-handoff/internal/syncer"
)

// refusing is a receiver that does not allow what it was given to be taken back.
type refusing struct{ *dest.Remote }

func (refusing) TakeBackRefs(context.Context, []string) error { return syncer.ErrCannotTakeBack }

// nasEnv makes a service whose photos go only to a destination, owned by ann, that bob has joined.
func nasEnv(t *testing.T, remote func(m *dest.Memory) syncer.Remote) (*env, context.Context, *dest.Memory, store.Link) {
	t.Helper()
	e, ctx := setup(t, true), context.Background()
	e.h.ReceiverDelete = true
	keys := len(e.f.keys)
	m := &dest.Memory{}
	e.h.Destinations = func(service string) []syncer.Remote {
		if service == "nas" {
			return []syncer.Remote{remote(m)}
		}
		return nil
	}
	if err := e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: "nas", Tag: "to-nas", Policy: store.PolicyOpen, DestinationOnly: true}); err != nil {
		t.Fatal(err)
	}
	if len(e.f.keys) != keys {
		t.Fatalf("a service with only destinations makes no key in the owner's Immich: %d keys before, %d after", keys, len(e.f.keys))
	}
	if err := e.h.Join(ctx, e.sess, bob, "nas", "", ""); err != nil {
		t.Fatal(err)
	}
	links, _ := e.st.Links(ctx)
	for _, l := range links {
		if l.Service == "nas" && l.ImmichUserID == bobID {
			return e, ctx, m, l
		}
	}
	t.Fatal("bob has no link")
	return nil, nil, nil, store.Link{}
}

func plain(m *dest.Memory) syncer.Remote { return &dest.Remote{Ident: "nas", Name: "the NAS", D: m} }

func TestAServiceWithOnlyDestinationsHasNoImmichReceiverAndNoKeyAndCannotBeAPool(t *testing.T) {
	e, ctx, _, _ := nasEnv(t, plain)
	var svc *syncer.Service
	for _, s := range e.h.Services() {
		if s.Label() == "nas" {
			svc = s
		}
	}
	if svc == nil || len(svc.Receivers) != 0 || len(svc.Remotes) != 1 || svc.Remotes[0].ID() != "nas" || svc.Broken != "" || len(svc.Senders) != 1 {
		t.Fatalf("%+v", svc)
	}
	stored, _ := e.st.Services(ctx)
	var found bool
	for _, v := range stored {
		if v.Name == "nas" {
			found = v.DestOnly && v.Key == "" && v.KeyID == uuid.Nil
		}
	}
	if !found {
		t.Errorf("stored without a key: %+v", stored)
	}
	if err := e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: "pool2", Tag: "to-pool2", DestinationOnly: true, Pool: true}); !errors.Is(err, hub.ErrDestOnly) || !hub.IsUserError(err) {
		t.Errorf("no pool: %v", err)
	}
	if err := e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: "alb", Tag: "to-alb", DestinationOnly: true, Album: "Trips"}); !errors.Is(err, hub.ErrDestOnly) {
		t.Errorf("no album: %v", err)
	}
	// and it can be deleted again with no key to remove
	if err := e.h.DeleteService(ctx, e.ann, ann, "nas"); err != nil {
		t.Errorf("delete: %v", err)
	}
}

func TestTakingBackFromADestinationDeletesTheFilesAndSettlesTheHandOver(t *testing.T) {
	e, ctx, m, link := nasEnv(t, plain)
	sender := uuid.New()
	m.Put(ctx, "bob/IMG_1.jpg", bytes.NewReader([]byte("x")), 1)
	m.Put(ctx, "bob/IMG_1.mov", bytes.NewReader([]byte("y")), 1)
	e.st.AddRemoteHandoverItem(ctx, link.ID, "b1", sender, "nas", "bob/IMG_1.jpg", false)
	e.st.AddRemoteHandoverItem(ctx, link.ID, "b1", uuid.New(), "nas", "bob/IMG_1.mov", true)
	if n, _ := e.h.TakeBackOffer(ctx, bobID, "nas"); n != 1 {
		t.Errorf("a service with only destinations offers to take back what it handed over, counting the photo once and not its clip: %d", n)
	}
	res, err := e.h.TakeBack(ctx, bob, "nas")
	if err != nil {
		t.Fatal(err)
	}
	if res.RemoteTaken != 1 || res.Taken != 1 || len(m.Refs()) != 0 {
		t.Errorf("both files are deleted and one photo counted: %+v %v", res, m.Refs())
	}
	if again, err := e.st.LastHandover(ctx, link.ID, time.Now().Add(-time.Hour)); err != nil || len(again) != 0 {
		t.Errorf("nothing is left to take back: %v %v", again, err)
	}
	if e.st.Delivered(ctx, sender, "nas") {
		t.Error("a photo taken back is not held any more, so it can be delivered again")
	}
}

func TestADestinationThatDoesNotAllowTakeBackKeepsItsCopiesAndSaysSo(t *testing.T) {
	e, ctx, m, link := nasEnv(t, func(m *dest.Memory) syncer.Remote { return refusing{&dest.Remote{Ident: "nas", Name: "the NAS", D: m}} })
	m.Put(ctx, "IMG_1.jpg", bytes.NewReader([]byte("x")), 1)
	e.st.AddRemoteHandoverItem(ctx, link.ID, "b1", uuid.New(), "nas", "IMG_1.jpg", false)
	res, err := e.h.TakeBack(ctx, bob, "nas")
	if err != nil || res.RemoteCannot != 1 || res.RemoteTaken != 0 || len(m.Refs()) != 1 {
		t.Fatalf("%+v %v %v", res, err, m.Refs())
	}
}

func TestADestinationThatCannotBeReachedStopsTheTakeBackBeforeAnythingChanges(t *testing.T) {
	e, ctx, m, link := nasEnv(t, plain)
	m.Put(ctx, "IMG_1.jpg", bytes.NewReader([]byte("x")), 1)
	e.st.AddRemoteHandoverItem(ctx, link.ID, "b1", uuid.New(), "nas", "IMG_1.jpg", false)
	m.Fail = errors.New("unreachable")
	if _, err := e.h.TakeBack(ctx, bob, "nas"); err == nil {
		t.Fatal("it cannot reach the destination")
	}
	if items, _ := e.st.LastHandover(ctx, link.ID, time.Now().Add(-time.Hour)); len(items) != 1 {
		t.Errorf("the hand-over is still there to take back later: %v", items)
	}
}

// ---- destinations the administrator names and people add ----

var folders = map[string]*dest.Memory{} // the root of a "folder" destination -> what it holds, for these tests

func init() {
	dest.Register("folder", func(s dest.Spec, n dest.Net) (dest.Destination, error) {
		m := folders[s.Settings["root"]]
		if m == nil {
			m = &dest.Memory{}
			folders[s.Settings["root"]] = m
		}
		return m, nil
	})
}

func folder(name, root string) dest.Spec {
	return dest.Spec{Name: name, Kind: "folder", Settings: map[string]string{"root": root}}
}

// destEnv has ann owning a service "nas" with a member bob, and the administrator's destinations "attic" and "archive".
func destEnv(t *testing.T) (*env, context.Context) {
	t.Helper()
	e, ctx := setup(t, true), context.Background()
	e.h.AdminDests = map[string]dest.Spec{"attic": folder("attic", "/d/attic-"+t.Name()), "archive": folder("archive", "/d/archive-"+t.Name())}
	if err := e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: "nas", Tag: "to-nas", Policy: store.PolicyOpen, DestinationOnly: true}); err != nil {
		t.Fatal(err)
	}
	return e, ctx
}

func nas(e *env) *syncer.Service {
	for _, s := range e.h.Services() {
		if s.Label() == "nas" {
			return s
		}
	}
	return nil
}

func TestAnOwnerBindsTheAdministratorsDestinationsToAServiceWithATemplateAndLimits(t *testing.T) {
	e, ctx := destEnv(t)
	if c := e.h.DestinationChoices(ctx, annID); len(c) != 2 || c[0].Name != "archive" || c[1].Name != "attic" || c[0].Own {
		t.Fatalf("the administrator's, by name: %+v", c)
	}
	if err := e.h.AttachDestination(ctx, ann, "nas", "admin:attic", "{yyyy}/{mm}/{name}", 100, 2000); err != nil {
		t.Fatal(err)
	}
	if got := nas(e); len(got.Remotes) != 1 || got.Remotes[0].Label() != "attic" {
		t.Fatalf("the service now has it as a receiver: %+v", got.Remotes)
	}
	for name, err := range map[string]error{
		"again":          e.h.AttachDestination(ctx, ann, "nas", "admin:attic", "", 0, 0),
		"unknown":        e.h.AttachDestination(ctx, ann, "nas", "admin:nope", "", 0, 0),
		"a bad limit":    e.h.AttachDestination(ctx, ann, "nas", "admin:archive", "", -1, 0),
		"a template":     e.h.AttachDestination(ctx, ann, "nas", "admin:archive", "../x/{name}", 0, 0),
		"someone else's": e.h.AttachDestination(ctx, bob, "nas", "admin:archive", "", 0, 0),
	} {
		if err == nil || !hub.IsUserError(err) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := e.h.AttachDestination(ctx, bob, "nas", "admin:archive", "", 0, 0); !errors.Is(err, hub.ErrNotOwner) {
		t.Errorf("only the owner: %v", err)
	}
	st := e.h.DestinationsOf(ctx, "nas")
	if len(st) != 1 || st[0].Name != "attic" || st[0].Kind != "folder" || st[0].CapPhotos != 100 || st[0].Example != "2026/03/IMG_1234.jpg" {
		t.Fatalf("%+v", st)
	}
	if err := e.h.SetDestinationTemplate(ctx, ann, "nas", st[0].ID, "{service}/{name}"); err != nil {
		t.Fatal(err)
	}
	if err := e.h.SetDestinationLimit(ctx, ann, "nas", st[0].ID, 5, 0); err != nil {
		t.Fatal(err)
	}
	if again := e.h.DestinationsOf(ctx, "nas"); again[0].Example != "nas/IMG_1234.jpg" || again[0].CapPhotos != 5 {
		t.Errorf("%+v", again)
	}
	if err := e.h.RemoveDestination(ctx, ann, "nas", st[0].ID); err != nil || len(nas(e).Remotes) != 0 {
		t.Errorf("removed: %v %v", err, nas(e).Remotes)
	}
}

func TestAPersonAddsANetworkDestinationOnlyWhenAllowedWithItsSecretsSealedAndItsAddressChecked(t *testing.T) {
	e, ctx := destEnv(t)
	webdav := hub.OwnDestParams{Name: "Nextcloud", Kind: "webdav", Settings: map[string]string{"url": "https://cloud.example.org/remote.php/dav/files/ann", "user": "ann"}, Secrets: map[string]string{"password": "hunter2"}}
	if _, err := e.h.AddOwnDestination(ctx, ann, webdav); !errors.Is(err, hub.ErrUserDestsOff) || !hub.IsUserError(err) {
		t.Fatalf("off by default: %v", err)
	}
	e.h.UserDests = true
	if _, err := e.h.AddOwnDestination(ctx, ann, hub.OwnDestParams{Name: "mine", Kind: "folder", Settings: map[string]string{"root": "/etc"}}); !errors.Is(err, hub.ErrNoFolderByUser) {
		t.Errorf("a person never types a folder: %v", err)
	}
	private := webdav
	private.Name, private.Settings = "LAN", map[string]string{"url": "https://192.168.1.20/dav", "user": "ann"}
	if _, err := e.h.AddOwnDestination(ctx, ann, private); !errors.Is(err, hub.ErrBadDestination) {
		t.Errorf("a private address is refused unless the administrator allows it: %v", err)
	}
	if _, err := e.h.AddOwnDestination(ctx, ann, hub.OwnDestParams{Name: "x", Kind: "sftp", Settings: map[string]string{"host": "nas.example.org"}}); !errors.Is(err, hub.ErrBadDestination) {
		t.Errorf("an sftp destination needs a user and a password or a key: %v", err)
	}
	c, err := e.h.AddOwnDestination(ctx, ann, webdav)
	if err != nil || !c.Own || c.Kind != "webdav" {
		t.Fatalf("%+v %v", c, err)
	}
	stored, _ := e.st.OwnDestinationsOf(ctx, annID)
	if len(stored) != 1 || stored[0].Secrets["password"] != "hunter2" || stored[0].Settings["password"] != "" || stored[0].Settings["url"] == "" {
		t.Errorf("the secret is kept apart and sealed: %+v", stored)
	}
	if _, err := e.h.AddOwnDestination(ctx, ann, webdav); !errors.Is(err, hub.ErrBadDestination) {
		t.Errorf("a name is used once: %v", err)
	}
	if list := e.h.DestinationChoices(ctx, annID); len(list) != 3 || !list[2].Own {
		t.Errorf("it is one of her choices: %+v", list)
	}
	if list := e.h.DestinationChoices(ctx, bobID); len(list) != 2 {
		t.Errorf("and only hers: %+v", list)
	}
	if err := e.h.AttachDestination(ctx, bob, "nas", c.Ref, "", 0, 0); err == nil {
		t.Error("another person cannot use it")
	}
	// with the switch for private addresses it is accepted
	e.h.DestNet.AllowPrivate = true
	if _, err := e.h.AddOwnDestination(ctx, ann, private); err != nil {
		t.Errorf("allowed by the administrator: %v", err)
	}
	// in use it cannot be forgotten; unused it can
	if err := e.h.AttachDestination(ctx, ann, "nas", c.Ref, "", 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := e.h.ForgetOwnDestination(ctx, ann, c.Ref); !errors.Is(err, hub.ErrDestinationTaken) {
		t.Errorf("in use: %v", err)
	}
	if st := e.h.DestinationsOf(ctx, "nas"); len(st) != 1 || !st[0].Own {
		t.Fatal(st)
	}
	if err := e.h.RemoveDestination(ctx, ann, "nas", e.h.DestinationsOf(ctx, "nas")[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := e.h.ForgetOwnDestination(ctx, ann, c.Ref); err != nil {
		t.Errorf("unused: %v", err)
	}
}

func TestADestinationOfAServiceIsPausableHasItsOwnDailyLimitAndTellsWhenItFailsAndWhenItIsBack(t *testing.T) {
	e, ctx := destEnv(t)
	if err := e.h.AttachDestination(ctx, ann, "nas", "admin:attic", "", 2, 0); err != nil {
		t.Fatal(err)
	}
	r := nas(e).Remotes[0]
	id := e.h.DestinationsOf(ctx, "nas")[0].ID
	if err := r.Ready(ctx); err != nil {
		t.Fatalf("ready: %v", err)
	}
	// its own pause: it is not ready, which holds only what it would receive
	if err := e.h.SetDestinationPaused(ctx, ann, "nas", id, true); err != nil {
		t.Fatal(err)
	}
	if err := r.Ready(ctx); err == nil || !strings.Contains(err.Error(), "paused") {
		t.Errorf("paused: %v", err)
	}
	e.h.SetDestinationPaused(ctx, ann, "nas", id, false)
	// its own limit
	e.st.LogDestinationUse(ctx, id, 10)
	if err := r.Ready(ctx); err != nil {
		t.Errorf("one of two: %v", err)
	}
	e.st.LogDestinationUse(ctx, id, 10)
	if err := r.Ready(ctx); err == nil || !strings.Contains(err.Error(), "daily limit of 2 photos") {
		t.Errorf("the limit: %v", err)
	}
	if st := e.h.DestinationsOf(ctx, "nas"); st[0].UsedPhotos != 2 || st[0].State != store.DestOK {
		t.Errorf("a limit is not a failure: %+v", st[0])
	}
	e.h.SetDestinationLimit(ctx, ann, "nas", id, 0, 0)
	// failing: told once, with the reason; back: told once
	m := folders["/d/attic-"+t.Name()]
	m.Fail = errors.New("the share is not mounted")
	events := func() int {
		n := 0
		evs, _ := e.h.RecentEvents(ctx, 100)
		for _, ev := range evs {
			if strings.Contains(ev.Text, "attic") && (ev.Kind == "broken" || ev.Kind == "restored") {
				n++
			}
		}
		return n
	}
	before := events()
	for i := 0; i < 3; i++ {
		if err := r.Ready(ctx); err == nil {
			t.Fatal("it is down")
		}
	}
	st := e.h.DestinationsOf(ctx, "nas")[0]
	if st.State != store.DestFailing || !strings.Contains(st.Note, "not mounted") || st.Since.IsZero() {
		t.Errorf("%+v", st)
	}
	if events()-before != 1 {
		t.Errorf("told once however many passes find it down: %d", events()-before)
	}
	m.Fail = nil
	if err := r.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	if st := e.h.DestinationsOf(ctx, "nas")[0]; st.State != store.DestOK || st.Note != "" || events()-before != 2 {
		t.Errorf("back, told once more: %+v %d", st, events()-before)
	}
}

func TestAPhotoDeliveredToABoundDestinationLandsWhereTheTemplateSaysAndCountsForItsLimit(t *testing.T) {
	e, ctx := destEnv(t)
	e.h.AttachDestination(ctx, ann, "nas", "admin:attic", "", 0, 0)
	id := uuid.New()
	e.f.assetName = map[string]string{id.String(): "IMG_9.jpg"}
	e.f.thumbs = map[string][]byte{"IMG_9": []byte("the bytes")}
	r := nas(e).Remotes[0]
	taken := time.Date(2026, 3, 9, 12, 0, 0, 0, time.UTC)
	cp, err := r.Deliver(ctx, e.sess, immich.AssetResponseDto{Id: id, OriginalFileName: "IMG_9.jpg", LocalDateTime: taken}, nil, syncer.RemoteJob{Sender: "Bob"})
	if err != nil || cp.Ref != "nas/Bob/2026/03/IMG_9.jpg" || !cp.Created {
		t.Fatalf("%+v %v", cp, err)
	}
	if string(folders["/d/attic-"+t.Name()].Content(cp.Ref)) != "the bytes" {
		t.Error("the bytes are there")
	}
	if st := e.h.DestinationsOf(ctx, "nas")[0]; st.UsedPhotos != 1 {
		t.Errorf("it counts toward the destination's limit: %+v", st)
	}
	if err := e.h.SetDestinationPaused(ctx, bob, "nas", e.h.DestinationsOf(ctx, "nas")[0].ID, true); !errors.Is(err, hub.ErrNotOwner) {
		t.Errorf("only the owner pauses: %v", err)
	}
}

func TestADestinationOfAKindThisVersionCannotWriteToSaysSoInsteadOfBreakingTheService(t *testing.T) {
	e, ctx := destEnv(t)
	e.h.AdminDests["cloud"] = dest.Spec{Name: "cloud", Kind: "sftp", Settings: map[string]string{"host": "h", "user": "u", "password": "p"}}
	if err := e.h.AttachDestination(ctx, ann, "nas", "admin:cloud", "", 0, 0); err != nil {
		t.Fatal(err)
	}
	r := nas(e).Remotes[0]
	if err := r.Ready(ctx); !errors.Is(err, dest.ErrKindUnavailable) {
		t.Errorf("%v", err)
	}
	if st := e.h.DestinationsOf(ctx, "nas")[0]; st.State != store.DestFailing || !strings.Contains(st.Note, "cannot hand over") {
		t.Errorf("the owner's card says why: %+v", st)
	}
}
