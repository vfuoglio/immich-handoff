package push

import (
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func b64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(strings.ReplaceAll(s, " ", ""))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The example of RFC 8291, section 5: the same keys, salt and secret must give the same bytes.
func TestTheExampleOfRFC8291IsReproducedByteForByte(t *testing.T) {
	eph, err := ecdh.P256().NewPrivateKey(b64(t, "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"))
	if err != nil {
		t.Fatal(err)
	}
	sub := Subscription{
		Endpoint: "https://push.example.net/push/JzLQ3raZJfFBR0aqvOMsLrt54w4rJUsV",
		P256dh:   b64(t, "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"),
		Auth:     b64(t, "BTBZMqHH6r4Tts7J_aSIgg"),
	}
	got, err := encrypt(sub, []byte("When I grow up, I want to be a watermelon"), eph, b64(t, "DGv6ra1nlYgDCS1FRnbzlw"))
	if err != nil {
		t.Fatal(err)
	}
	want := b64(t, "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27ml"+
		"mlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPT"+
		"pK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN")
	if string(got) != string(want) {
		t.Errorf("got  %x\nwant %x", got, want)
	}
	if len(got) != 86+len("When I grow up, I want to be a watermelon")+1+16 { // header, text, delimiter, tag
		t.Errorf("length %d", len(got))
	}
}

func TestEncryptRefusesWhatCannotWorkAndMakesNewKeysEachTime(t *testing.T) {
	ua, _ := ecdh.P256().GenerateKey(rand.Reader)
	sub := Subscription{P256dh: ua.PublicKey().Bytes(), Auth: make([]byte, 16)}
	a, err := Encrypt(sub, []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Encrypt(sub, []byte("x"))
	if string(a) == string(b) {
		t.Error("two pushes of the same text must differ (new salt and key each time)")
	}
	if _, err := Encrypt(sub, make([]byte, MaxPayload+1)); err != ErrTooLong {
		t.Errorf("too long: %v", err)
	}
	if body, err := Encrypt(sub, make([]byte, MaxPayload)); err != nil || len(body) > 4096 {
		t.Errorf("the longest message fits in 4096 bytes: %d %v", len(body), err)
	}
	if _, err := Encrypt(Subscription{P256dh: ua.PublicKey().Bytes(), Auth: []byte("short")}, nil); err == nil {
		t.Error("a short auth secret was accepted")
	}
	if _, err := Encrypt(Subscription{P256dh: []byte("not a point"), Auth: make([]byte, 16)}, nil); err == nil {
		t.Error("a bad key was accepted")
	}
}

func TestTheVAPIDTokenIsSignedForTheServiceAndExpires(t *testing.T) {
	k, err := NewKeys()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	h, err := k.Authorization("https://fcm.googleapis.com", "https://handoff.example", now)
	if err != nil {
		t.Fatal(err)
	}
	rest, ok := strings.CutPrefix(h, "vapid t=")
	if !ok {
		t.Fatal(h)
	}
	jwt, key, ok := strings.Cut(rest, ", k=")
	if !ok || key != k.PublicB64() {
		t.Fatalf("header: %s", h)
	}
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatal(jwt)
	}
	var claims struct {
		Aud, Sub string
		Exp      int64
	}
	if err := json.Unmarshal(b64(t, parts[1]), &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Aud != "https://fcm.googleapis.com" || claims.Sub != "https://handoff.example" || claims.Exp != now.Add(12*time.Hour).Unix() {
		t.Errorf("%+v", claims)
	}
	if string(b64(t, parts[0])) != `{"alg":"ES256","typ":"JWT"}` {
		t.Errorf("header %s", b64(t, parts[0]))
	}
	sig := b64(t, parts[2])
	if len(sig) != 64 {
		t.Fatalf("a signature is two 32-byte numbers, not DER: %d bytes", len(sig))
	}
	pubRaw, _ := k.Public()
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), pubRaw)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(pub, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Error("the signature does not verify with the public key")
	}
}

func TestKeysSurviveBeingStored(t *testing.T) {
	k, _ := NewKeys()
	raw, err := k.Private()
	if err != nil || len(raw) != 32 {
		t.Fatalf("%d %v", len(raw), err)
	}
	k2, err := KeysFromPrivate(raw)
	if err != nil || k2.PublicB64() != k.PublicB64() {
		t.Errorf("%v", err)
	}
	if _, err := KeysFromPrivate([]byte("nope")); err == nil {
		t.Error("garbage accepted")
	}
}

