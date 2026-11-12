package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSendTestSendsOneMessageToEachDestinationAndSaysHowEachAnswered(t *testing.T) {
	var got atomic.Int32
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got.Add(1) }))
	defer good.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	defer bad.Close()
	t.Setenv("NOTIFY_URL", good.URL+"/secret-topic")
	t.Setenv("NOTIFY_FILE", writeNotifyFile(t, bad.URL+"/other-secret"))

	var out bytes.Buffer
	fails := sendTestNotifications(context.Background(), &out)
	if fails != 1 || got.Load() != 1 {
		t.Fatalf("fails %d, delivered %d:\n%s", fails, got.Load(), out.String())
	}
	for _, want := range []string{"NOTIFY_URL (ntfy): delivered", "FAIL  broken (json): answered 401"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "secret") {
		t.Errorf("an address was printed:\n%s", out.String())
	}
}

func TestSendTestWithNothingSetIsFineAndABadFileIsAFailure(t *testing.T) {
	t.Setenv("NOTIFY_URL", "")
	t.Setenv("NOTIFY_FILE", "")
	var out bytes.Buffer
	if fails := sendTestNotifications(context.Background(), &out); fails != 0 || !strings.Contains(out.String(), "no destination is set") {
		t.Errorf("%d: %s", fails, out.String())
	}
	t.Setenv("NOTIFY_FILE", "/nonexistent/notify.yaml")
	out.Reset()
	if fails := sendTestNotifications(context.Background(), &out); fails != 1 || !strings.Contains(out.String(), "FAIL") {
		t.Errorf("%d: %s", fails, out.String())
	}
}
