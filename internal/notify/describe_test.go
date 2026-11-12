package notify

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDescribeNamesWhatIsConfiguredWithoutItsAddress(t *testing.T) {
	f := newFakeSMTP(t, nil, false, false)
	n := New([]Destination{
		{Name: "phone", Format: FormatNtfy, URL: "https://ntfy.example/secret-topic"},
		{Name: "home", Format: FormatJSON, URL: "https://ha.example/api/webhook/secret-id", Kinds: map[Kind]bool{Backup: true, Problem: true}},
		peopleDest(f),
	}, "", nil)
	defer n.Close()
	got := n.Describe()
	if len(got) != 3 || got[0].Name != "phone" || len(got[0].Kinds) != 0 || got[1].Format != FormatJSON || !got[2].People {
		t.Fatalf("%+v", got)
	}
	if got[1].Kinds[0] != Problem || got[1].Kinds[1] != Backup { // in the documented order, not the order they were written
		t.Errorf("%v", got[1].Kinds)
	}
	for _, d := range got {
		if strings.Contains(d.Name+d.Format, "secret") {
			t.Error("an address leaked")
		}
	}
	var none *Notifier
	if none.Describe() != nil || none.Test(context.Background()) != nil {
		t.Error("no notifier, nothing to describe")
	}
}

func TestATestReachesEveryDestinationWhateverKindsItTakesAndSaysHowEachAnswered(t *testing.T) {
	ok := newSink(t, nil)
	denied := newSink(t, func(int) int { return 401 })
	gone := httptest.NewServer(http.NotFoundHandler())
	goneURL := gone.URL + "/secret-topic"
	gone.Close()
	mailbox := newFakeSMTP(t, nil, false, false)
	var log bytes.Buffer
	n := newMailNotifier(t, nil, &log,
		Destination{Name: "ok", Format: FormatNtfy, URL: ok.URL},
		Destination{Name: "only-backups", Format: FormatJSON, URL: ok.URL, Kinds: map[Kind]bool{Backup: true}},
		Destination{Name: "denied", Format: FormatNtfy, URL: denied.URL, Auth: "wrong"},
		Destination{Name: "gone", Format: FormatNtfy, URL: goneURL},
		mailDest(mailbox, TLSNone, time.Hour), // a long window: a test must not wait for it
		peopleDest(mailbox),
	)
	defer n.Close()
	res := n.Test(context.Background())
	if len(res) != 6 {
		t.Fatalf("%+v", res)
	}
	by := map[string]TestResult{}
	for _, r := range res {
		by[r.Name] = r
	}
	if by["ok"].Err != "" || by["only-backups"].Err != "" || by["mail"].Err != "" {
		t.Errorf("these should be delivered: %+v", res)
	}
	if by["denied"].Err != "answered 401" {
		t.Errorf("denied: %q", by["denied"].Err)
	}
	if !strings.HasPrefix(by["gone"].Err, "could not reach it") || strings.Contains(by["gone"].Err, "secret-topic") || strings.Contains(by["gone"].Err, "127.0.0.1") {
		t.Errorf("gone: %q", by["gone"].Err)
	}
	if !by["people"].People || by["people"].Err != "" {
		t.Errorf("the server for people is not tested here: %+v", by["people"])
	}
	if ok.count() != 2 { // "ok" and "only-backups": a test ignores the kinds
		t.Errorf("%d messages arrived", ok.count())
	}
	if g := ok.hits[0]; !strings.Contains(g.body, "test notification") && !strings.Contains(g.body, `"kind":"test"`) {
		t.Errorf("%q", g.body)
	}
	if denied.count() != 1 {
		t.Errorf("a test makes one attempt, not retries: %d", denied.count())
	}
	if mailbox.count() != 1 {
		t.Errorf("one test mail, to the administrator's list only: %d", mailbox.count())
	}
	p := parseReceived(t, mailbox.wait(t, 1)[0])
	if subj, _ := decodeSubject(p); subj != "immich-handoff: test" {
		t.Errorf("subject %q", subj)
	}
	if strings.Contains(strings.Join([]string{by["denied"].Err, by["gone"].Err}, " "), "wrong") {
		t.Error("the token leaked")
	}
}

func TestATestThatTakesTooLongSaysSoAtOnceWhenTheCallerGivesUp(t *testing.T) {
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer slow.Close()
	defer close(release)
	var log bytes.Buffer
	n := newMailNotifier(t, nil, &log, Destination{Name: "slow", Format: FormatNtfy, URL: slow.URL})
	defer n.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	res := n.Test(ctx)
	if time.Since(start) > 3*time.Second || res[0].Err != "no answer in time" {
		t.Errorf("%v %+v", time.Since(start), res)
	}
}