func TestOnlyTheAddressesOfKnownPushServicesAreAllowed(t *testing.T) {
	for _, ok := range []string{
		"https://fcm.googleapis.com/fcm/send/abc",
		"https://updates.push.services.mozilla.com/wpush/v2/abc",
		"https://web.push.apple.com/abc",
		"https://api.push.apple.com/3/device/abc",
		"https://par02p.notify.windows.com/w/?token=abc",
		"https://FCM.GoogleAPIs.com:443/x",
	} {
		if err := AllowedEndpoint(ok); err != nil {
			t.Errorf("%s refused: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"", "not a url", "http://fcm.googleapis.com/x", "https://fcm.googleapis.com.evil.test/x", "https://evilfcm.googleapis.com/x",
		"https://googleapis.com/x", "https://push.services.mozilla.com/x", "https://x.push.services.mozilla.com.evil.test/x",
		"https://127.0.0.1/x", "https://[::1]/x", "https://10.0.0.5/x", "https://localhost/x", "https://fcm.googleapis.com:8443/x",
		"https://user:pw@fcm.googleapis.com/x", "https://fcm.googleapis.com./x", "https://evil.test/https://fcm.googleapis.com/x",
		"https://fcm.googleapis.com/x#frag", "ftp://fcm.googleapis.com/x", "https://.notify.windows.com/x", "https://notify.windows.com/x",
	} {
		if err := AllowedEndpoint(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func testClient(t *testing.T, srv *httptest.Server) (*Client, Subscription) {
	t.Helper()
	keys, _ := NewKeys()
	ua2, _ := ecdh.P256().GenerateKey(rand.Reader)
	auth := make([]byte, 16)
	return &Client{Keys: keys, Subject: "https://handoff.example", HTTP: srv.Client(), Allow: func(string) error { return nil },
			Wait: []time.Duration{time.Millisecond, time.Millisecond}},
		Subscription{Endpoint: srv.URL + "/push/abc", P256dh: ua2.PublicKey().Bytes(), Auth: auth}
}

func TestASendCarriesTheHeadersAnEncryptedBodyAndOurIdentity(t *testing.T) {
	var got http.Header
	var size int
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		b := make([]byte, 5000)
		n, _ := r.Body.Read(b)
		size = n
		w.WriteHeader(201)
	}))
	defer srv.Close()
	c, sub := testClient(t, srv)
	if err := c.Send(context.Background(), sub, []byte(`{"title":"hi"}`), Options{TTL: time.Hour, Urgency: "high"}); err != nil {
		t.Fatal(err)
	}
	if got.Get("Content-Encoding") != "aes128gcm" || got.Get("TTL") != "3600" || got.Get("Urgency") != "high" ||
		!strings.HasPrefix(got.Get("Authorization"), "vapid t=") || !strings.HasSuffix(got.Get("Authorization"), "k="+c.Keys.PublicB64()) {
		t.Errorf("headers %v", got)
	}
	if size < 86+17 {
		t.Errorf("the body is %d bytes", size)
	}
}

func TestEachAnswerOfAPushServiceIsTreatedAsItShouldBe(t *testing.T) {
	var calls int32
	var status atomic.Int32
	var retryAfter atomic.Value
	retryAfter.Store("")
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if ra := retryAfter.Load().(string); ra != "" {
			w.Header().Set("Retry-After", ra)
		}
		w.WriteHeader(int(status.Load()))
	}))
	defer srv.Close()
	c, sub := testClient(t, srv)
	for _, tc := range []struct {
		code  int
		want  error
		calls int32
	}{{201, nil, 1}, {200, nil, 1}, {404, ErrGone, 1}, {410, ErrGone, 1}, {413, ErrTooLarge, 1}, {500, ErrTemporary, 3}, {429, ErrTemporary, 3}} {
		atomic.StoreInt32(&calls, 0)
		status.Store(int32(tc.code))
		if err := c.Send(context.Background(), sub, []byte("x"), Options{}); !errors.Is(err, tc.want) {
			t.Errorf("%d: %v, want %v", tc.code, err, tc.want)
		}
		if atomic.LoadInt32(&calls) != tc.calls {
			t.Errorf("%d: %d requests, want %d", tc.code, calls, tc.calls)
		}
	}
	for _, code := range []int{400, 401, 403} {
		status.Store(int32(code))
		var r *RejectedError
		if err := c.Send(context.Background(), sub, []byte("x"), Options{}); !errors.As(err, &r) || r.Code != code {
			t.Errorf("%d: %v", code, err)
		}
	}
}

func TestAnAddressOutsideTheKnownServicesIsNeverCalled(t *testing.T) {
	var hit atomic.Bool
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit.Store(true) }))
	defer srv.Close()
	c, sub := testClient(t, srv)
	c.Allow = nil // the real rule
	if err := c.Send(context.Background(), sub, []byte("x"), Options{}); !errors.Is(err, ErrHost) {
		t.Errorf("%v", err)
	}
	if hit.Load() {
		t.Error("a request was made to an address that is not a push service")
	}
}

func TestARedirectIsNotFollowed(t *testing.T) {
	var elsewhere atomic.Bool
	target := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { elsewhere.Store(true) }))
	defer target.Close()
	srv := httptest.NewTLSServer(http.RedirectHandler(target.URL, http.StatusTemporaryRedirect))
	defer srv.Close()
	c, sub := testClient(t, srv)
	c.HTTP = &http.Client{Transport: srv.Client().Transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var r *RejectedError
	if err := c.Send(context.Background(), sub, []byte("x"), Options{}); !errors.As(err, &r) {
		t.Errorf("%v", err)
	}
	if elsewhere.Load() {
		t.Error("the redirect was followed")
	}
	// the client made when none is given follows none either
	d := &Client{}
	if d.client().CheckRedirect(nil, nil) != http.ErrUseLastResponse {
		t.Error("the default client follows redirects")
	}
}

func TestTheErrorNeverHoldsTheAddress(t *testing.T) {
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	c, sub := testClient(t, srv)
	srv.Close()
	err := c.Send(context.Background(), sub, []byte("x"), Options{})
	if !errors.Is(err, ErrTemporary) || strings.Contains(err.Error(), "127.0.0.1") {
		t.Errorf("%v", err)
	}
}
