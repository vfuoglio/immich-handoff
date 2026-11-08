package web_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
	"github.com/vfuoglio/immich-handoff/internal/vault"
	"github.com/vfuoglio/immich-handoff/internal/web"
)

// withClub gives the page a store with a service "club" owned by the "family" user and one sender, the "other" user.
func withClub(t *testing.T, st **store.Store) func(*web.Server) {
	return func(srv *web.Server) {
		v, _ := vault.New("0123456789abcdef0123")
		s, err := store.Open(t.TempDir()+"/h.db", v)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		*st = s
		ctx := context.Background()
		if _, err := s.AddService(ctx, store.Service{Name: "club", Tag: "share", InboxTag: "inbox", Refused: "dup", ExpiryDays: 30,
			OwnerID: uuid.MustParse(familyID), OwnerName: "Family", OwnerEmail: "family@x.test", KeyID: uuid.New(), Key: "k-family",
			Policy: store.PolicyOpen, Visibility: store.VisListed}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.AddLink(ctx, store.Link{Service: "club", Role: store.RoleSender, ImmichUserID: uuid.MustParse(otherID), Name: "Other", Email: "other@x.test", KeyID: uuid.New(), Key: "k-alice"}); err != nil {
			t.Fatal(err)
		}
		h := &hub.Hub{Static: srv.S.Services, Store: s, Log: srv.Log,
			Connect: func(key string) (*immich.API, error) { return immich.New(srv.BaseURL, immich.Auth{APIKey: key}, nil) }}
		if err := h.Reload(ctx); err != nil {
			t.Fatal(err)
		}
		srv.Hub = h
		srv.S.Provider = h
		srv.S.Usage = h
	}
}

func get(t *testing.T, app, cookie string) string {
	t.Helper()
	_, body := do(t, "GET", app+"/", cookie, nil)
	return body
}

func TestTheOwnerPausesAndResumesAServiceAndSendersSeeIt(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withClub(t, &st))
	if body := get(t, app.URL, "sess=family"); !strings.Contains(body, "Pause service") || strings.Contains(body, "Resume service") {
		t.Error("the owner must be offered Pause service")
	}
	if code, _ := postForm(t, app.URL+"/services/pause", "sess=other", "service=club&paused=1"); code != 409 {
		t.Errorf("a sender pausing the whole service: %d, want 409", code)
	}
	if code, body := postForm(t, app.URL+"/services/pause", "sess=family", "service=club&paused=1"); code != 204 {
		t.Fatalf("pause: %d %s", code, body)
	}
	owner := get(t, app.URL, "sess=family")
	if !strings.Contains(owner, "Resume service") || !strings.Contains(owner, "You paused this service") {
		t.Errorf("the owner must see it is paused and be offered Resume")
	}
	if sender := get(t, app.URL, "sess=other"); !strings.Contains(sender, "The owner paused this service.") {
		t.Errorf("a sender must be told the owner paused it")
	}
	if code, _ := postForm(t, app.URL+"/services/pause", "sess=family", "service=club&paused=0"); code != 204 {
		t.Fatalf("resume: %d", code)
	}
	if sender := get(t, app.URL, "sess=other"); strings.Contains(sender, "paused this service") {
		t.Error("after resuming nobody is told it is paused")
	}
	if code, _ := postForm(t, app.URL+"/services/pause", "", "service=club&paused=1"); code != 401 {
		t.Errorf("signed out: %d", code)
	}
}

func TestASenderPausesAndResumesTheirOwnSending(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withClub(t, &st))
	if body := get(t, app.URL, "sess=other"); !strings.Contains(body, "Pause sending") {
		t.Error("a member must be offered Pause sending")
	}
	if code, body := postForm(t, app.URL+"/pause", "sess=other", "service=club&paused=1"); code != 204 {
		t.Fatalf("pause: %d %s", code, body)
	}
	sender := get(t, app.URL, "sess=other")
	if !strings.Contains(sender, "You paused sending to this service.") || !strings.Contains(sender, "Resume sending") {
		t.Errorf("the sender must see it and be offered Resume")
	}
	if owner := get(t, app.URL, "sess=family"); !strings.Contains(owner, "(paused)") {
		t.Errorf("the owner must see that the sender paused")
	}
	if code, _ := postForm(t, app.URL+"/pause", "sess=alice", "service=club&paused=1"); code != 409 {
		t.Errorf("someone who did not join: %d, want 409", code)
	}
	if code, _ := postForm(t, app.URL+"/pause", "sess=other", "service=club&paused=0"); code != 204 {
		t.Fatalf("resume: %d", code)
	}
	if sender := get(t, app.URL, "sess=other"); strings.Contains(sender, "You paused sending") {
		t.Error("after resuming the pause note must be gone")
	}
}

