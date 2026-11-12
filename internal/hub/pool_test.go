package hub_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/store"
	"github.com/vfuoglio/immich-handoff/internal/syncer"
)

func poolService(t *testing.T, e *env, mod func(*hub.CreateParams)) {
	t.Helper()
	p := hub.CreateParams{Name: "trip", Tag: "trip", Policy: "open", Pool: true}
	if mod != nil {
		mod(&p)
	}
	if err := e.h.CreateService(context.Background(), e.sess, bob, p); err != nil {
		t.Fatal(err)
	}
}

func svcNamed(e *env, name string) *syncer.Service {
	for _, s := range e.h.Services() {
		if s.Label() == name {
			return s
		}
	}
	return nil
}

func TestAPoolIsMadeWithAKeyThatSendsAndReceivesAndTheOwnerIsBothToo(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	poolService(t, e, func(p *hub.CreateParams) { p.EndsInDays = 7 })
	for _, p := range []string{"asset.upload", "tag.create", "asset.download", "stack.read", "workflow.create", "asset.read"} {
		if !hasPerm(e.f.lastPerms, p) {
			t.Errorf("the owner's key lacks %s: %v", p, e.f.lastPerms)
		}
	}
	all, _ := e.st.Services(ctx)
	var v store.Service
	for _, s := range all {
		if s.Name == "trip" {
			v = s
		}
	}
	if !v.Pool || v.Mode != syncer.ModeCopy || time.Until(v.EndsAt) < 6*24*time.Hour || time.Until(v.EndsAt) > 8*24*time.Hour {
		t.Fatalf("%+v", v)
	}
	svc := svcNamed(e, "trip")
	if svc == nil || !svc.Pool || svc.Mode != syncer.ModeCopy || len(svc.Senders) != 1 || len(svc.Receivers) != 1 || svc.Senders[0] != svc.Receivers[0] {
		t.Fatalf("the owner is a sender and a receiver, through one account: %+v", svc)
	}
	found := false
	for _, name := range e.f.workflows {
		if name == "immich-handoff: trip" {
			found = true
		}
	}
	if !found {
		t.Errorf("the owner's account needs the workflow a sender gets: %v", e.f.workflows)
	}
}

func TestAPoolOnlyEndsAfterOneToThreeSixtySixDaysAndOnlyAPoolEnds(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	for _, days := range []int{-1, 367, 1000} {
		if err := e.h.CreateService(ctx, e.sess, bob, hub.CreateParams{Name: "pp", Tag: "pp", Pool: true, EndsInDays: days}); err != hub.ErrBadPoolEnd {
			t.Errorf("%d days: %v", days, err)
		}
	}
	if err := e.h.CreateService(ctx, e.sess, bob, hub.CreateParams{Name: "plain", Tag: "plain", EndsInDays: 5}); err != hub.ErrBadPoolEnd {
		t.Errorf("a service that is not a pool has no end: %v", err)
	}
	if err := e.h.CreateService(ctx, e.sess, bob, hub.CreateParams{Name: "plain2", Tag: "plain2"}); err != nil {
		t.Fatal(err)
	}
	if svc := svcNamed(e, "plain2"); svc.Pool || len(svc.Receivers) != 1 || len(svc.Senders) != 0 {
		t.Errorf("an ordinary service stays what it was: %+v", svc)
	}
	if err := e.h.SetPoolEnd(ctx, bob, "plain2", 3); err != hub.ErrNotPool {
		t.Errorf("%v", err)
	}
	if err := e.h.EndPoolNow(ctx, bob, "plain2"); err != hub.ErrNotPool {
		t.Errorf("%v", err)
	}
}

