package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthURL(t *testing.T) {
	for in, want := range map[string]string{
		":8080":         "http://127.0.0.1:8080/healthz",
		"0.0.0.0:9000":  "http://127.0.0.1:9000/healthz",
		"[::]:9000":     "http://127.0.0.1:9000/healthz",
		"10.1.2.3:8081": "http://10.1.2.3:8081/healthz",
		"garbage":       "http://127.0.0.1:8080/healthz",
	} {
		if got := healthURL(in); got != want {
			t.Errorf("healthURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCheckHealth(t *testing.T) {
	serve := func(code int, body string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
			w.Write([]byte(body))
		}))
	}
	ok, down, odd := serve(200, "ok"), serve(503, "no"), serve(200, "<html>a login page</html>")
	defer ok.Close()
	defer down.Close()
	defer odd.Close()
	if err := checkHealth(context.Background(), ok.URL); err != nil {
		t.Errorf("a healthy app must pass: %v", err)
	}
	if err := checkHealth(context.Background(), down.URL); err == nil {
		t.Error("a 503 must fail")
	}
	if err := checkHealth(context.Background(), odd.URL); err == nil {
		t.Error("something else answering on the port must fail")
	}
	closed := serve(200, "ok")
	url := closed.URL
	closed.Close()
	if err := checkHealth(context.Background(), url); err == nil {
		t.Error("nothing listening must fail")
	}
}