func TestTheOwnerSetsDailyLimitsAndTheSenderSeesTheirUse(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withClub(t, &st))
	if code, body := postForm(t, app.URL+"/services/limits", "sess=family", "service=club&photos=5&mb=100"); code != 204 {
		t.Fatalf("limits: %d %s", code, body)
	}
	sender := get(t, app.URL, "sess=other")
	for _, want := range []string{"limits each sender to 5 photos and 100 MB a day", "Used in the last 24 hours: 0 of 5 photos, 0 of 100 MB."} {
		if !strings.Contains(sender, want) {
			t.Errorf("the sender's page lacks %q", want)
		}
	}
	// what was handed over shows for the sender and for the owner
	links, _ := st.Links(context.Background())
	st.AddHandover(context.Background(), links[0].ID, 3<<20)
	st.AddHandover(context.Background(), links[0].ID, 1<<20)
	if sender := get(t, app.URL, "sess=other"); !strings.Contains(sender, "2 of 5 photos, 4 of 100 MB") {
		t.Errorf("the sender's use: %.0s", sender)
	}
	if owner := get(t, app.URL, "sess=family"); !strings.Contains(owner, "2 photos, 4 MB in the last 24 hours") || !strings.Contains(owner, `value="5"`) {
		t.Error("the owner must see each sender's use and the current limits in the form")
	}
	for _, bad := range []string{"photos=abc", "photos=-3", "mb=99999999"} {
		if code, _ := postForm(t, app.URL+"/services/limits", "sess=family", "service=club&"+bad); code != 409 {
			t.Errorf("%s: %d, want 409", bad, code)
		}
	}
	if code, _ := postForm(t, app.URL+"/services/limits", "sess=other", "service=club&photos=1"); code != 409 {
		t.Errorf("a sender setting limits: %d, want 409", code)
	}
	if code, _ := postForm(t, app.URL+"/services/limits", "sess=family", "service=club&photos=&mb="); code != 204 {
		t.Errorf("empty means no limit: %d", code)
	}
	if sender := get(t, app.URL, "sess=other"); strings.Contains(sender, "limits each sender") {
		t.Error("with the limits removed the sender is told nothing")
	}
}

func TestTheAdminPageShowsPausesAndLimits(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withClub(t, &st))
	postForm(t, app.URL+"/services/limits", "sess=family", "service=club&photos=5")
	postForm(t, app.URL+"/services/pause", "sess=family", "service=club&paused=1")
	postForm(t, app.URL+"/pause", "sess=other", "service=club&paused=1")
	_, body := do(t, "GET", app.URL+"/admin", "sess=admin", nil)
	for _, want := range []string{"Paused", "Limit per sender: 5 photos a day", "(paused)"} {
		if !strings.Contains(body, want) {
			t.Errorf("the admin page lacks %q", want)
		}
	}
}

func TestTheOwnerSetsQuietHoursAndWhatTheServiceAcceptsAndTheSenderSeesThem(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withClub(t, &st))
	form := "service=club&from=01:00&to=06:00&largemb=200&rating=4&photos=1&orientation=landscape&maxmb=20"
	if code, body := postForm(t, app.URL+"/services/rules", "sess=family", form); code != 204 {
		t.Fatalf("rules: %d %s", code, body)
	}
	sender := get(t, app.URL, "sess=other")
	for _, want := range []string{"Files over 200 MB are handed over only between 01:00 and 06:00.", "Accepts: a rating of 4 or more, photos only, landscape only, files up to 20 MB."} {
		if !strings.Contains(sender, want) {
			t.Errorf("the sender's page lacks %q", want)
		}
	}
	owner := get(t, app.URL, "sess=family")
	for _, want := range []string{`value="01:00"`, `value="06:00"`, `value="landscape" selected`, `value="4" selected`, `name="photos" type="checkbox" value="1" checked`} {
		if !strings.Contains(owner, want) {
			t.Errorf("the owner's form does not show the current rules: lacks %q", want)
		}
	}
	for _, bad := range []string{"from=01:00&to=", "from=late&to=06:00", "rating=9", "orientation=square", "maxmb=-5"} {
		if code, _ := postForm(t, app.URL+"/services/rules", "sess=family", "service=club&"+bad); code != 409 {
			t.Errorf("%s: %d, want 409", bad, code)
		}
	}
	if code, _ := postForm(t, app.URL+"/services/rules", "sess=other", "service=club&rating=2"); code != 409 {
		t.Errorf("a sender setting rules: %d, want 409", code)
	}
	if code, _ := postForm(t, app.URL+"/services/rules", "sess=family", "service=club&from=&to=&largemb=&rating=0&orientation=&maxmb="); code != 204 {
		t.Errorf("empty means no rules: %d", code)
	}
	if sender := get(t, app.URL, "sess=other"); strings.Contains(sender, "Accepts:") || strings.Contains(sender, "handed over only between") {
		t.Error("with the rules cleared the sender is told nothing")
	}
}

