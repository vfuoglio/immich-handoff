package web_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/vfuoglio/immich-handoff/internal/store"
)

func TestServicesAreRowsGroupedByWhatThePersonDoesWithThem(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withClub(t, &st))

	// a sender: one row, so it starts open; Leave sits apart in its own zone, after the other actions
	sender := get(t, app.URL, "sess=other")
	has(t, sender, "<h2>You send to</h2>", `<details class="svc card" name="svc" open>`, `class="badge`, `>Pause sending<`)
	lacks(t, sender, "<h2>You receive</h2>", "<h2>You offer</h2>")
	zone := regexp.MustCompile(`(?s)<div class="danger-zone">(.*?)</div>`).FindStringSubmatch(sender)
	if zone == nil || !strings.Contains(zone[1], ">Leave<") || !strings.Contains(zone[1], "data-danger") {
		t.Errorf("Leave must sit in the danger zone: %v", zone)
	}
	if strings.Index(sender, ">Pause sending<") > strings.Index(sender, `class="danger-zone"`) {
		t.Error("the safe actions come before the danger zone")
	}

	// the owner of a service: it appears once, among the services they offer, not also among those they receive
	owner := get(t, app.URL, "sess=family")
	has(t, owner, "<h2>You offer</h2>", "<h2>You receive</h2>", "Save limits", ">Delete service<", `>Pause service<`)
	if n := strings.Count(owner, `class="svc card"`); n != 2 {
		t.Errorf("one row for the service they offer and one for the one they receive from: %d rows", n)
	}
	if strings.Count(owner, "club") == 0 || strings.Contains(owner, "<h2>You send to</h2>") {
		t.Error("the owner's sections are wrong")
	}
	zone = regexp.MustCompile(`(?s)<div class="danger-zone">(.*?)</div>`).FindStringSubmatch(owner)
	if zone == nil || !strings.Contains(zone[1], ">Delete service<") || !strings.Contains(zone[1], "Photos already in your library stay") {
		t.Errorf("Delete service must sit in the danger zone and say what it does: %v", zone)
	}
	// several rows start closed
	if strings.Contains(owner, `name="svc" open`) {
		t.Error("with several rows they start closed")
	}
}

func TestEveryQuestionIsAskedInThePagesOwnDialog(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withClub(t, &st))
	for _, c := range []struct{ name, path, cookie string }{
		{"owner", "/", "sess=family"}, {"sender", "/", "sess=other"}, {"admin", "/admin", "sess=admin"},
	} {
		_, body := do(t, "GET", app.URL+c.path, c.cookie, nil)
		has(t, body, `<dialog id="confirm" class="confirm"`, `id="confirm-title"`, `id="confirm-ok"`, `<form method="dialog">`)
		// the script that shows it needs a title for every question; a question without one would read "Please confirm"
		for _, el := range regexp.MustCompile(`<(?:button|form)[^>]*hx-confirm="[^>]*>`).FindAllString(body, -1) {
			if !strings.Contains(el, "data-confirm-title") || !strings.Contains(el, "data-confirm-label") {
				t.Errorf("%s: a question without its own title or button label: %.140s", c.name, el)
			}
		}
	}
	// every destructive action is marked, so the dialog shows its button in the warning colour
	_, sender := do(t, "GET", app.URL+"/", "sess=other", nil)
	_, owner := do(t, "GET", app.URL+"/", "sess=family", nil)
	for _, body := range []string{sender, owner} {
		for _, el := range regexp.MustCompile(`<button[^>]*class="danger"[^>]*>`).FindAllString(body, -1) {
			if !strings.Contains(el, `data-danger="true"`) || !strings.Contains(el, "hx-confirm") {
				t.Errorf("a danger button must ask first and be marked: %.140s", el)
			}
		}
	}
}

func TestARowsNumberRidesAlongWithItsCount(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withClub(t, &st))
	_, frag := do(t, "GET", app.URL+"/fragments/count?service=club&role=sender&sum=sum-send-0", "sess=other", nil)
	has(t, frag, `id="sum-send-0"`, `hx-swap-oob="true"`, `<b data-odo="sum-send-0">2</b>`, "waiting")
	// only the ids the page makes are accepted
	_, frag = do(t, "GET", app.URL+"/fragments/count?service=club&role=sender&sum=x%22%3E%3Cscript", "sess=other", nil)
	lacks(t, frag, "hx-swap-oob", "<script")
	// the row's line has the placeholder that the number lands in
	sender := get(t, app.URL, "sess=other")
	has(t, sender, `id="sum-send-0"`, `hx-get="fragments/count?service=club&amp;role=sender&amp;sum=sum-send-0"`)
}
