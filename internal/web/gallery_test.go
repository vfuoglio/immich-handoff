package web_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vfuoglio/immich-handoff/internal/store"
)

// guest is a browser that keeps cookies, so a guest's secret travels from the page to the heart.
func guest(t *testing.T) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func fetch(t *testing.T, c *http.Client, method, u string) (int, http.Header, string) {
	t.Helper()
	req, _ := http.NewRequest(method, u, nil)
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, res.Header, string(b)
}

type feedOut struct {
	Items []struct {
		ID      int64  `json:"id"`
		Name    string `json:"name"`
		Caption string `json:"caption"`
		Hearts  int    `json:"hearts"`
		Mine    bool   `json:"mine"`
	} `json:"items"`
	Download bool `json:"download"`
}

func TestAGuestLooksAtHeartsAndDownloadsThePhotosOfAnEvent(t *testing.T) {
	var st *store.Store
	token := strings.Repeat("9a", 16)
	app, _ := setup(t, withDrop(t, &st, &storing{}, store.DropBox{Token: token, Name: "Wedding", InboxTag: "inbox/Wedding", Gallery: true, GalleryDownload: true, GalleryNames: true}))
	g := guest(t)
	base := app.URL + "/drop/" + token

	code, _, page := fetch(t, g, "GET", base)
	for _, want := range []string{"data-gallery", `data-feed="drop/` + token + `/gallery"`, `name="guest"`, "data-download", "The photos so far"} {
		if code != 200 || !strings.Contains(page, want) {
			t.Fatalf("the page lacks %q (%d)", want, code)
		}
	}
	u, _ := url.Parse(app.URL)
	if len(g.Jar.Cookies(u)) == 0 {
		t.Fatal("the guest is given a secret so a heart can be found again")
	}
	if code, _, _ := fetch(t, g, "GET", base+"/gallery"); code != 200 {
		t.Fatalf("empty feed: %d", code)
	}

	// photos sent with a name appear in the feed with it
	if code, body := sendFiles(t, base+"/upload", map[string]string{"guest": "  Ann <Lee> "}, map[string][]byte{"a.jpg": jpegBytes}); code != 200 || !strings.Contains(body, "Received") {
		t.Fatalf("upload: %d %s", code, body)
	}
	_, hdr, body := fetch(t, g, "GET", base+"/gallery")
	var feed feedOut
	if err := json.Unmarshal([]byte(body), &feed); err != nil || len(feed.Items) != 1 || feed.Items[0].Name != "Ann Lee" || !feed.Download || feed.Items[0].Hearts != 0 {
		t.Fatalf("feed: %s %v", body, err)
	}
	if hdr.Get("Cache-Control") != "no-store" || !strings.HasPrefix(hdr.Get("Content-Type"), "application/json") {
		t.Errorf("headers: %v", hdr)
	}
	idText := strconv.FormatInt(feed.Items[0].ID, 10)

	// the picture, and nothing for what is not the box's
	code, hdr, pic := fetch(t, g, "GET", base+"/photo/"+idText)
	if code != 200 || hdr.Get("Content-Type") != "image/jpeg" || len(pic) != len(jpegBytes) || !strings.Contains(hdr.Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("picture: %d %v", code, hdr)
	}
	if code, _, _ := fetch(t, g, "GET", base+"/photo/99999"); code != 404 {
		t.Errorf("a photo that is not there: %d", code)
	}
	if code, _, _ := fetch(t, g, "GET", base+"/photo/abc"); code != 404 {
		t.Errorf("not a number: %d", code)
	}

	// a heart is given, counted, and taken back by the same tap
	code, _, body = fetch(t, g, "POST", base+"/heart/"+idText)
	if code != 200 || !strings.Contains(body, `"hearted":true`) || !strings.Contains(body, `"hearts":1`) {
		t.Fatalf("heart: %d %s", code, body)
	}
	_, _, body = fetch(t, g, "GET", base+"/gallery")
	json.Unmarshal([]byte(body), &feed)
	if feed.Items[0].Hearts != 1 || !feed.Items[0].Mine {
		t.Errorf("mine: %s", body)
	}
	_, _, body = fetch(t, guest(t), "GET", base+"/gallery") // another guest's browser
	var other feedOut
	json.Unmarshal([]byte(body), &other)
	if other.Items[0].Hearts != 1 || other.Items[0].Mine {
		t.Errorf("a stranger sees the count but not whose it is: %s", body)
	}
	if _, _, body = fetch(t, g, "POST", base+"/heart/"+idText); !strings.Contains(body, `"hearted":false`) || !strings.Contains(body, `"hearts":0`) {
		t.Errorf("taken back: %s", body)
	}
	if code, _, _ := fetch(t, g, "POST", base+"/heart/99999"); code != 404 {
		t.Errorf("a heart on a photo that is not there: %d", code)
	}

	// the original, saved under a clean name
	code, hdr, orig := fetch(t, g, "GET", base+"/original/"+idText)
	if code != 200 || len(orig) != len(jpegBytes) || !strings.Contains(hdr.Get("Content-Disposition"), "attachment") || !strings.Contains(hdr.Get("Content-Disposition"), `filename=IMG_0042.jpg`) {
		t.Errorf("original: %d %v", code, hdr)
	}
}