func TestJoiningAPoolNeedsAYesToWhatItMeansAndGivesAKeyThatSendsAndReceivesInCopyMode(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	poolService(t, e, nil)
	if err := e.h.Join(ctx, e.ann, ann, "trip", "", "move"); err != hub.ErrPoolConfirm || !hub.IsUserError(err) {
		t.Fatalf("without the yes: %v", err)
	}
	if links, _ := e.st.Links(ctx); len(links) != 0 {
		t.Fatal("a refused join left a membership behind")
	}
	keysBefore := len(e.f.keys)
	if err := e.h.JoinWith(ctx, e.ann, ann, "trip", "", "move", true); err != nil { // asked for "move": a pool never moves
		t.Fatal(err)
	}
	if len(e.f.keys)-keysBefore != 1 {
		t.Errorf("one key for the member: %d", len(e.f.keys)-keysBefore)
	}
	for _, p := range []string{"asset.upload", "tag.create", "asset.download", "workflow.create", "asset.read"} {
		if !hasPerm(e.f.lastPerms, p) {
			t.Errorf("the member's key lacks %s: %v", p, e.f.lastPerms)
		}
	}
	links, _ := e.st.Links(ctx)
	if len(links) != 1 || links[0].Mode != syncer.ModeCopy || links[0].Role != store.RoleSender {
		t.Errorf("a pool keeps everyone's originals: %+v", links)
	}
	svc := svcNamed(e, "trip")
	if len(svc.Senders) != 2 || len(svc.Receivers) != 2 {
		t.Fatalf("two members, each sends and receives: %d senders, %d receivers", len(svc.Senders), len(svc.Receivers))
	}
	for _, s := range svc.Senders {
		in := false
		for _, r := range svc.Receivers {
			in = in || r == s
		}
		if !in {
			t.Errorf("%s sends but does not receive", s.Name)
		}
	}
	if err := e.h.JoinWith(ctx, e.ann, ann, "trip", "", "copy", true); err != hub.ErrAlready {
		t.Errorf("joining twice: %v", err)
	}
	// leaving takes the member out of both lists and leaves the owner
	if err := e.h.Leave(ctx, e.ann, ann, "trip"); err != nil {
		t.Fatal(err)
	}
	svc = svcNamed(e, "trip")
	if len(svc.Senders) != 1 || len(svc.Receivers) != 1 || svc.Senders[0].Name != "Bob" {
		t.Errorf("after ann left: %d senders, %d receivers", len(svc.Senders), len(svc.Receivers))
	}
}

func TestTheConfirmationIsOnlyAskedOfPoolsAndAnEndedPoolTakesNoOne(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	if err := e.h.CreateService(ctx, e.sess, bob, hub.CreateParams{Name: "plain", Tag: "plain", Policy: "open"}); err != nil {
		t.Fatal(err)
	}
	if err := e.h.Join(ctx, e.ann, ann, "plain", "", "copy"); err != nil {
		t.Errorf("an ordinary service needs no such yes: %v", err)
	}
	poolService(t, e, nil)
	if err := e.h.EndPoolNow(ctx, bob, "trip"); err != nil {
		t.Fatal(err)
	}
	if err := e.h.JoinWith(ctx, e.ann, ann, "trip", "", "copy", true); err != hub.ErrPoolEnded || !hub.IsUserError(err) {
		t.Errorf("an ended pool: %v", err)
	}
}

func TestOnlyTheOwnerEndsOrExtendsAPoolAndAnEndedOneCanBeReopened(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	poolService(t, e, func(p *hub.CreateParams) { p.EndsInDays = 2 })
	if err := e.h.EndPoolNow(ctx, ann, "trip"); err != hub.ErrNotOwner {
		t.Errorf("someone else ending it: %v", err)
	}
	if err := e.h.SetPoolEnd(ctx, ann, "trip", 9); err != hub.ErrNotOwner {
		t.Errorf("someone else extending it: %v", err)
	}
	if svcNamed(e, "trip").Ended(time.Now()) {
		t.Fatal("a refused request ended it")
	}
	for _, d := range []int{-1, 400} {
		if err := e.h.SetPoolEnd(ctx, bob, "trip", d); err != hub.ErrBadPoolEnd {
			t.Errorf("%d days: %v", d, err)
		}
	}
	if err := e.h.EndPoolNow(ctx, bob, "trip"); err != nil {
		t.Fatal(err)
	}
	if !svcNamed(e, "trip").Ended(time.Now()) {
		t.Fatal("not ended")
	}
	if err := e.h.SetPoolEnd(ctx, bob, "trip", 10); err != nil { // reopened for ten days
		t.Fatal(err)
	}
	s := svcNamed(e, "trip")
	if s.Ended(time.Now()) || time.Until(s.EndsAt) < 9*24*time.Hour {
		t.Errorf("reopened: %v", s.EndsAt)
	}
	if err := e.h.SetPoolEnd(ctx, bob, "trip", 0); err != nil { // and then for as long as the owner wants
		t.Fatal(err)
	}
	if s := svcNamed(e, "trip"); !s.EndsAt.IsZero() || s.Ended(time.Now()) {
		t.Errorf("no end: %v", s.EndsAt)
	}
	acts, _ := e.h.Activity(ctx, bobID, 20)
	var lines []string
	for _, a := range acts {
		if a.Kind == "pool" {
			lines = append(lines, en(a.Text))
		}
	}
	if joined := strings.Join(lines, "\n"); !strings.Contains(joined, "ended the pool trip") || !strings.Contains(joined, "to go on until they end it") || !strings.Contains(joined, "to end on") {
		t.Errorf("the pool's activity: %s", joined)
	}
}
