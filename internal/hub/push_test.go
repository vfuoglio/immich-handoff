package hub_test

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/push"
	"github.com/vfuoglio/immich-handoff/internal/store"
	"github.com/vfuoglio/immich-handoff/internal/syncer"
)

type sentPush struct {
	endpoint string
	title    string
	body     string
	raw      map[string]any
}

type fakePushService struct {
	mu   sync.Mutex
	sent []sentPush
	fail map[string]error // by endpoint
}

func (f *fakePushService) Send(_ context.Context, sub push.Subscription, payload []byte, _ push.Options) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail[sub.Endpoint]; err != nil {
		return err
	}
	var m map[string]any
	json.Unmarshal(payload, &m)
	title, _ := m["title"].(string)
	body, _ := m["body"].(string)
	f.sent = append(f.sent, sentPush{sub.Endpoint, title, body, m})
	return nil
}

func (f *fakePushService) to(endpoint string) []sentPush {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []sentPush
	for _, s := range f.sent {
		if s.endpoint == endpoint {
			out = append(out, s)
		}
	}
	return out
}
func (f *fakePushService) total() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.sent) }

func withPush(t *testing.T, e *env, window time.Duration) (*hub.Push, *fakePushService) {
	t.Helper()
	svc := &fakePushService{fail: map[string]error{}}
	p := &hub.Push{Store: e.st, Sender: svc, Key: "PUBLIC-KEY", Window: window}
	e.h.Push = p
	t.Cleanup(p.Close)
	return p, svc
}

func device(t *testing.T, endpoint, lang, mode string) hub.PushDeviceInput {
	t.Helper()
	k, _ := ecdh.P256().GenerateKey(rand.Reader)
	return hub.PushDeviceInput{Endpoint: endpoint, P256dh: base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes()),
		Auth: base64.RawURLEncoding.EncodeToString(make([]byte, 16)), Lang: lang, Mode: mode, UserAgent: "Mozilla/5.0 (X11; Linux x86_64; rv:130.0) Gecko/20100101 Firefox/130.0"}
}

func subscribe(t *testing.T, e *env, user uuid.UUID, endpoint, lang, mode string) string {
	t.Helper()
	id, err := e.h.PushSubscribe(context.Background(), user, device(t, endpoint, lang, mode))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

const (
	annPhone = "https://fcm.googleapis.com/fcm/send/ann-phone"
	annLap   = "https://updates.push.services.mozilla.com/wpush/v2/ann-laptop"
	bobPhone = "https://web.push.apple.com/bob-phone"
)

func TestDevicesGetTheLinesTheirPageShowsInTheirOwnLanguageAndMode(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	_, svc := withPush(t, e, 200*time.Millisecond)
	subscribe(t, e, annID, annPhone, "de", store.MailAll)
	subscribe(t, e, annID, annLap, "en", store.MailNeeds)
	subscribe(t, e, bobID, bobPhone, "en", store.MailNeeds)
	subscribe(t, e, uuid.New(), "https://fcm.googleapis.com/fcm/send/stranger", "en", store.MailAll) // takes part in nothing

	e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: "party", Tag: "party", Policy: "open", Visibility: ""})
	if err := e.h.Join(ctx, e.sess, bob, "party", "", ""); err != nil {
		t.Fatal(err)
	}
	e.f.deleteKeyOf("bob")
	e.h.CheckKeys(ctx)

	waitFor(t, "the phone and the laptop and bob", func() bool {
		return len(svc.to(annPhone)) == 1 && len(svc.to(annLap)) == 1 && len(svc.to(bobPhone)) == 1
	})
	time.Sleep(400 * time.Millisecond)
	if svc.total() != 3 {
		t.Fatalf("one notification per device, none for the stranger: %d", svc.total())
	}
	phone := svc.to(annPhone)[0]
	if phone.title != "Immich Handoff" || !strings.Contains(phone.body, "2 Dinge") && !strings.Contains(phone.body, "Dinge") {
		t.Errorf("Ann's phone is in German and takes everything (joined and cut): %q", phone.body)
	}
	if lap := svc.to(annLap)[0]; strings.Contains(lap.body, "joined") || !strings.Contains(lap.body, "cut") {
		t.Errorf("the laptop is set to what needs her only (the cut connection, not the joining): %q", lap.body)
	}
	if b := svc.to(bobPhone)[0]; !strings.Contains(b.body, "cut") {
		t.Errorf("Bob is told his connection was cut: %q", b.body)
	}
}

