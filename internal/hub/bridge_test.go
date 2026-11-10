package hub_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vfuoglio/immich-handoff/internal/bridge"
	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

func TestAServiceWithABridgeGetsItsRemoteReceiver(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	if err := e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: "holiday", Tag: "hol", Policy: "approval"}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetBridge(ctx, store.Bridge{Service: "holiday", Address: "https://photos.example.org", Key: "REMOTE", DisplayName: "Aunt Lia"}); err != nil {
		t.Fatal(err)
	}

	// off (the default): the service must not run at all, because the owner promised a copy on the other server too
	e.h.Bridge = bridge.Settings{}
	if err := e.h.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if svc := service(e, "holiday"); svc.Remote != nil || svc.Broken == "" || len(svc.Receivers) != 0 {
		t.Fatalf("bridges off: %+v", svc)
	}

	e.h.Bridge = bridge.Settings{Enabled: true}
	if err := e.h.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	svc := service(e, "holiday")
	if svc.Remote == nil || svc.Remote.Label() != "Aunt Lia" || svc.Broken != "" || len(svc.Receivers) != 1 {
		t.Fatalf("bridges on: %+v", svc)
	}
	// the same receiver is kept across reloads (its connections are reused), and replaced with a new key
	first := svc.Remote
	e.h.Reload(ctx)
	if service(e, "holiday").Remote != first {
		t.Error("a reload made a new receiver")
	}
	e.st.SetBridge(ctx, store.Bridge{Service: "holiday", Address: "https://photos.example.org", Key: "NEW", DisplayName: "Aunt Lia"})
	e.h.Reload(ctx)
	if service(e, "holiday").Remote == first {
		t.Error("a new key must make a new receiver")
	}

	// a service without a bridge is untouched, and removing the bridge removes the receiver
	e.st.RemoveBridge(ctx, "holiday")
	e.h.Reload(ctx)
	if service(e, "holiday").Remote != nil || service(e, "family").Remote != nil {
		t.Error("no bridge, no remote")
	}
}

// otherServer answers what a check asks, and can be made to fail.
type otherServer struct {
	*httptest.Server
	mu    sync.Mutex
	down  int
	valid string
	hits  int
}

