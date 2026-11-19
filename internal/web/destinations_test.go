package web_test

import (
	"context"
	"strings"
	"testing"

	"github.com/vfuoglio/immich-handoff/internal/dest"
	"github.com/vfuoglio/immich-handoff/internal/store"
	"github.com/vfuoglio/immich-handoff/internal/web"
)

func init() {
	dest.Register("folder", func(s dest.Spec, n dest.Net) (dest.Destination, error) { return &dest.Memory{}, nil })
}

func withDestinations(t *testing.T, st **store.Store, userAdd bool) func(*web.Server) {
	base := withPairings(t, st)
	return func(srv *web.Server) {
		base(srv)
		srv.Hub.AdminDests = map[string]dest.Spec{"attic": {Name: "attic", Kind: "folder", Settings: map[string]string{"root": "/d/attic"}}}
		srv.Hub.UserDests = userAdd
	}
}

func TestAnOwnerMakesAServiceThatGoesOnlyToDestinationsAndManagesThemFromItsCard(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withDestinations(t, &st, true))
	ctx := context.Background()
	_, page := do(t, "GET", app.URL+"/", "sess=other", nil)
	if !strings.Contains(page, "Only to destinations: no copy goes to my own library") {
		t.Error("the offer form has the checkbox when there are destinations")
	}
	if code, b := postForm(t, app.URL+"/services", "sess=other", "name=Backup&tag=to-backup&policy=open&destonly=1&pool=1"); code != 409 || !strings.Contains(b, "cannot be an event pool") {
		t.Errorf("not a pool: %d %s", code, b)
	}
	if code, b := postForm(t, app.URL+"/services", "sess=other", "name=Backup&tag=to-backup&policy=open&destonly=1"); code != 204 {
		t.Fatalf("made: %d %s", code, b)
	}
	_, page = do(t, "GET", app.URL+"/", "sess=other", nil)
	for _, want := range []string{"This service has only destinations", "Add a destination", `value="admin:attic"`, "attic (folder)", "Enter a network destination of my own"} {
		if !strings.Contains(page, want) {
			t.Errorf("the owner's card lacks %q", want)
		}
	}
	if code, b := postForm(t, app.URL+"/services/destinations", "sess=other", "service=Backup&ref=admin:attic&template=%7Byyyy%7D%2F%7Bname%7D&cap_photos=50"); code != 204 {
		t.Fatalf("added: %d %s", code, b)
	}
	if code, b := postForm(t, app.URL+"/services/destinations", "sess=other", "service=Backup&ref=admin:attic"); code != 409 || !strings.Contains(b, "already uses that destination") {
		t.Errorf("twice: %d %s", code, b)
	}
	if code, b := postForm(t, app.URL+"/services/destinations", "sess=other", "service=Backup&ref=admin:attic&template=..%2Fx&cap_photos=x"); code != 409 {
		t.Errorf("a bad limit: %d %s", code, b)
	}
	_, page = do(t, "GET", app.URL+"/", "sess=other", nil)
	bindings, _ := st.ServiceDestinations(ctx, "Backup")
	if len(bindings) != 1 {
		t.Fatalf("%+v", bindings)
	}
	id := bindings[0].ID
	for _, want := range []string{"Working", "For example: 2026/IMG_1234.jpg", "Pause this destination", `value="50"`, "Stop sending Backup to attic?"} {
		if !strings.Contains(page, want) {
			t.Errorf("the destination's row lacks %q", want)
		}
	}
	if code, _ := postForm(t, app.URL+"/services/destinations/pause", "sess=other", "service=Backup&id="+id+"&paused=1"); code != 204 {
		t.Fatal("pause")
	}
	_, page = do(t, "GET", app.URL+"/", "sess=other", nil)
	if !strings.Contains(page, "Resume this destination") || !strings.Contains(page, "Paused") {
		t.Error("it says paused and offers to resume")
	}
	if code, b := postForm(t, app.URL+"/services/destinations/edit", "sess=other", "service=Backup&id="+id+"&template=%7Bservice%7D%2F%7Bname%7D&cap_photos=5&cap_mb=100"); code != 204 {
		t.Fatalf("edit: %d %s", code, b)
	}
	if b, _ := st.ServiceDestination(ctx, id); b.Template != "{service}/{name}" || b.CapPhotos != 5 || b.CapMB != 100 || !b.Paused {
		t.Errorf("%+v", b)
	}
	// somebody else cannot touch it
	if code, _ := postForm(t, app.URL+"/services/destinations/remove", "sess=alice", "service=Backup&id="+id); code != 409 {
		t.Errorf("not theirs: %d", code)
	}
	if code, _ := postForm(t, app.URL+"/services/destinations/remove", "", "service=Backup&id="+id); code == 204 {
		t.Error("signed out")
	}
	if code, _ := postForm(t, app.URL+"/services/destinations/remove", "sess=other", "service=Backup&id="+id); code != 204 {
		t.Fatal("remove")
	}
	if left, _ := st.ServiceDestinations(ctx, "Backup"); len(left) != 0 {
		t.Errorf("%+v", left)
	}
}

func TestAPersonEntersANetworkDestinationOnlyWhenTheAdministratorAllowsIt(t *testing.T) {
	var st *store.Store
	off, _ := setup(t, withDestinations(t, &st, false))
	postForm(t, off.URL+"/services", "sess=other", "name=Backup&tag=to-backup&policy=open&destonly=1")
	if code, b := postForm(t, off.URL+"/destinations", "sess=other", "name=Cloud&kind=webdav&url=https%3A%2F%2Fcloud.example.org%2Fdav&user=me&password=pw"); code != 409 || !strings.Contains(b, "has not allowed people to add destinations") {
		t.Errorf("off: %d %s", code, b)
	}
	_, page := do(t, "GET", off.URL+"/", "sess=other", nil)
	if strings.Contains(page, "Enter a network destination of my own") {
		t.Error("the form is not offered")
	}
	var st2 *store.Store
	on, _ := setup(t, withDestinations(t, &st2, true))
	postForm(t, on.URL+"/services", "sess=other", "name=Backup&tag=to-backup&policy=open&destonly=1")
	if code, b := postForm(t, on.URL+"/destinations", "sess=other", "name=Mine&kind=folder&root=%2Fetc"); code != 409 || !strings.Contains(b, "only be one the administrator named") {
		t.Errorf("never a folder: %d %s", code, b)
	}
	if code, b := postForm(t, on.URL+"/destinations", "sess=other", "name=Cloud&kind=webdav&url=http%3A%2F%2F192.168.0.5%2Fdav&user=me&password=pw"); code != 409 {
		t.Errorf("a private address: %d %s", code, b)
	}
	if code, b := postForm(t, on.URL+"/destinations", "sess=other", "name=Cloud&kind=webdav&url=https%3A%2F%2Fcloud.example.org%2Fdav&user=me&password=hunter2"); code != 204 {
		t.Fatalf("saved: %d %s", code, b)
	}
	_, page = do(t, "GET", on.URL+"/", "sess=other", nil)
	if !strings.Contains(page, "Cloud (WebDAV)") || strings.Contains(page, "hunter2") {
		t.Error("it is offered, and the password is never shown")
	}
	if code, b := postForm(t, on.URL+"/destinations/forget", "sess=other", "ref=own:nope"); code != 409 {
		t.Errorf("%d %s", code, b)
	}
}