func TestADeviceTheServiceSaysIsGoneIsForgottenAndOneThatKeepsFailingIsGivenUp(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	p, svc := withPush(t, e, 20*time.Millisecond)
	p.Now = func() time.Time { return time.Now().Add(3 * time.Minute) } // the devices are old enough to be forgotten
	subscribe(t, e, annID, annPhone, "en", store.MailAll)
	subscribe(t, e, annID, annLap, "en", store.MailAll)
	svc.fail[annPhone] = push.ErrGone
	svc.fail[annLap] = push.ErrTemporary

	count := func() int { l, _ := e.st.PushSubs(ctx, annID); return len(l) }
	p.Tell(ctx, []uuid.UUID{annID}, "joined", "x")
	waitFor(t, "the gone device to be forgotten", func() bool { return count() == 1 })
	for i := 0; i < 2; i++ {
		time.Sleep(60 * time.Millisecond)
		p.Tell(ctx, []uuid.UUID{annID}, "joined", "x")
	}
	waitFor(t, "the failing device to be given up on after three failures", func() bool { return count() == 0 })
}

func TestArrivalsAreToldToReceiversAndOwnerAndRefusalsToTheSender(t *testing.T) {
	e := setup(t, false)
	_, svc := withPush(t, e, 30*time.Millisecond)
	carol := uuid.New()
	const carolPhone = "https://fcm.googleapis.com/fcm/send/carol"
	subscribe(t, e, carol, carolPhone, "en", store.MailNeeds) // the sender, who only wants what needs her
	subscribe(t, e, bobID, bobPhone, "en", store.MailAll)     // a receiver
	subscribe(t, e, annID, annPhone, "en", store.MailNeeds)   // the owner, who only wants what needs her

	service := &syncer.Service{Name: "holiday", OwnerID: annID, Receivers: []*syncer.Account{{Name: "Bob", UserID: bobID}}}
	sender := &syncer.Account{Name: "Carol", UserID: carol}
	e.h.HandedOver(service, sender, 12, 0)
	waitFor(t, "bob", func() bool { return len(svc.to(bobPhone)) == 1 })
	time.Sleep(150 * time.Millisecond)
	if b := svc.to(bobPhone)[0].body; b != "12 photos arrived from Carol in holiday" {
		t.Errorf("%q", b)
	}
	if len(svc.to(annPhone)) != 0 || len(svc.to(carolPhone)) != 0 {
		t.Errorf("an arrival is news: the owner on 'only what needs me' and the sender hear nothing (%d, %d)", len(svc.to(annPhone)), len(svc.to(carolPhone)))
	}

	e.h.HandedOver(service, sender, 1, 3)
	waitFor(t, "carol is told about the refusals", func() bool { return len(svc.to(carolPhone)) == 1 })
	if b := svc.to(carolPhone)[0].body; !strings.Contains(b, "3 photos were not handed over to holiday") {
		t.Errorf("%q", b)
	}
	waitFor(t, "bob's second", func() bool { return len(svc.to(bobPhone)) == 2 })
	if b := svc.to(bobPhone)[1].body; b != "1 photo arrived from Carol in holiday" {
		t.Errorf("%q", b)
	}
	if len(svc.to(bobPhone)) != 2 {
		t.Error("bob must not be told about the sender's refusals")
	}
}