func newOtherServer(t *testing.T) *otherServer {
	t.Helper()
	o := &otherServer{valid: "REMOTE"}
	o.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o.mu.Lock()
		defer o.mu.Unlock()
		o.hits++
		if o.down != 0 {
			http.Error(w, "down", o.down)
			return
		}
		switch r.URL.Path {
		case "/api/server/version":
			w.Write([]byte(`{"major":3,"minor":2,"patch":0}`))
		case "/api/api-keys/me":
			if r.Header.Get("x-api-key") != o.valid {
				http.Error(w, "no", 401)
				return
			}
			w.Write([]byte(`{"permissions":["user.read","tag.create","tag.asset","asset.upload"]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(o.Close)
	return o
}

func (o *otherServer) count() int { o.mu.Lock(); defer o.mu.Unlock(); return o.hits }

func withBridge(t *testing.T, o *otherServer) (*env, *bridge.Remote) {
	t.Helper()
	e, ctx := setup(t, false), context.Background()
	e.h.Bridge = bridge.Settings{Enabled: true, Policy: bridge.Policy{AllowPrivate: true}}
	if err := e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: "holiday", Tag: "hol", Policy: "approval"}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetBridge(ctx, store.Bridge{Service: "holiday", Address: o.URL, Key: "REMOTE", DisplayName: "Aunt Lia"}); err != nil {
		t.Fatal(err)
	}
	if err := e.h.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	return e, service(e, "holiday").Remote.(*bridge.Remote)
}

func state(t *testing.T, e *env) store.Bridge {
	t.Helper()
	b, err := e.st.Bridge(context.Background(), "holiday")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAHealthyBridgeStaysOkAndRemembersTheVersion(t *testing.T) {
	o := newOtherServer(t)
	e, r := withBridge(t, o)
	if err := r.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b := state(t, e); b.State != store.BridgeOK || b.Version != "v3.2.0" {
		t.Fatalf("%+v", b)
	}
}

func TestARevokedKeyIsRecordedOnceAndNotTriedAgain(t *testing.T) {
	o := newOtherServer(t)
	e, r := withBridge(t, o)
	var told []string
	e.h.Notify = func(m string) { told = append(told, m) }
	ctx := context.Background()
	o.valid = "SOMETHING ELSE"

	if err := r.Ready(ctx); !errors.Is(err, bridge.ErrRevoked) {
		t.Fatalf("%v", err)
	}
	if b := state(t, e); b.State != store.BridgeRevoked || b.Note == "" {
		t.Fatalf("%+v", b)
	}
	hits := o.count()
	for i := 0; i < 3; i++ {
		if err := r.Ready(ctx); !errors.Is(err, bridge.ErrRevoked) {
			t.Fatalf("%v", err)
		}
	}
	if o.count() != hits {
		t.Errorf("a revoked key was tried again: %d requests", o.count()-hits)
	}
	if len(told) != 1 {
		t.Errorf("the owner must be told once, got %d: %v", len(told), told)
	}

	// a new key ends it
	e.st.SetBridge(ctx, store.Bridge{Service: "holiday", Address: o.URL, Key: "SOMETHING ELSE", DisplayName: "Aunt Lia"})
	e.h.Reload(ctx)
	r = service(e, "holiday").Remote.(*bridge.Remote)
	if err := r.Ready(ctx); err != nil {
		t.Fatalf("after a new key: %v", err)
	}
	if state(t, e).State != store.BridgeOK {
		t.Fatal("not ok again")
	}
}

func TestAnUnreachableServerBacksOffAndRecovers(t *testing.T) {
	o := newOtherServer(t)
	e, r := withBridge(t, o)
	var told []string
	var woken []string
	e.h.Notify = func(m string) { told = append(told, m) }
	var mu sync.Mutex
	e.h.Wake = func(reason string) { mu.Lock(); woken = append(woken, reason); mu.Unlock() }
	ctx := context.Background()

	o.down = 503
	if err := r.Ready(ctx); !errors.Is(err, bridge.ErrUnreachable) {
		t.Fatalf("%v", err)
	}
	b := state(t, e)
	if b.State != store.BridgeUnreachable || b.Failures != 1 || !b.RetryAt.After(time.Now()) {
		t.Fatalf("%+v", b)
	}
	// until the retry time the server is left alone
	hits := o.count()
	if err := r.Ready(ctx); !errors.Is(err, bridge.ErrUnreachable) || o.count() != hits {
		t.Fatalf("it was asked again before its time: %v, %d requests", err, o.count()-hits)
	}
	// failures of a burst of photos in one pass count once
	r.Report(bridge.ErrUnreachable, "")
	r.Report(bridge.ErrUnreachable, "")
	if f := state(t, e).Failures; f != 1 {
		t.Errorf("failures in one pass counted %d times", f)
	}
	if len(told) != 1 {
		t.Errorf("told %d times: %v", len(told), told)
	}

	// when the time comes and the server is back, it is ok again, once told, and a pass is woken for the waiting photos
	e.st.SetBridgeState(ctx, "holiday", store.BridgeUnreachable, "x", "", 1, time.Now().Add(-time.Second))
	o.down = 0
	if err := r.Ready(ctx); err != nil {
		t.Fatalf("%v", err)
	}
	if b := state(t, e); b.State != store.BridgeOK || b.Failures != 0 || b.Note != "" {
		t.Fatalf("%+v", b)
	}
	if len(told) != 2 {
		t.Errorf("recovery is told once: %v", told)
	}
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	found := false
	for _, w := range woken {
		found = found || strings.HasPrefix(w, "bridge restored")
	}
	if !found {
		t.Errorf("no pass was woken on recovery: %v", woken)
	}
}

func TestTheBackoffGrowsAndIsCapped(t *testing.T) {
	// not exported: through a server that stays down, each failure after the retry time doubles the wait
	o := newOtherServer(t)
	e, r := withBridge(t, o)
	ctx := context.Background()
	o.down = 500
	var last time.Duration
	for i := 1; i <= 9; i++ {
		e.st.SetBridgeState(ctx, "holiday", store.BridgeUnreachable, "x", "", i-1, time.Now().Add(-time.Second)) // its time has come
		r.Ready(ctx)
		d := time.Until(state(t, e).RetryAt)
		if d < last-2*time.Second {
			t.Fatalf("failure %d waits %v, less than before (%v)", i, d, last)
		}
		last = d
	}
	if last > time.Hour+time.Second || last < 55*time.Minute {
		t.Fatalf("capped at an hour, got %v", last)
	}
}

func inviteSetup(t *testing.T, o *otherServer, p bridge.Policy) (*env, string) {
	t.Helper()
	e, ctx := setup(t, false), context.Background()
	e.h.Bridge = bridge.Settings{Enabled: true, Policy: p}
	if err := e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: "holiday", Tag: "hol", Policy: "approval"}); err != nil {
		t.Fatal(err)
	}
	token, err := e.h.NewBridgeInvite(ctx, ann, "holiday")
	if err != nil {
		t.Fatal(err)
	}
	return e, token
}

func TestOnlyTheOwnerMakesAnInvitationAndANewOneReplacesTheOld(t *testing.T) {
	e, first := inviteSetup(t, newOtherServer(t), bridge.Policy{AllowPrivate: true})
	ctx := context.Background()
	if _, err := e.h.NewBridgeInvite(ctx, bob, "holiday"); !errors.Is(err, hub.ErrNotOwner) {
		t.Errorf("someone else: %v", err)
	}
	second, err := e.h.NewBridgeInvite(ctx, ann, "holiday")
	if err != nil || second == first {
		t.Fatal(err)
	}
	if _, err := e.h.BridgeInvite(ctx, first); !errors.Is(err, hub.ErrBridgeInvite) {
		t.Errorf("the old link must stop working: %v", err)
	}
	info, err := e.h.BridgeInvite(ctx, second)
	if err != nil || info.Service != "holiday" || info.Owner == "" {
		t.Fatalf("%+v %v", info, err)
	}
	if e.h.BridgeInviteOf(ctx, "holiday") != second {
		t.Error("the owner's page shows the open link")
	}
	e.h.Bridge = bridge.Settings{}
	if _, err := e.h.NewBridgeInvite(ctx, ann, "holiday"); !errors.Is(err, hub.ErrBridgesOff) {
		t.Errorf("off: %v", err)
	}
	if _, err := e.h.BridgeInvite(ctx, second); !errors.Is(err, hub.ErrBridgeInvite) || e.h.BridgeInviteOf(ctx, "holiday") != "" {
		t.Errorf("with bridges off nothing is offered: %v", err)
	}
	if _, err := e.h.BridgeInvite(ctx, "no-such-token"); !errors.Is(err, hub.ErrBridgeInvite) {
		t.Errorf("unknown: %v", err)
	}
}

func TestAcceptingAnInvitationConnectsTheServiceAndUsesTheLinkUp(t *testing.T) {
	o := newOtherServer(t)
	e, token := inviteSetup(t, o, bridge.Policy{AllowPrivate: true})
	ctx := context.Background()
	if err := e.h.AcceptBridge(ctx, token, o.URL, "REMOTE", "  Aunt Lia "); err != nil {
		t.Fatal(err)
	}
	b, err := e.st.Bridge(ctx, "holiday")
	if err != nil || b.Key != "REMOTE" || b.DisplayName != "Aunt Lia" || b.Version != "v3.2.0" || b.State != store.BridgeOK || b.Address != o.URL {
		t.Fatalf("%+v %v", b, err)
	}
	if svc := service(e, "holiday"); svc.Remote == nil || svc.Remote.Label() != "Aunt Lia" {
		t.Fatal("the running service does not have its receiver")
	}
	if err := e.h.AcceptBridge(ctx, token, o.URL, "REMOTE", "Again"); !errors.Is(err, hub.ErrBridgeInvite) {
		t.Errorf("a link works once: %v", err)
	}
}

func TestNothingIsStoredUntilTheChecksPassAndTheLinkSurvivesAMistake(t *testing.T) {
	o := newOtherServer(t)
	e, token := inviteSetup(t, o, bridge.Policy{AllowPrivate: true})
	ctx := context.Background()
	for name, c := range map[string]struct {
		address, key, who string
		want              error
	}{
		"wrong key":        {o.URL, "WRONG", "Lia", bridge.ErrRevoked},
		"empty key":        {o.URL, " ", "Lia", bridge.ErrRevoked},
		"no name":          {o.URL, "REMOTE", "  ", hub.ErrBadBridgeName},
		"a very long name": {o.URL, "REMOTE", strings.Repeat("x", 41), hub.ErrBadBridgeName},
		"not an address":   {"", "REMOTE", "Lia", bridge.ErrBadAddress},
		"ftp":              {"ftp://x.example.org", "REMOTE", "Lia", bridge.ErrBadAddress},
	} {
		if err := e.h.AcceptBridge(ctx, token, c.address, c.key, c.who); !errors.Is(err, c.want) || !hub.IsUserError(err) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := e.st.Bridge(ctx, "holiday"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("a failed attempt stored a bridge")
	}
	if _, err := e.h.BridgeInvite(ctx, token); err != nil {
		t.Fatalf("a mistake must not use the link up: %v", err)
	}
}

func TestAPrivateAddressIsNotContactedWithoutTheOptIn(t *testing.T) {
	o := newOtherServer(t)
	e, token := inviteSetup(t, o, bridge.Policy{})
	err := e.h.AcceptBridge(context.Background(), token, o.URL, "REMOTE", "Lia")
	if !errors.Is(err, bridge.ErrBadAddress) {
		t.Fatalf("%v", err)
	}
	if o.count() != 0 {
		t.Fatal("the server was contacted")
	}
}

func TestAttemptsAreRationedPerAddressAndPerInvitation(t *testing.T) {
	h := &hub.Hub{}
	for i := 0; i < 10; i++ {
		if !h.AllowBridgeAttempt("1.2.3.4", "tok"+string(rune('a'+i))) {
			t.Fatalf("attempt %d refused too early", i)
		}
	}
	if h.AllowBridgeAttempt("1.2.3.4", "other") {
		t.Error("an eleventh attempt from one address within the hour")
	}
	if !h.AllowBridgeAttempt("5.6.7.8", "other") {
		t.Error("another address is not affected")
	}
	for i := 0; i < 20; i++ {
		h.AllowBridgeAttempt("9.9.9."+string(rune('a'+i)), "busy")
	}
	if h.AllowBridgeAttempt("10.10.10.10", "busy") {
		t.Error("a twenty-first attempt on one invitation")
	}
}

func TestTheOwnerCanRemoveTheBridgeAndWaitingPhotosThenGoToTheOwnerAlone(t *testing.T) {
	o := newOtherServer(t)
	e, _ := withBridge(t, o)
	ctx := context.Background()
	var woken []string
	var mu sync.Mutex
	e.h.Wake = func(r string) { mu.Lock(); woken = append(woken, r); mu.Unlock() }

	st, ok := e.h.BridgeStatus(ctx, "holiday")
	if !ok || st.Name != "Aunt Lia" || st.State != store.BridgeOK {
		t.Fatalf("%+v %v", st, ok)
	}
	if err := e.h.RemoveBridge(ctx, bob, "holiday"); !errors.Is(err, hub.ErrNotOwner) {
		t.Errorf("someone else: %v", err)
	}
	if err := e.h.RemoveBridge(ctx, ann, "holiday"); err != nil {
		t.Fatal(err)
	}
	if svc := service(e, "holiday"); svc.Remote != nil || svc.Broken != "" || len(svc.Receivers) != 1 {
		t.Fatalf("%+v", svc)
	}
	if _, ok := e.h.BridgeStatus(ctx, "holiday"); ok {
		t.Error("still there")
	}
	if len(woken) != 1 {
		t.Errorf("a pass must be woken so waiting photos go: %v", woken)
	}
	if err := e.h.RemoveBridge(ctx, ann, "holiday"); !errors.Is(err, hub.ErrNoBridge) || !hub.IsUserError(err) {
		t.Errorf("none left: %v", err)
	}
}
