package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// healthURL is the address of the app's own /healthz as seen from inside its container, derived from the
// LISTEN_ADDR the app listens on (":8080", "0.0.0.0:8080", "[::]:8080" and "host:8080" all work).
func healthURL(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "http://127.0.0.1:8080/healthz"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/healthz"
}

// checkHealth asks a running app whether it is healthy: it must answer 200 with the body "ok".
func checkHealth(ctx context.Context, url string) error {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 64))
	if res.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != "ok" {
		return fmt.Errorf("unhealthy: %s %q", res.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

// runHealthcheck is `immich-handoff --healthcheck`, used by the container's HEALTHCHECK (the image has no
// shell or curl). It prints what is wrong and returns the exit status.
func runHealthcheck(listen string) int {
	if err := checkHealth(context.Background(), healthURL(listen)); err != nil {
		fmt.Println("healthcheck failed:", err)
		return 1
	}
	return 0
}
