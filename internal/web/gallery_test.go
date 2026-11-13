package web_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
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

func postTo(t *testing.T, c *http.Client, u, form string) (int, http.Header, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", u, strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, res.Header, string(b)
}

func TestAModeratorWithNoAccountApprovesAndRejectsWhatWaits(t *testing.T) {
	var st *store.Store
	token := strings.Repeat("6b", 16)
	mod := strings.Repeat("c4", 32)
	im := &storing{}
	app, _ := setup(t, withDrop(t, &st, im, store.DropBox{Token: token, Name: "Wedding", InboxTag: "inbox/Wedding", Approve: true, ModToken: mod}))
	g := guest(t)
	for _, n := range []string{"a.jpg", "b.jpg", "c.jpg"} {
		if code, _ := sendFiles(t, app.URL+"/drop/"+token+"/upload", nil, map[string][]byte{n: jpegBytes}); code != 200 {
			t.Fatal("upload")
		}
	}
	code, hdr, page := fetch(t, g, "GET", app.URL+"/mod/"+mod)
	if code != 200 || !strings.Contains(page, "Wedding") || !strings.Contains(page, "Approve chosen") || !strings.Contains(page, `class="count-badge">3<`) || hdr.Get("Cache-Control") != "no-store" {
		t.Fatalf("the moderator's page: %d %.300s", code, page)
	}
	// nothing of the owner's side: no link, no settings, no way to end the box
	for _, bad := range []string{token, "Revoke", "Extend", "Co-hosts", "data-link", "handoff-drop-"} {
		if strings.Contains(page, bad) {
			t.Errorf("the moderator's page shows %q", bad)
		}
	}
	m := regexp.MustCompile(`src="mod/`+mod+`/pending/([0-9a-f-]{36})"`).FindAllStringSubmatch(page, -1)
	if len(m) != 3 {
		t.Fatalf("thumbnails: %d", len(m))
	}
	if code, hdr, body := fetch(t, g, "GET", app.URL+"/mod/"+mod+"/pending/"+m[0][1]); code != 200 || len(body) != len(jpegBytes) || !strings.Contains(hdr.Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("a waiting file: %d %v", code, hdr)
	}
	if code, _, _ := fetch(t, g, "GET", app.URL+"/mod/"+mod+"/pending/"+uuidText()); code != 404 {
		t.Errorf("a file that is not there: %d", code)
	}

	// choose nothing: asked to choose; approve one; reject one; approve the rest
	if code, hdr, _ := postTo(t, g, app.URL+"/mod/"+mod+"/approve", ""); code != 303 || !strings.Contains(hdr.Get("Location"), "nothing=1") {
		t.Errorf("nothing chosen: %d %v", code, hdr.Get("Location"))
	}
	code, hdr, _ = postTo(t, g, app.URL+"/mod/"+mod+"/approve", "item="+m[0][1])
	if code != 303 || !strings.Contains(hdr.Get("Location"), "done=1") || !strings.Contains(hdr.Get("Location"), "did=approved") || !strings.HasPrefix(hdr.Get("Location"), "../"+mod+"?") {
		t.Fatalf("approve one: %d %v", code, hdr.Get("Location"))
	}
	if len(im.names) != 1 {
		t.Errorf("an approved file reaches the library: %v", im.names)
	}
	if code, hdr, _ = postTo(t, g, app.URL+"/mod/"+mod+"/reject", "item="+m[1][1]); code != 303 || !strings.Contains(hdr.Get("Location"), "did=rejected") {
		t.Errorf("reject one: %d %v", code, hdr.Get("Location"))
	}
	if len(im.names) != 1 {
		t.Errorf("a rejected file does not: %v", im.names)
	}
	_, _, after := fetch(t, g, "GET", app.URL+"/mod/"+mod+"?done=1&did=rejected")
	if !strings.Contains(after, "1 file rejected.") || !strings.Contains(after, `class="count-badge">1<`) {
		t.Errorf("the page says how it went: %.400s", after)
	}
	if code, _, _ = postTo(t, g, app.URL+"/mod/"+mod+"/approve", "all=1&item="+m[2][1]); code != 303 || len(im.names) != 2 {
		t.Errorf("approve all: %d (%d stored)", code, len(im.names))
	}
	_, _, empty := fetch(t, g, "GET", app.URL+"/mod/"+mod)
	if !strings.Contains(empty, "Nothing is waiting") {
		t.Errorf("an empty queue says so: %.200s", empty)
	}

	// a wrong, a guest's or an ended link is no page
	for _, path := range []string{"/mod/" + strings.Repeat("0", 64), "/mod/" + token, "/mod/short"} {
		if code, _, _ := fetch(t, g, "GET", app.URL+path); code != 404 {
			t.Errorf("%s: %d", path, code)
		}
	}
	if code, _, _ := postTo(t, g, app.URL+"/mod/"+strings.Repeat("0", 64)+"/approve", "all=1"); code != 404 {
		t.Errorf("approving through a wrong link: %d", code)
	}
}

