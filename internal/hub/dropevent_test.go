package hub_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

func TestAnEventKeepsWhatGuestsLeaveForTheDaysTheOwnerChose(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	end := time.Now().Add(48 * time.Hour)
	b := newBox(t, e, hub.DropParams{Name: "Wedding", Closes: end, KeepDays: 30})
	if b.KeepDays != 30 {
		t.Fatalf("keep days: %d", b.KeepDays)
	}
	if got := b.KeepUntil(); got.Sub(end.AddDate(0, 0, 30)).Abs() > time.Second {
		t.Errorf("kept until %v, want 30 days after %v", got, end)
	}
	if s := hub.SettingsOf(b); s.KeepDays != 30 || s.Params("Copy").KeepDays != 30 {
		t.Errorf("a template keeps the retention: %+v", s)
	}
	// a box that never ends, or that keeps nothing past its end, has no date
	if until := newBox(t, e, hub.DropParams{Name: "Open ended", KeepDays: 30}).KeepUntil(); !until.IsZero() {
		t.Errorf("a box with no end has no date: %v", until)
	}
	if until := newBox(t, e, hub.DropParams{Name: "Until revoked", Closes: end}).KeepUntil(); !until.IsZero() {
		t.Errorf("0 keeps it until the box is revoked: %v", until)
	}
	for _, days := range []int{-1, 3651} {
		if _, err := e.h.CreateDropBox(ctx, e.sess, bob, hub.DropParams{Name: "Bad", KeepDays: days}); !errors.Is(err, hub.ErrBadDropLimit) {
			t.Errorf("%d days: %v", days, err)
		}
	}
}

func TestOnlyTheOwnerChangesWhatIsKeptAfterTheEvent(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	b := newBox(t, e, hub.DropParams{Name: "Party", Days: 3, KeepDays: 7})
	if err := e.h.AddDropHost(ctx, e.sess, bob, b.ID, "ann@x.test", store.HostManage); err != nil {
		t.Fatal(err)
	}
	ann := immich.User{ID: annID, Name: "Ann", Email: "ann@x.test"}
	if err := e.h.SetDropEvent(ctx, ann, b.ID, hub.EventSettings{KeepDays: 365}); !errors.Is(err, hub.ErrDropRole) {
		t.Errorf("a co-host who manages must not decide what is kept of other people: %v", err)
	}
	if err := e.h.SetDropEvent(ctx, immich.User{ID: uuid.New()}, b.ID, hub.EventSettings{KeepDays: 365}); !errors.Is(err, hub.ErrNoDropBox) {
		t.Errorf("a stranger: %v", err)
	}
	if err := e.h.SetDropEvent(ctx, bob, b.ID, hub.EventSettings{KeepDays: -2}); !errors.Is(err, hub.ErrBadDropLimit) {
		t.Errorf("a negative number: %v", err)
	}
	if err := e.h.SetDropEvent(ctx, bob, b.ID, hub.EventSettings{KeepDays: 90}); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.st.DropBox(ctx, b.ID); got.KeepDays != 90 {
		t.Errorf("keep days after the change: %d", got.KeepDays)
	}
	if ev, _ := e.h.DropEvents(ctx, bob.ID, b.ID, 5); len(ev) == 0 || ev[0].Text == "" {
		t.Errorf("the change is in the log: %+v", ev)
	}
}
