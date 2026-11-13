package web_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/vfuoglio/immich-handoff/internal/store"
)

func TestTheOwnerSetsHowLongWhatGuestsLeaveIsKept(t *testing.T) {
	var st *store.Store
	token := strings.Repeat("e1", 16)
	end := time.Now().Add(72 * time.Hour)
	app, _ := setup(t, withDrop(t, &st, &storing{}, store.DropBox{Token: token, Name: "Wedding", InboxTag: "inbox/Wedding", ExpiresAt: end, KeepDays: 30}))
	id := firstBox(t, st)

	page := get(t, app.URL, "sess=family")
	for _, want := range []string{`name="keepdays"`, `data-event`, "The event: what is kept afterwards", "goes on "} {
		if !strings.Contains(page, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if code, _ := postForm(t, app.URL+"/dropbox/event", "sess=family", "service="+id+"&keepdays=90"); code != 204 {
		t.Fatalf("saving: %d", code)
	}
	if got, _ := st.DropBox(context.Background(), id); got.KeepDays != 90 {
		t.Errorf("keep days: %d", got.KeepDays)
	}
	if code, _ := postForm(t, app.URL+"/dropbox/event", "sess=family", "service="+id+"&keepdays=soon"); code != 409 {
		t.Errorf("not a number: %d", code)
	}
	if code, _ := postForm(t, app.URL+"/dropbox/event", "sess=other", "service="+id+"&keepdays=1"); code != 409 {
		t.Errorf("somebody else's box: %d", code)
	}
	if code, _ := postForm(t, app.URL+"/dropbox/event", "", "service="+id+"&keepdays=1"); code == 204 {
		t.Error("signed out")
	}
}
