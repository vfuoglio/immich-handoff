//go:build e2e

package e2e

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestEventPool: three people run a pool. Every photo any of them tags reaches the other two exactly once, tagged with who
// took it, nobody's original is touched, nothing is handed on again, and when the owner ends the pool nothing more moves.
func TestEventPool(t *testing.T) {
	alice, bob, family, grandma := bootstrap(t) // alice owns the pool; bob and family join; grandma comes too late
	bin := buildApp(t)
	port := fmt.Sprint(atoi(appPort) + 65)
	launchApp(t, bin, port, "HANDOFF_STORE_KEY=an-e2e-store-key-0123456789", "DATA_DIR="+t.TempDir())
	ownerC, bobC, familyC, grandmaC := loginCookie(t, "alice@e2e.test"), loginCookie(t, "bob@e2e.test"), loginCookie(t, "family@e2e.test"), loginCookie(t, "grandma@e2e.test")
	must := func(code int, want int, what string) {
		t.Helper()
		if code != want {
			t.Fatalf("%s: HTTP %d, want %d", what, code, want)
		}
	}
	code, body := postBody(t, port, "/services", ownerC, url.Values{"name": {"weekend"}, "tag": {"weekend-tag"}, "policy": {"open"}, "pool": {"1"}})
	must(code, 204, "make the pool: "+body)
	op := page(t, port, ownerC)
	if !strings.Contains(op, "Event pool") || !strings.Contains(op, "End the pool now") {
		t.Fatalf("the owner's page should say it is a pool: %.400s", op)
	}
	if p := keyPermissions(t, alice, "handoff: weekend (receiving)"); !has(p, "asset.upload") || !has(p, "asset.download") || !has(p, "workflow.create") {
		t.Errorf("the owner's key must send and receive: %v", p)
	}

	// joining a pool needs the yes
	code, body = postBody(t, port, "/join", bobC, url.Values{"service": {"weekend"}, "mode": {"copy"}})
	if code != 409 || !strings.Contains(body, "goes to everyone") {
		t.Fatalf("joining without the yes: %d %s", code, body)
	}
	for _, c := range []string{bobC, familyC} {
		code, body = postBody(t, port, "/join", c, url.Values{"service": {"weekend"}, "mode": {"move"}, "pool": {"1"}}) // "move" is overruled
		must(code, 204, "join the pool: "+body)
	}
	for _, u := range []user{alice, bob, family} {
		u := u
		eventually(t, "a workflow for the pool exists", 90*time.Second, func() bool {
			var wfs []struct{ Name string }
			call(t, "GET", "/workflows", keyAuth(u), nil, &wfs)
			for _, w := range wfs {
				if w.Name == "immich-handoff: weekend" {
					return true
				}
			}
			return false
		})
	}

	// everyone takes a photo and tags it
	people := []struct {
		name string
		u    user
		seed uint32
	}{{"alice", alice, 7001}, {"bob", bob, 7002}, {"family", family, 7003}}
	originals := map[string]string{}
	for _, p := range people {
		id, _ := upload(t, p.u, "pool-"+p.name+".jpg", texturedJPEG(p.seed))
		originals[p.name] = id
		tagAsset(t, p.u, "weekend-tag", id)
	}
	for _, p := range people {
		p := p
		eventually(t, p.name+" has the two photos that are not theirs", 150*time.Second, func() bool {
			return len(searchTag(t, p.u, "inbox/weekend", false)) == 2
		})
	}
	for _, p := range people {
		for _, other := range people {
			want := 1
			if other.name == p.name {
				want = 0
			}
			if got := len(searchTag(t, p.u, "from/"+other.name, false)); got != want {
				t.Errorf("%s has %d photos tagged from/%s, want %d", p.name, got, other.name, want)
			}
		}
		if a, c := getAsset(t, p.u, originals[p.name]); c != 200 || a.IsTrashed {
			t.Errorf("%s's original must be untouched: %d %+v", p.name, c, a)
		}
		own, _ := getAsset(t, p.u, originals[p.name])
		for _, tg := range own.Tags {
			if tg.Name == "weekend-tag" {
				t.Errorf("%s's original still carries the pool's tag, so it would be sent again", p.name)
			}
		}
	}

	// nothing loops: a while later nobody has more
	time.Sleep(20 * time.Second)
	for _, p := range people {
		if got := len(searchTag(t, p.u, "inbox/weekend", false)); got != 2 {
			t.Errorf("%s now has %d photos from the others: something was handed on again", p.name, got)
		}
	}

	// the owner ends the pool: nothing more moves, and nobody joins
	code, body = postBody(t, port, "/services/pool", ownerC, url.Values{"service": {"weekend"}, "do": {"end"}})
	must(code, 204, "end the pool: "+body)
	if p := page(t, port, ownerC); !strings.Contains(p, "The pool ended") {
		t.Errorf("the owner should see that it ended: %.300s", p)
	}
	late, _ := upload(t, bob, "pool-late.jpg", texturedJPEG(7004))
	tagAsset(t, bob, "weekend-tag", late)
	time.Sleep(25 * time.Second)
	for _, p := range people {
		if got := len(searchTag(t, p.u, "inbox/weekend", false)); got != 2 {
			t.Errorf("after the end %s has %d photos from the others, want 2", p.name, got)
		}
	}
	if a, _ := getAsset(t, bob, late); a.IsTrashed {
		t.Error("a photo tagged after the end must stay where it is")
	}
	code, body = postBody(t, port, "/join", grandmaC, url.Values{"service": {"weekend"}, "mode": {"copy"}, "pool": {"1"}})
	if code != 409 || !strings.Contains(body, "ended") {
		t.Errorf("joining an ended pool: %d %s", code, body)
	}
	_ = grandma
}