func TestTheOwnerSetsHowLongCopiesLiveWhenTheAdministratorAllowsIt(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withClub(t, &st), func(srv *web.Server) { srv.Hub.ReceiverDelete = true })
	if code, body := postForm(t, app.URL+"/services/lifetime", "sess=family", "service=club&days=7"); code != 204 {
		t.Fatalf("lifetime: %d %s", code, body)
	}
	owner := get(t, app.URL, "sess=family")
	if !strings.Contains(owner, `name="days" type="number" min="0" max="3650" value="7"`) {
		t.Error("the owner's form must show the current lifetime")
	}
	if !strings.Contains(owner, "The key can also move photos the app handed over to your trash") {
		t.Error("the dialog that makes a service must say that the key can move photos to the trash")
	}
	if sender := get(t, app.URL, "sess=other"); !strings.Contains(sender, "trash after 7 days unless they file them in an album") {
		t.Error("the sender must be told that copies expire")
	}
	for _, bad := range []string{"days=abc", "days=-1", "days=9999"} {
		if code, _ := postForm(t, app.URL+"/services/lifetime", "sess=family", "service=club&"+bad); code != 409 {
			t.Errorf("%s: %d, want 409", bad, code)
		}
	}
	if code, _ := postForm(t, app.URL+"/services/lifetime", "sess=other", "service=club&days=3"); code != 409 {
		t.Errorf("a sender setting a lifetime: %d, want 409", code)
	}
	if code, _ := postForm(t, app.URL+"/services/lifetime", "sess=family", "service=club&days="); code != 204 {
		t.Errorf("empty turns it off: %d", code)
	}
	if sender := get(t, app.URL, "sess=other"); strings.Contains(sender, "trash after") {
		t.Error("with no lifetime the sender is told nothing")
	}
}

func TestWithDeletingSwitchedOffTheOwnerIsToldAndNothingIsOfferedToSenders(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withClub(t, &st)) // RECEIVER_DELETE off: the hub's default in tests
	owner := get(t, app.URL, "sess=family")
	if strings.Contains(owner, "The key can also move photos") {
		t.Error("with deleting off the dialog must not say the key can move photos to the trash")
	}
	if !strings.Contains(owner, "switched off by the administrator (RECEIVER_DELETE)") || strings.Contains(owner, `name="days"`) {
		t.Error("the owner must be told it is off, with no field to fill in")
	}
	if code, _ := postForm(t, app.URL+"/services/lifetime", "sess=family", "service=club&days=7"); code != 409 {
		t.Errorf("setting a lifetime while it is switched off: %d, want 409", code)
	}
	if code, body := postForm(t, app.URL+"/services/takeback", "sess=other", "service=club"); code != 409 || !strings.Contains(body, "switched off by the administrator") {
		t.Errorf("taking back while it is switched off: %d %s", code, body)
	}
}

func TestTakingBackWithNothingHandedOverIsRefusedWithAReason(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withClub(t, &st), func(srv *web.Server) { srv.Hub.ReceiverDelete = true })
	if sender := get(t, app.URL, "sess=other"); strings.Contains(sender, "Take back the last hand-over") {
		t.Error("no button when nothing was handed over")
	}
	if code, body := postForm(t, app.URL+"/services/takeback", "sess=other", "service=club"); code != 409 || !strings.Contains(body, "there is nothing to take back") {
		t.Errorf("%d %s", code, body)
	}
	// once something was handed over, the button is there
	links, _ := st.Links(context.Background())
	if err := st.AddHandoverItem(context.Background(), links[0].ID, "b1", uuid.New(), uuid.New(), false); err != nil {
		t.Fatal(err)
	}
	sender := get(t, app.URL, "sess=other")
	if !strings.Contains(sender, "Take back the last hand-over") || !strings.Contains(sender, "1 photo handed over") {
		t.Errorf("the sender must be offered to take back the last hand-over")
	}
}