func TestOnlyTheOwnerMakesAndEndsTheModeratorLink(t *testing.T) {
	var st *store.Store
	token := strings.Repeat("8d", 16)
	app, _ := setup(t, withDrop(t, &st, &storing{}, store.DropBox{Token: token, Name: "Party", InboxTag: "inbox/Party"}))
	id := firstBox(t, st)
	if page := get(t, app.URL, "sess=family"); !strings.Contains(page, "Make a moderator link") {
		t.Error("the owner is offered a moderator link")
	}
	if code, _ := postForm(t, app.URL+"/dropbox/moderator", "sess=other", "service="+id+"&on=1"); code != 409 {
		t.Errorf("somebody else's box: %d", code)
	}
	if code, _ := postForm(t, app.URL+"/dropbox/moderator", "sess=family", "service="+id+"&on=1"); code != 204 {
		t.Fatalf("make: %d", code)
	}
	box, _ := st.DropBox(context.Background(), id)
	if len(box.ModToken) < 64 {
		t.Fatalf("token: %q", box.ModToken)
	}
	page := get(t, app.URL, "sess=family")
	if !strings.Contains(page, `data-link="mod/`+box.ModToken+`"`) || !strings.Contains(page, "End the moderator link") {
		t.Error("the owner sees the link and can end it")
	}
	if code, _, _ := fetch(t, guest(t), "GET", app.URL+"/mod/"+box.ModToken); code != 200 {
		t.Errorf("the link works with no sign-in: %d", code)
	}
	if code, _ := postForm(t, app.URL+"/dropbox/moderator", "sess=family", "service="+id+"&on=0"); code != 204 {
		t.Fatal("end")
	}
	if code, _, _ := fetch(t, guest(t), "GET", app.URL+"/mod/"+box.ModToken); code != 404 {
		t.Errorf("the ended link: %d", code)
	}
}

func uuidText() string { return "00000000-0000-0000-0000-000000000000" }

