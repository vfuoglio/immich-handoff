package web_test

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"net/http"
	"strings"
	"testing"

	"github.com/vfuoglio/immich-handoff/internal/store"
	"github.com/vfuoglio/immich-handoff/internal/web"
)

const (
	myPhoto    = "11111111-0000-4000-8000-000000000001"
	notMyPhoto = "50000000-0000-4000-8000-000000000001"
)

func TestTheButtonLearnsTheServicesThePersonSendsToInTheirLanguage(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withClub(t, &st))
	code, body := do(t, "GET", app.URL+"/immich/services", "sess=other", nil)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var got struct {
		Words    map[string]string
		Services []web.ButtonService
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Services) != 1 || got.Services[0].Name != "club" || !got.Services[0].Copy || got.Services[0].Note != "" {
		t.Errorf("the one service they joined from the page: %+v", got.Services)
	}
	if got.Words["Hand over..."] != "Hand over..." || got.Words["Undo"] == "" {
		t.Errorf("words: %v", got.Words)
	}
	_, german := do(t, "GET", app.URL+"/immich/services", "sess=other", map[string]string{"Accept-Language": "de"})
	if !strings.Contains(german, "Rückgängig") {
		t.Errorf("in German: %s", german)
	}
	// a person who sends to nothing gets an empty list, a stranger to Immich gets 401
	if _, body := do(t, "GET", app.URL+"/immich/services", "sess=alice", nil); !strings.Contains(body, `"services":[]`) {
		t.Errorf("no services: %s", body)
	}
	if code, _ := do(t, "GET", app.URL+"/immich/services", "", nil); code != 401 {
		t.Errorf("signed out: %d", code)
	}
	// the owner's pause is said in words
	if code, _ := postForm(t, app.URL+"/services/pause", "sess=family", "service=club&paused=1"); code != 204 {
		t.Fatal(code)
	}
	if _, body := do(t, "GET", app.URL+"/immich/services", "sess=other", nil); !strings.Contains(body, "The owner paused this service.") {
		t.Errorf("a pause is shown: %s", body)
	}
}

func TestPressingTheButtonTagsThePersonsOwnPhotosAndUndoTakesTheTagOff(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withClub(t, &st))
	buttonTagged.Clear()
	if code, _ := postForm(t, app.URL+"/immich/send", "sess=other", "service=club&asset="+myPhoto); code != 409 {
		t.Errorf("a photo that is alice's, not other's: %d, want 409", code) // the fake says every photo but 5... is alice's
	}
	if code, body := postForm(t, app.URL+"/immich/send", "sess=other", "service=club&asset="+notMyPhoto); code != 200 || !strings.Contains(body, "1 photo handed over to club") {
		t.Fatalf("send: %d %s", code, body)
	}
	if v, _ := buttonTagged.Load(notMyPhoto); v != "on" {
		t.Errorf("tagged: %v", v)
	}
	if code, body := postForm(t, app.URL+"/immich/undo", "sess=other", "service=club&asset="+notMyPhoto); code != 200 || !strings.Contains(body, "Taken off again") {
		t.Fatalf("undo: %d %s", code, body)
	}
	if v, _ := buttonTagged.Load(notMyPhoto); v != "off" {
		t.Errorf("untagged: %v", v)
	}
	if code, _ := postForm(t, app.URL+"/immich/send", "sess=other", "service=club"); code != 409 {
		t.Errorf("nothing chosen: %d", code)
	}
	if code, _ := postForm(t, app.URL+"/immich/send", "sess=alice", "service=club&asset="+myPhoto); code != 403 {
		t.Errorf("someone who is not a sender of the service: %d", code)
	}
	if code, _ := postForm(t, app.URL+"/immich/send", "", "service=club&asset="+notMyPhoto); code != 401 {
		t.Errorf("signed out: %d", code)
	}
}

func TestThePressNeedsTheCrossSiteHeaderAndCanBeSwitchedOff(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withClub(t, &st))
	req, _ := http.NewRequest("POST", app.URL+"/immich/send", strings.NewReader("service=club&asset="+notMyPhoto))
	req.Header.Set("Cookie", "sess=other")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 400 {
		t.Errorf("a form from another site cannot send the header: %d", res.StatusCode)
	}
	off, _ := setup(t, withClub(t, &st), func(s *web.Server) { s.ButtonOff = true })
	if code, _ := do(t, "GET", off.URL+"/immich/services", "sess=other", nil); code != 404 {
		t.Errorf("switched off, the page: %d", code)
	}
	if code, _ := postForm(t, off.URL+"/immich/send", "sess=other", "service=club&asset="+notMyPhoto); code != 404 {
		t.Errorf("switched off, the press: %d", code)
	}
}

func TestAServiceWhoseOwnerDecidesCanBeAskedForFromImmich(t *testing.T) {
	var st *store.Store
	withBoard := func(srv *web.Server) {
		withClub(t, &st)(srv)
		if _, err := st.AddService(context.Background(), store.Service{Name: "board", Tag: "board-tag", InboxTag: "inbox", Refused: "dup", ExpiryDays: 30,
			OwnerID: uuid.MustParse(familyID), OwnerName: "Family", OwnerEmail: "family@x.test", KeyID: uuid.New(), Key: "k-family",
			Policy: store.PolicyApproval, Visibility: store.VisListed}); err != nil {
			t.Fatal(err)
		}
		if err := srv.Hub.Reload(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	app, _ := setup(t, withBoard)
	_, body := do(t, "GET", app.URL+"/immich/services", "sess=other", nil)
	if !strings.Contains(body, `"offers":[{"name":"board","pending":false}]`) {
		t.Fatalf("offered: %s", body)
	}
	if code, body := postForm(t, app.URL+"/immich/ask", "sess=other", "service=board"); code != 200 || !strings.Contains(body, "You asked to join board") {
		t.Fatalf("ask: %d %s", code, body)
	}
	if _, body := do(t, "GET", app.URL+"/immich/services", "sess=other", nil); !strings.Contains(body, `"offers":[{"name":"board","pending":true}]`) {
		t.Errorf("after asking it waits for the owner: %s", body)
	}
	if code, _ := postForm(t, app.URL+"/immich/ask", "sess=other", "service=club"); code != 409 {
		t.Errorf("a service that needs no asking: %d", code)
	}
}

func TestRepeatedPressesAreThrottled(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withClub(t, &st))
	var last int
	for i := 0; i < 40; i++ {
		last, _ = postForm(t, app.URL+"/immich/send", "sess=other", "service=club&asset="+notMyPhoto)
	}
	if last != 429 {
		t.Errorf("the 40th press in a minute: %d, want 429", last)
	}
}