func TestAPersonCanOnlyAddUsableDevicesAndManageTheirOwn(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	if c := e.h.PushCard(ctx, annID); c.Available {
		t.Fatal("no card without pushes")
	}
	if _, err := e.h.PushSubscribe(ctx, annID, device(t, annPhone, "en", store.MailAll)); err != hub.ErrPushOff {
		t.Errorf("%v", err)
	}
	_, svc := withPush(t, e, time.Minute)

	bad := func(mut func(*hub.PushDeviceInput), want error) {
		t.Helper()
		in := device(t, annPhone, "en", store.MailAll)
		mut(&in)
		if _, err := e.h.PushSubscribe(ctx, annID, in); err != want {
			t.Errorf("got %v, want %v", err, want)
		}
	}
	bad(func(in *hub.PushDeviceInput) { in.Endpoint = "https://evil.test/x" }, hub.ErrBadPushHost)
	bad(func(in *hub.PushDeviceInput) { in.Endpoint = "http://fcm.googleapis.com/x" }, hub.ErrBadPushHost)
	bad(func(in *hub.PushDeviceInput) { in.Endpoint = "https://127.0.0.1/x" }, hub.ErrBadPushHost)
	bad(func(in *hub.PushDeviceInput) { in.P256dh = "AAAA" }, hub.ErrBadPushDevice)
	bad(func(in *hub.PushDeviceInput) { in.Auth = "AAAA" }, hub.ErrBadPushDevice)
	bad(func(in *hub.PushDeviceInput) { in.Auth = "not base64!" }, hub.ErrBadPushDevice)
	bad(func(in *hub.PushDeviceInput) { in.Mode = "loud" }, hub.ErrBadMailMode)
	if l, _ := e.st.PushSubs(ctx, annID); len(l) != 0 {
		t.Fatal("a refused device was stored")
	}

	id := subscribe(t, e, annID, annPhone, "xx", store.MailAll) // an unknown language becomes English
	c := e.h.PushCard(ctx, annID)
	if !c.Available || c.Key != "PUBLIC-KEY" || len(c.Devices) != 1 || c.Devices[0].ID != id || c.Devices[0].Label != "Firefox on Linux" {
		t.Fatalf("%+v", c)
	}
	if l, _ := e.st.PushSubs(ctx, annID); l[0].Lang != "en" {
		t.Errorf("%q", l[0].Lang)
	}
	if err := e.h.PushSetMode(ctx, bobID, id, store.MailNeeds); err != hub.ErrNoDevice {
		t.Errorf("bob changed ann's device: %v", err)
	}
	if err := e.h.PushSetMode(ctx, annID, id, "loud"); err != hub.ErrBadMailMode {
		t.Errorf("%v", err)
	}
	if err := e.h.PushSetMode(ctx, annID, id, store.MailNeeds); err != nil {
		t.Fatal(err)
	}

	// the test push, in the device's language, once a minute, and its failure is reported
	if err := e.h.PushTest(ctx, bob, id); err != hub.ErrNoDevice {
		t.Errorf("bob tested ann's device: %v", err)
	}
	if err := e.h.PushTest(ctx, ann, id); err != nil {
		t.Fatal(err)
	}
	if got := svc.to(annPhone); len(got) != 1 || !strings.Contains(got[0].body, "Notifications work") {
		t.Errorf("%+v", got)
	}
	if err := e.h.PushTest(ctx, ann, id); err != hub.ErrPushTestSoon {
		t.Errorf("%v", err)
	}

	if err := e.h.PushRemove(ctx, bobID, id); err != hub.ErrNoDevice {
		t.Errorf("bob removed ann's device: %v", err)
	}
	if err := e.h.PushRemove(ctx, annID, id); err != nil {
		t.Fatal(err)
	}
	if err := e.h.PushRemove(ctx, annID, id); err != hub.ErrNoDevice {
		t.Errorf("%v", err)
	}
}

func TestAFailedTestPushSaysSoAndTheTenthDeviceIsTheLast(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	_, svc := withPush(t, e, time.Minute)
	id := subscribe(t, e, annID, annPhone, "en", store.MailAll)
	svc.fail[annPhone] = errors.New("push: the push service refused it (403)")
	err := e.h.PushTest(ctx, ann, id)
	if !errors.Is(err, hub.ErrPushSend) || !hub.IsUserError(err) || !strings.Contains(err.Error(), "403") {
		t.Errorf("%v", err)
	}
	for i := 0; i < 9; i++ {
		subscribe(t, e, annID, "https://fcm.googleapis.com/fcm/send/"+uuid.NewString(), "en", store.MailAll)
	}
	if _, err := e.h.PushSubscribe(ctx, annID, device(t, "https://fcm.googleapis.com/fcm/send/eleventh", "en", store.MailAll)); err != hub.ErrTooManyDevices {
		t.Errorf("%v", err)
	}
}

func TestADeviceMadeAMomentAgoIsKeptEvenIfItsPushServiceDoesNotKnowItYet(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	p, svc := withPush(t, e, 10*time.Millisecond)
	var later atomic.Int64 // how far the clock has moved on, in nanoseconds
	p.Now = func() time.Time { return time.Now().Add(time.Duration(later.Load())) }
	subscribe(t, e, annID, annPhone, "en", store.MailAll)
	svc.mu.Lock()
	svc.fail[annPhone] = push.ErrGone
	svc.mu.Unlock()
	count := func() int { l, _ := e.st.PushSubs(ctx, annID); return len(l) }

	p.Tell(ctx, []uuid.UUID{annID}, "joined", "x")
	time.Sleep(150 * time.Millisecond)
	if count() != 1 {
		t.Fatal("a device that is seconds old was forgotten on its first 'gone'")
	}
	later.Store(int64(3 * time.Minute)) // later, the same answer means it really is gone
	p.Tell(ctx, []uuid.UUID{annID}, "joined", "x")
	waitFor(t, "the old device to be forgotten", func() bool { return count() == 0 })
}
