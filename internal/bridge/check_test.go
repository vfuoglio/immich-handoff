package bridge

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestCheckAcceptsExactlyTheFourPermissions(t *testing.T) {
	f := newFake(t)
	info, err := Check(context.Background(), f.client(), f.URL, "good-key")
	if err != nil || info.Version != "v3.2.0" || info.Major != 3 {
		t.Fatalf("info %+v err %v", info, err)
	}
	// only the two questions that need no permission, never the list of users
	if got := strings.Join(f.asked(), ","); got != "GET /api/server/version,GET /api/api-keys/me" {
		t.Fatalf("asked %s", got)
	}
}

func TestCheckRefusesAKeyThatCanDoMore(t *testing.T) {
	for _, extra := range []string{"asset.read", "all", "asset.delete", "tag.read"} {
		f := newFake(t)
		f.Permissions = append(f.Permissions, extra)
		_, err := Check(context.Background(), f.client(), f.URL, "good-key")
		if !errors.Is(err, ErrKeyTooBroad) || !strings.Contains(err.Error(), extra) || !strings.Contains(err.Error(), "make a smaller one") {
			t.Errorf("with %s: %v", extra, err)
		}
	}
}

func TestCheckRefusesAKeyThatCanDoLess(t *testing.T) {
	f := newFake(t)
	f.Permissions = []string{"user.read", "asset.upload"}
	_, err := Check(context.Background(), f.client(), f.URL, "good-key")
	if !errors.Is(err, ErrKeyTooSmall) || !strings.Contains(err.Error(), "tag.create") || !strings.Contains(err.Error(), "tag.asset") {
		t.Fatalf("%v", err)
	}
	// more and less at once: the broad one is the more important thing to say
	f.Permissions = []string{"asset.read"}
	if _, err := Check(context.Background(), f.client(), f.URL, "good-key"); !errors.Is(err, ErrKeyTooBroad) {
		t.Fatalf("%v", err)
	}
}

func TestCheckSaysWhatIsWrongWithTheServer(t *testing.T) {
	ctx := context.Background()
	f := newFake(t)

	if _, err := Check(ctx, f.client(), f.URL, "a-revoked-key"); !errors.Is(err, ErrRevoked) {
		t.Errorf("a key the server rejects: %v", err)
	}

	f.Version = [3]int{2, 7, 1}
	info, err := Check(ctx, f.client(), f.URL, "good-key")
	if !errors.Is(err, ErrVersion) || info.Version != "v2.7.1" || !strings.Contains(err.Error(), "v2.7.1") || !strings.Contains(err.Error(), "3") {
		t.Errorf("an untested major version: %+v %v (both versions must be in the message)", info, err)
	}
	f.Version = [3]int{3, 9, 4}
	if info, err := Check(ctx, f.client(), f.URL, "good-key"); err != nil || info.Version != "v3.9.4" {
		t.Errorf("a newer minor version of a tested major is fine: %+v %v", info, err)
	}

	f.Down = 503
	if _, err := Check(ctx, f.client(), f.URL, "good-key"); !errors.Is(err, ErrUnreachable) {
		t.Errorf("a server error: %v", err)
	}
	f.Down = 404
	if _, err := Check(ctx, f.client(), f.URL, "good-key"); !errors.Is(err, ErrNotImmich) {
		t.Errorf("not Immich: %v", err)
	}

	f.Down, f.Redirect = 0, "https://photos.example.org"
	if _, err := Check(ctx, f.client(), f.URL, "good-key"); !errors.Is(err, ErrRedirect) || !strings.Contains(err.Error(), "https://photos.example.org") {
		t.Errorf("a redirect must be refused and say where it points: %v", err)
	}
	if got := f.asked(); got[len(got)-1] != "GET /api/server/version" {
		t.Errorf("the redirect was followed or the key was sent anyway: %v", got)
	}

	f.Close()
	if _, err := Check(ctx, f.client(), f.URL, "good-key"); !errors.Is(err, ErrUnreachable) {
		t.Errorf("a server that is gone: %v", err)
	}
}

func TestCheckRefusesAPrivateAddressWithoutTheOptIn(t *testing.T) {
	f := newFake(t)
	_, err := Check(context.Background(), Policy{}.HTTPClient(), f.URL, "good-key")
	if !errors.Is(err, ErrBadAddress) {
		t.Fatalf("%v", err)
	}
	if len(f.asked()) != 0 {
		t.Fatal("the server was contacted")
	}
}

func TestCheckDoesNotLeakTheAddressIntoTheMessage(t *testing.T) {
	f := newFake(t)
	f.Close()
	_, err := Check(context.Background(), f.client(), f.URL, "good-key")
	if err == nil || strings.Contains(err.Error(), "/api/") {
		t.Fatalf("%v", err)
	}
}

func TestPermissionsHelpNamesAllFour(t *testing.T) {
	h := PermissionsHelp()
	for _, p := range RequiredPermissions {
		if !strings.Contains(h, p) {
			t.Errorf("%s missing from %q", p, h)
		}
	}
	if len(RequiredPermissions) != 4 {
		t.Fatal("the design says four")
	}
}