func TestTheWallCaptionsAPhotoWithItsPromptAndItsSender(t *testing.T) {
	var st *store.Store
	token := strings.Repeat("4a", 16)
	wall := strings.Repeat("d7", 32)
	app, _ := setup(t, withDrop(t, &st, &storing{}, store.DropBox{Token: token, Name: "Wedding", InboxTag: "inbox/Wedding", Wall: wall, GalleryNames: true, Gallery: true,
		Prompts: []store.Prompt{{ID: "p1", Text: "The cake"}}}))
	send := func(fields map[string]string) {
		if code, _ := sendFiles(t, app.URL+"/drop/"+token+"/upload", fields, map[string][]byte{"a.jpg": append(jpegBytes, byte(len(fields)))}); code != 200 {
			t.Fatal("upload")
		}
	}
	send(map[string]string{"guest": "Ann", "prompt": "p1"})
	send(map[string]string{"guest": "Eve"})
	send(map[string]string{"prompt": "p1"})
	code, _, body := fetch(t, guest(t), "GET", app.URL+"/wall/"+wall+"/feed")
	if code != 200 {
		t.Fatal(code)
	}
	var feed struct {
		Items []struct {
			Caption string `json:"caption"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(body), &feed); err != nil || len(feed.Items) != 3 {
		t.Fatalf("feed: %s %v", body, err)
	}
	got := []string{feed.Items[0].Caption, feed.Items[1].Caption, feed.Items[2].Caption}
	want := []string{"The cake · by Ann", "by Eve", "The cake"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("caption %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestTheOwnerDownloadsTheBoxAsAZipAndNobodyElse(t *testing.T) {
	var st *store.Store
	token := strings.Repeat("1b", 16)
	app, _ := setup(t, withDrop(t, &st, &storing{}, store.DropBox{Token: token, Name: "Trip", InboxTag: "inbox/Trip"}))
	if code, _ := sendFiles(t, app.URL+"/drop/"+token+"/upload", nil, map[string][]byte{"a.jpg": jpegBytes}); code != 200 {
		t.Fatal("upload")
	}
	id := firstBox(t, st)
	if page := get(t, app.URL, "sess=family"); !strings.Contains(page, `href="dropbox/zip/`+id+`"`) || !strings.Contains(page, "Download every photo as a ZIP") {
		t.Error("the owner is offered the ZIP")
	}
	req, _ := http.NewRequest("GET", app.URL+"/dropbox/zip/"+id, nil)
	req.Header.Set("Cookie", "sess=family")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	zr, zerr := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "application/zip" || !strings.Contains(res.Header.Get("Content-Disposition"), "Trip photos.zip") || zerr != nil || len(zr.File) != 1 || zr.File[0].Name != "IMG_0042.jpg" {
		t.Fatalf("zip: %d %v %v", res.StatusCode, res.Header, zerr)
	}
	for who, want := range map[string]int{"sess=other": 404, "": 401} {
		if code, _ := do(t, "GET", app.URL+"/dropbox/zip/"+id, who, nil); code != want {
			t.Errorf("%q: %d, want %d", who, code, want)
		}
	}
	if code, _ := do(t, "GET", app.URL+"/dropbox/zip/"+id, "sess=family", map[string]string{"Sec-Fetch-Site": "cross-site"}); code != 403 {
		t.Errorf("started from another site: %d", code)
	}
	if code, _ := do(t, "GET", app.URL+"/dropbox/zip/"+id, "sess=family", map[string]string{"Sec-Fetch-Site": "same-origin"}); code != 200 {
		t.Errorf("from this site: %d", code)
	}
}

func TestAGuestDownloadsAllThePhotosAsAZipWhereTheBoxAllowsDownloads(t *testing.T) {
	var st *store.Store
	token := strings.Repeat("0d", 16)
	app, _ := setup(t, withDrop(t, &st, &storing{}, store.DropBox{Token: token, Name: "Trip", InboxTag: "inbox/Trip", Gallery: true, GalleryDownload: true}))
	g := guest(t)
	base := app.URL + "/drop/" + token
	for _, n := range []string{"a.jpg", "b.jpg"} {
		if code, _ := sendFiles(t, base+"/upload", nil, map[string][]byte{n: append(jpegBytes, n[0])}); code != 200 {
			t.Fatal("upload")
		}
	}
	if _, _, page := fetch(t, g, "GET", base); !strings.Contains(page, `href="drop/`+token+`/zip"`) || !strings.Contains(page, "Download all as a ZIP") {
		t.Error("the page offers the ZIP")
	}
	code, hdr, body := fetch(t, g, "GET", base+"/zip")
	zr, err := zip.NewReader(strings.NewReader(body), int64(len(body)))
	if code != 200 || hdr.Get("Content-Type") != "application/zip" || !strings.Contains(hdr.Get("Content-Disposition"), "Trip photos.zip") || err != nil || len(zr.File) != 2 {
		t.Fatalf("zip: %d %v %v", code, hdr.Get("Content-Disposition"), err)
	}
	if zr.File[0].Name != "IMG_0042.jpg" || zr.File[1].Name != "IMG_0042 (2).jpg" {
		t.Errorf("names: %s, %s", zr.File[0].Name, zr.File[1].Name)
	}

	// no downloads, no ZIP and no button; no gallery, nothing
	st.SetDropGallery(context.Background(), firstBox(t, st), true, false, false)
	if code, _, _ := fetch(t, g, "GET", base+"/zip"); code != 404 {
		t.Errorf("a gallery that does not allow downloads: %d", code)
	}
	if _, _, page := fetch(t, g, "GET", base); strings.Contains(page, "Download all as a ZIP") {
		t.Error("no button without downloads")
	}
	st.SetDropGallery(context.Background(), firstBox(t, st), false, false, false)
	if code, _, _ := fetch(t, g, "GET", base+"/zip"); code != 404 {
		t.Errorf("no gallery: %d", code)
	}
}

func TestAModeratorPinsAndHidesThePhotosTheBoxKept(t *testing.T) {
	var st *store.Store
	token := strings.Repeat("5e", 16)
	mod := strings.Repeat("a9", 32)
	app, _ := setup(t, withDrop(t, &st, &storing{}, store.DropBox{Token: token, Name: "Gala", InboxTag: "inbox/Gala", Gallery: true, GalleryNames: true, ModToken: mod}))
	g := guest(t)
	base := app.URL + "/mod/" + mod
	for i, n := range []string{"a.jpg", "b.jpg"} {
		if code, _ := sendFiles(t, app.URL+"/drop/"+token+"/upload", map[string]string{"guest": "Ann"}, map[string][]byte{n: append(jpegBytes, byte(i))}); code != 200 {
			t.Fatal("upload")
		}
	}
	_, _, page := fetch(t, g, "GET", base)
	for _, want := range []string{"The photos kept", `name="do" value="pin"`, `name="do" value="hide"`, `src="mod/` + mod + `/photo/`, "by Ann"} {
		if !strings.Contains(page, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	photos := regexp.MustCompile(`id="photo-(\d+)"`).FindAllStringSubmatch(page, -1)
	if len(photos) != 2 {
		t.Fatalf("photos: %d", len(photos))
	}
	first := photos[0][1] // the newest
	if code, _, pic := fetch(t, g, "GET", base+"/photo/"+first); code != 200 || len(pic) != len(jpegBytes) {
		t.Errorf("the picture: %d", code)
	}
	if code, _, _ := fetch(t, g, "GET", base+"/photo/99999"); code != 404 {
		t.Errorf("a photo that is not there: %d", code)
	}

	// hide it: guests no longer see it; the moderator sees it marked, and can show it again
	code, hdr, _ := postTo(t, g, base+"/mark", "item="+first+"&do=hide")
	if code != 303 || !strings.HasPrefix(hdr.Get("Location"), "../"+mod) || !strings.HasSuffix(hdr.Get("Location"), "#photo-"+first) {
		t.Fatalf("hide: %d %v", code, hdr.Get("Location"))
	}
	_, _, feed := fetch(t, g, "GET", app.URL+"/drop/"+token+"/gallery")
	if strings.Count(feed, `"id":`) != 1 || strings.Contains(feed, `"id":`+first+`,`) {
		t.Errorf("guests see: %s", feed)
	}
	if _, _, page = fetch(t, g, "GET", base); !strings.Contains(page, `value="show"`) || !strings.Contains(page, "is-hidden") {
		t.Error("the moderator sees the hidden one, marked, with a way to show it")
	}
	if code, _, _ := fetch(t, g, "GET", app.URL+"/drop/"+token+"/photo/"+first); code != 404 {
		t.Errorf("a hidden photo's picture for a guest: %d", code)
	}
	postTo(t, g, base+"/mark", "item="+first+"&do=pin")
	if _, _, page = fetch(t, g, "GET", base); !strings.Contains(page, "is-pinned") || !strings.Contains(page, `value="unpin"`) {
		t.Error("a pinned photo is marked")
	}
	postTo(t, g, base+"/mark", "item="+first+"&do=show")
	if _, _, feed = fetch(t, g, "GET", app.URL+"/drop/"+token+"/gallery"); strings.Count(feed, `"id":`) != 2 {
		t.Errorf("shown again: %s", feed)
	}

	// bad requests and a wrong link
	for _, form := range []string{"item=abc&do=hide", "item=" + first + "&do=delete", "item=99999&do=hide", ""} {
		if code, _, _ := postTo(t, g, base+"/mark", form); code != 404 {
			t.Errorf("%q: %d", form, code)
		}
	}
	if code, _, _ := postTo(t, g, app.URL+"/mod/"+strings.Repeat("0", 64)+"/mark", "item="+first+"&do=hide"); code != 404 {
		t.Errorf("a wrong link: %d", code)
	}
}

func TestAModeratorOfABoxWithNoGalleryOrWallDoesNotSeeThePhotos(t *testing.T) {
	var st *store.Store
	token := strings.Repeat("6f", 16)
	mod := strings.Repeat("b8", 32)
	app, _ := setup(t, withDrop(t, &st, &storing{}, store.DropBox{Token: token, Name: "Quiet", InboxTag: "inbox/Quiet", ModToken: mod}))
	sendFiles(t, app.URL+"/drop/"+token+"/upload", nil, map[string][]byte{"a.jpg": jpegBytes})
	g := guest(t)
	if _, _, page := fetch(t, g, "GET", app.URL+"/mod/"+mod); strings.Contains(page, "The photos kept") {
		t.Error("the key cannot view pictures, so the page does not offer them")
	}
	if code, _, _ := fetch(t, g, "GET", app.URL+"/mod/"+mod+"/photo/1"); code != 404 {
		t.Errorf("the picture: %d", code)
	}
}

func TestTheOwnerMakesTheAlbumOfLovedPhotos(t *testing.T) {
	var st *store.Store
	token := strings.Repeat("7a", 16)
	app, _ := setup(t, withDrop(t, &st, &storing{}, store.DropBox{Token: token, Name: "Party", InboxTag: "inbox/Party"}))
	id := firstBox(t, st)
	if page := get(t, app.URL, "sess=family"); !strings.Contains(page, "Make the album of loved photos") || !strings.Contains(page, `hx-post="dropbox/loved"`) {
		t.Error("the owner is offered the album of loved photos")
	}
	if code, _ := postForm(t, app.URL+"/dropbox/loved", "sess=other", "service="+id); code != 409 {
		t.Errorf("somebody else's box: %d", code)
	}
	if code, body := postForm(t, app.URL+"/dropbox/loved", "sess=family", "service="+id); code != 409 || !strings.Contains(body, "no photo has a heart") {
		t.Errorf("nothing is loved yet: %d %s", code, body)
	}
}

func TestAHostPutsTheirTitleAndLogoOnTheGuestPagesTheAlbumAndTheWall(t *testing.T) {
	var st *store.Store
	token := strings.Repeat("8b", 16)
	wall := strings.Repeat("e2", 32)
	app, _ := setup(t, withDrop(t, &st, &storing{}, store.DropBox{Token: token, Name: "rossi-2026-06", InboxTag: "inbox/rossi-2026-06", Wall: wall, Accent: "#ffe066", Gallery: true}))
	id := firstBox(t, st)
	form := func(fields map[string]string, logo []byte) int {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		mw.WriteField("service", id)
		for k, v := range fields {
			mw.WriteField(k, v)
		}
		if logo != nil {
			fw, _ := mw.CreateFormFile("logo", "logo.png")
			fw.Write(logo)
		}
		mw.Close()
		req, _ := http.NewRequest("POST", app.URL+"/dropbox/look", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.Header.Set("Cookie", "sess=family")
		req.Header.Set("X-Requested-With", "immich-handoff")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	g := guest(t)
	base := app.URL + "/drop/" + token

	// before: the box's own name, no logo
	if _, _, page := fetch(t, g, "GET", base); !strings.Contains(page, "rossi-2026-06") || strings.Contains(page, "drop-logo") {
		t.Error("without a title the name is shown, and no logo")
	}
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	for name, logo := range map[string][]byte{"an SVG": svg, "a page": []byte("<html>x</html>"), "too big": append(append([]byte{}, pngBytes...), make([]byte, 301<<10)...)} {
		if code := form(map[string]string{"title": "Nope"}, logo); code != 409 {
			t.Errorf("%s as a logo: %d, want 409", name, code)
		}
	}
	if code := form(map[string]string{"title": strings.Repeat("x", 61)}, nil); code != 409 {
		t.Errorf("a title that is too long: %d", code)
	}
	if code := form(map[string]string{"title": "Anna & Marco's wedding", "accent": "#ffe066"}, pngBytes); code != 204 {
		t.Fatalf("title and logo: %d", code)
	}

	// after: the title everywhere guests look, the box's name nowhere
	_, _, page := fetch(t, g, "GET", base)
	if !strings.Contains(page, "Anna &amp; Marco&#39;s wedding") || strings.Contains(page, "rossi-2026-06") || !strings.Contains(page, `src="drop/`+token+`/logo"`) {
		t.Errorf("the guest page: %.600s", page)
	}
	if code, hdr, logo := fetch(t, g, "GET", base+"/logo"); code != 200 || hdr.Get("Content-Type") != "image/png" || len(logo) != len(pngBytes) || !strings.Contains(hdr.Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("the logo: %d %v", code, hdr)
	}
	if _, _, m := fetch(t, g, "GET", base+"/manifest.webmanifest"); !strings.Contains(m, "Anna \\u0026 Marco's wedding") && !strings.Contains(m, "Anna & Marco's wedding") {
		t.Errorf("the installed app's name: %s", m)
	}
	if _, _, w := fetch(t, g, "GET", app.URL+"/wall/"+wall); !strings.Contains(w, `class="wall-logo"`) || !strings.Contains(w, "Marco") || strings.Contains(w, "rossi-2026-06") {
		t.Errorf("the wall: %.500s", w)
	}
	if code, _, _ := fetch(t, g, "GET", app.URL+"/wall/"+wall+"/logo"); code != 200 {
		t.Errorf("the wall's logo: %d", code)
	}
	if code, _, _ := fetch(t, g, "GET", app.URL+"/wall/"+strings.Repeat("0", 64)+"/logo"); code != 404 {
		t.Errorf("a wrong wall: %d", code)
	}
	// the host's colour is made to read: the pale yellow gets a darker ink for a light page
	if _, _, css := fetch(t, g, "GET", base+"/theme.css"); !strings.Contains(css, "--accent:#ffe066") || !strings.Contains(css, "--on-accent:#111111") || !strings.Contains(css, "--accent-ink:light-dark(#") || strings.Contains(css, "light-dark(#ffe066,") {
		t.Errorf("theme: %s", css)
	}
	// the owner's page keeps the real name; the album after the end carries the title and the logo
	if owner := get(t, app.URL, "sess=family"); !strings.Contains(owner, "rossi-2026-06") || !strings.Contains(owner, "Remove the logo") {
		t.Error("the owner still sees the box's name and can remove the logo")
	}
	st.SetDropSchedule(context.Background(), id, time.Time{}, time.Now().Add(-time.Hour))
	if _, _, album := fetch(t, g, "GET", base); !strings.Contains(album, "This event is over") || !strings.Contains(album, "drop-logo") || !strings.Contains(album, "Marco") {
		t.Errorf("the album: %.500s", album)
	}

	// taking the logo away, and the title
	if code := form(map[string]string{"title": "", "nologo": "1", "accent": "#ffe066"}, nil); code != 204 {
		t.Fatal(code)
	}
	if code, _, _ := fetch(t, g, "GET", base+"/logo"); code != 404 {
		t.Errorf("the logo after removing it: %d", code)
	}
}
