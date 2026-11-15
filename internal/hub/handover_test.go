package hub_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/hub"
)

func TestThePhotosAPersonPicksInImmichAreHandedOverWithTheirOwnKeyAndCanBeTakenOffAgain(t *testing.T) {
	e, ctx := ruleEnv(t)
	a, b, other := uuid.NewString(), uuid.NewString(), uuid.NewString()
	e.f.mine = map[string]string{a: bobID.String(), b: bobID.String(), other: annID.String()}

	e.woken = nil
	n, err := e.h.HandOver(ctx, e.sess, bob, "family", []string{a, b, a})
	if err != nil || n != 2 {
		t.Fatalf("two photos, one picked twice: %d %v", n, err)
	}
	if !slices.Contains(e.f.tagged, a) || !slices.Contains(e.f.tagged, b) || len(e.f.tagged) != 2 {
		t.Errorf("tagged: %v", e.f.tagged)
	}
	if len(e.woken) == 0 {
		t.Error("a pass must start at once, so the photos do not wait for the timer")
	}
	if err := e.h.UndoHandOver(ctx, e.sess, bob, "family", []string{a}); err != nil || !slices.Equal(e.f.untagged, []string{a}) {
		t.Errorf("undo takes the tag off the photo named: %v %v", e.f.untagged, err)
	}

	before := len(e.f.tagged)
	if _, err := e.h.HandOver(ctx, e.sess, bob, "family", []string{a, other}); !errors.Is(err, hub.ErrNotYourPhotos) || len(e.f.tagged) != before {
		t.Errorf("someone else's photo among them: nothing is tagged and the person is told: %v", err)
	}
	if _, err := e.h.HandOver(ctx, e.sess, bob, "family", []string{"not-an-id"}); !errors.Is(err, hub.ErrNotYourPhotos) {
		t.Errorf("not an id: %v", err)
	}
	if _, err := e.h.HandOver(ctx, e.sess, bob, "family", nil); !errors.Is(err, hub.ErrNoPhotosChosen) {
		t.Errorf("nothing chosen: %v", err)
	}
	many := make([]string, hub.HandOverMax+1)
	for i := range many {
		many[i] = uuid.NewString()
	}
	if _, err := e.h.HandOver(ctx, e.sess, bob, "family", many); !errors.Is(err, hub.ErrTooManyChosen) {
		t.Errorf("too many: %v", err)
	}
	if _, err := e.h.HandOver(ctx, e.sess, bob, "elsewhere", []string{a}); err == nil {
		t.Error("a service that does not exist is refused")
	}
	for _, err := range []error{hub.ErrNotYourPhotos, hub.ErrNoPhotosChosen, hub.ErrTooManyChosen} {
		if !hub.IsUserError(err) {
			t.Errorf("%v is told to the person as it is", err)
		}
	}
}

func TestSomeoneWhoDidNotJoinFromThePageCannotHandOverFromImmich(t *testing.T) {
	e, ctx := setup(t, true), t.Context()
	a := uuid.NewString()
	e.f.mine = map[string]string{a: bobID.String()}
	if _, err := e.h.HandOver(ctx, e.sess, bob, "family", []string{a}); !errors.Is(err, hub.ErrNotJoined) {
		t.Errorf("no key of theirs to tag with: %v", err)
	}
}
