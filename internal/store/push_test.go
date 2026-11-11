package store

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

func pushSub(user uuid.UUID, endpoint string) PushSub {
	return PushSub{UserID: user, Endpoint: endpoint, P256dh: bytes.Repeat([]byte{4}, 65), Auth: bytes.Repeat([]byte{9}, 16), Lang: "de", Mode: MailAll, Label: "Firefox on Linux"}
}

func TestADeviceIsStoredSealedUpdatedAndRemovedByItsOwner(t *testing.T) {
	dir, ctx := t.TempDir(), context.Background()
	st, err := open(t, dir, "push-store-secret-1234")
	if err != nil {
		t.Fatal(err)
	}
	ann, bob := uuid.New(), uuid.New()
	const ep = "https://fcm.googleapis.com/fcm/send/SECRET-ENDPOINT-TOKEN"
	id, err := st.PushSubscribe(ctx, pushSub(ann, ep))
	if err != nil || id != PushID(ep) {
		t.Fatalf("%q %v", id, err)
	}
	got, _ := st.PushSubs(ctx, ann)
	if len(got) != 1 || got[0].Endpoint != ep || got[0].Lang != "de" || len(got[0].P256dh) != 65 || len(got[0].Auth) != 16 || got[0].CreatedAt.IsZero() {
		t.Fatalf("%+v", got)
	}
	if other, _ := st.PushSubs(ctx, bob); len(other) != 0 {
		t.Error("another person sees the device")
	}

	// the same browser again is the same device, with the new choices
	again := pushSub(ann, ep)
	again.Mode, again.Lang = MailNeeds, "fr"
	if _, err := st.PushSubscribe(ctx, again); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.PushSubs(ctx, ann); len(got) != 1 || got[0].Mode != MailNeeds || got[0].Lang != "fr" {
		t.Errorf("%+v", got)
	}

	// mode, success and failure counts
	if ok, _ := st.PushSetMode(ctx, bob, id, MailAll); ok {
		t.Error("bob changed ann's device")
	}
	if ok, _ := st.PushSetMode(ctx, ann, id, MailAll); !ok {
		t.Error("ann could not")
	}
	if n, _ := st.PushMarkFailed(ctx, id); n != 1 {
		t.Errorf("%d", n)
	}
	if n, _ := st.PushMarkFailed(ctx, id); n != 2 {
		t.Errorf("%d", n)
	}
	st.PushMarkOK(ctx, id, testNow())
	if got, _ := st.PushSubs(ctx, ann); got[0].Failures != 0 || got[0].LastOK.IsZero() {
		t.Errorf("%+v", got[0])
	}

	// not on disk in the clear
	st.Close()
	raw, _ := os.ReadFile(filepath.Join(dir, "handoff.db"))
	wal, _ := os.ReadFile(filepath.Join(dir, "handoff.db-wal"))
	if bytes.Contains(raw, []byte("SECRET-ENDPOINT-TOKEN")) || bytes.Contains(wal, []byte("SECRET-ENDPOINT-TOKEN")) {
		t.Error("the address of the push service is in the file in the clear")
	}
	st2, err := open(t, dir, "push-store-secret-1234")
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if ok, _ := st2.PushRemove(ctx, bob, id); ok {
		t.Error("bob removed ann's device")
	}
	if ok, _ := st2.PushRemove(ctx, ann, id); !ok {
		t.Error("ann could not remove it")
	}
	if all, _ := st2.PushSubsAll(ctx); len(all) != 0 {
		t.Errorf("%+v", all)
	}
}

func TestAPersonHasAtMostTenDevicesAndABrowserMovesToWhoeverUsesItNow(t *testing.T) {
	st, err := open(t, t.TempDir(), "push-store-secret-1234")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	ann, bob := uuid.New(), uuid.New()
	for i := 0; i < MaxPushDevices; i++ {
		if _, err := st.PushSubscribe(ctx, pushSub(ann, "https://fcm.googleapis.com/fcm/send/"+uuid.NewString())); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.PushSubscribe(ctx, pushSub(ann, "https://fcm.googleapis.com/fcm/send/one-too-many")); err != ErrTooManyDevices {
		t.Errorf("%v", err)
	}
	all, _ := st.PushSubs(ctx, ann)
	if _, err := st.PushSubscribe(ctx, func() PushSub { p := pushSub(ann, all[0].Endpoint); p.Mode = MailNeeds; return p }()); err != nil {
		t.Errorf("an existing device can always be updated: %v", err)
	}
	// bob signs in on a browser that was ann's: the device is now bob's alone
	if _, err := st.PushSubscribe(ctx, pushSub(bob, all[1].Endpoint)); err != nil {
		t.Fatal(err)
	}
	if a, _ := st.PushSubs(ctx, ann); len(a) != MaxPushDevices-1 {
		t.Errorf("ann has %d", len(a))
	}
	if b, _ := st.PushSubs(ctx, bob); len(b) != 1 {
		t.Errorf("bob has %d", len(b))
	}
}

func TestThePushKeyIsMadeOnceKeptSealedAndNeedsTheStoreKey(t *testing.T) {
	dir, ctx := t.TempDir(), context.Background()
	st, _ := open(t, dir, "push-store-secret-1234")
	calls := 0
	mk := func() ([]byte, error) { calls++; return bytes.Repeat([]byte{7}, 32), nil }
	a, err := st.PushKey(ctx, mk)
	if err != nil || len(a) != 32 {
		t.Fatal(err)
	}
	b, _ := st.PushKey(ctx, mk)
	if calls != 1 || !bytes.Equal(a, b) {
		t.Errorf("made %d times", calls)
	}
	st.Close()
	raw, _ := os.ReadFile(filepath.Join(dir, "handoff.db"))
	wal, _ := os.ReadFile(filepath.Join(dir, "handoff.db-wal"))
	if bytes.Contains(raw, bytes.Repeat([]byte{7}, 32)) || bytes.Contains(wal, bytes.Repeat([]byte{7}, 32)) {
		t.Error("the private key is in the file in the clear")
	}
	st2, _ := open(t, dir, "push-store-secret-1234")
	defer st2.Close()
	if c, _ := st2.PushKey(ctx, mk); calls != 1 || !bytes.Equal(a, c) {
		t.Errorf("the key changed after a restart (made %d times)", calls)
	}
}

func testNow() time.Time { return time.Now() }

// the page works out the same id from the address (ts/push.test.ts has the same value)
func TestADevicesIdIsTheHashOfItsAddress(t *testing.T) {
	if got := PushID("abc"); got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Errorf("%s", got)
	}
}