func TestABoxWithNoGalleryOrNoDownloadsGivesGuestsNothingToLookAtOrSave(t *testing.T) {
	var st *store.Store
	token := strings.Repeat("7c", 16)
	app, _ := setup(t, withDrop(t, &st, &storing{}, store.DropBox{Token: token, Name: "Private", InboxTag: "inbox/Private"}))
	base := app.URL + "/drop/" + token
	g := guest(t)
	if code, _ := sendFiles(t, base+"/upload", nil, map[string][]byte{"a.jpg": jpegBytes}); code != 200 {
		t.Fatal("upload")
	}
	_, _, page := fetch(t, g, "GET", base)
	if strings.Contains(page, "data-gallery") || strings.Contains(page, `name="guest"`) || !strings.Contains(page, "will not see anyone else") {
		t.Error("a box with no gallery says so and shows none")
	}
	for _, path := range []string{"/gallery", "/photo/1", "/original/1"} {
		if code, _, _ := fetch(t, g, "GET", base+path); code != 404 {
			t.Errorf("%s: %d", path, code)
		}
	}
	if code, _, _ := fetch(t, g, "POST", base+"/heart/1"); code != 404 {
		t.Errorf("heart: %d", code)
	}
	// a gallery that does not allow downloads gives no originals
	if err := st.SetDropGallery(context.Background(), firstBox(t, st), true, false, false); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := fetch(t, g, "GET", base+"/original/1"); code != 404 {
		t.Errorf("original with no download: %d", code)
	}
	if code, _, body := fetch(t, g, "GET", base+"/gallery"); code != 200 || !strings.Contains(body, `"download":false`) {
		t.Errorf("the feed says no download: %d %s", code, body)
	}
}

func TestAGalleryFollowsThePagePassword(t *testing.T) {
	var st *store.Store
	token := strings.Repeat("5d", 16)
	app, _ := setup(t, withDrop(t, &st, &storing{}, store.DropBox{Token: token, Name: "Locked", InboxTag: "inbox/Locked", Gallery: true, Password: "x", PasswordScope: store.DropScopePage}))
	// a password that guards the whole page guards the gallery too (the hash above is not a real one, so it is never given)
	for _, path := range []string{"/gallery", "/photo/1"} {
		if code, _, _ := fetch(t, guest(t), "GET", app.URL+"/drop/"+token+path); code != 404 {
			t.Errorf("%s without the password: %d", path, code)
		}
	}
}

func TestAnEndedEventShowsItsGalleryUntilWhatGuestsLeftIsGone(t *testing.T) {
	var st *store.Store
	token := strings.Repeat("3e", 16)
	ended := time.Now().Add(-24 * time.Hour)
	app, _ := setup(t, withDrop(t, &st, &storing{}, store.DropBox{Token: token, Name: "Over", InboxTag: "inbox/Over", Gallery: true, ExpiresAt: ended, KeepDays: 7}))
	g := guest(t)
	base := app.URL + "/drop/" + token
	code, _, page := fetch(t, g, "GET", base)
	if code != 200 || !strings.Contains(page, "This event is over") || !strings.Contains(page, "data-gallery") || strings.Contains(page, `type="file"`) {
		t.Fatalf("the ended event shows its gallery and no form: %d %.300s", code, page)
	}
	if code, _ := sendFiles(t, base+"/upload", nil, map[string][]byte{"a.jpg": jpegBytes}); code != 404 {
		t.Errorf("nothing can be sent after the end: %d", code)
	}
	if code, _, _ := fetch(t, g, "GET", base+"/gallery"); code != 200 {
		t.Errorf("the feed while it is kept: %d", code)
	}
	st.SetDropSchedule(context.Background(), firstBox(t, st), time.Time{}, time.Now().Add(-9*24*time.Hour))
	_, _, page = fetch(t, g, "GET", base)
	if !strings.Contains(page, "This link is not open") {
		t.Errorf("past the retention the link is closed: %.200s", page)
	}
	if code, _, _ := fetch(t, g, "GET", base+"/gallery"); code != 404 {
		t.Errorf("the feed past the retention: %d", code)
	}
}

func TestTheOwnerSwitchesTheGalleryOnFromTheEventSettings(t *testing.T) {
	var st *store.Store
	token := strings.Repeat("2f", 16)
	app, _ := setup(t, withDrop(t, &st, &storing{}, store.DropBox{Token: token, Name: "Switch", InboxTag: "inbox/Switch"}))
	id := firstBox(t, st)
	page := get(t, app.URL, "sess=family")
	for _, want := range []string{`name="gallery"`, `name="gallerydownload"`, `name="gallerynames"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the page lacks %s", want)
		}
	}
	if code, body := postForm(t, app.URL+"/dropbox/event", "sess=family", "service="+id+"&keepdays=10&gallery=1&gallerydownload=1&gallerynames=1"); code != 204 && code != 409 {
		t.Fatalf("saving: %d %s", code, body)
	}
}
