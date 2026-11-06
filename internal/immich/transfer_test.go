package immich

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// A fake pair of Immich servers: the source streams a file in slow chunks, the destination takes it and answers.
func transferServers(t *testing.T, chunks int, gap time.Duration, stallAfter int) (src, dst *httptest.Server, asset AssetResponseDto) {
	t.Helper()
	asset = AssetResponseDto{Id: uuid.New(), OriginalFileName: "big.mov", FileCreatedAt: time.Now(), FileModifiedAt: time.Now()}
	src = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/quicktime")
		for i := 0; i < chunks; i++ {
			if i == stallAfter { // the source goes quiet, without closing the connection
				select {
				case <-r.Context().Done():
				case <-time.After(30 * time.Second):
				}
				return
			}
			io.WriteString(w, "0123456789abcdef")
			w.(http.Flusher).Flush()
			time.Sleep(gap)
		}
	}))
	dst = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		io.WriteString(w, `{"id":"`+uuid.NewString()+`","status":"created"}`)
	}))
	t.Cleanup(src.Close)
	t.Cleanup(dst.Close)
	return
}

func withLimits(t *testing.T, api, stall time.Duration) {
	t.Helper()
	oa, os := apiTimeout, StallTimeout
	apiTimeout, StallTimeout = api, stall
	t.Cleanup(func() { apiTimeout, StallTimeout = oa, os })
}

func TestATransferThatKeepsMovingIsNotCutOffByTheOrdinaryTimeLimit(t *testing.T) {
	withLimits(t, 400*time.Millisecond, 5*time.Second)                        // ordinary calls: 0.4 s; a stall: 5 s
	srcSrv, dstSrv, asset := transferServers(t, 12, 100*time.Millisecond, -1) // the file takes about 1.2 s to arrive
	src, _ := New(srcSrv.URL, Auth{APIKey: "s"}, nil)
	dst, _ := New(dstSrv.URL, Auth{APIKey: "d"}, nil)
	_, sum, err := Transfer(context.Background(), src, dst, asset)
	if err != nil {
		t.Fatalf("a transfer that keeps moving must finish, however long it takes: %v", err)
	}
	want := sha1.Sum([]byte(strings.Repeat("0123456789abcdef", 12)))
	if base64.StdEncoding.EncodeToString(sum.Sum(nil)) != base64.StdEncoding.EncodeToString(want[:]) {
		t.Error("the checksum must cover every byte that moved")
	}
}

func TestOrdinaryCallsStillHaveATimeLimit(t *testing.T) {
	withLimits(t, 300*time.Millisecond, 5*time.Second)
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer slow.Close()
	api, _ := New(slow.URL, Auth{APIKey: "k"}, nil)
	start := time.Now()
	if _, err := api.Me(context.Background()); err == nil {
		t.Fatal("a call that gets no answer must time out")
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("it took %v to give up", time.Since(start))
	}
}

func TestATransferThatStopsMovingIsGivenUp(t *testing.T) {
	withLimits(t, 2*time.Minute, 400*time.Millisecond)
	srcSrv, dstSrv, asset := transferServers(t, 50, 20*time.Millisecond, 3) // three chunks, then silence
	src, _ := New(srcSrv.URL, Auth{APIKey: "s"}, nil)
	dst, _ := New(dstSrv.URL, Auth{APIKey: "d"}, nil)
	start := time.Now()
	_, _, err := Transfer(context.Background(), src, dst, asset)
	if err == nil {
		t.Fatal("a transfer with no data moving must not hang or pretend to succeed")
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("it took %v to give up", time.Since(start))
	}
	if !strings.Contains(err.Error(), "given up") || !strings.Contains(err.Error(), ErrStalled.Error()) {
		t.Errorf("the error must say why: %v", err)
	}
}

func TestAConnectionWithItsOwnClientKeepsItsOwnLimits(t *testing.T) {
	// tests and callers that pass their own client get it for everything, as before
	own := &http.Client{Timeout: 50 * time.Millisecond}
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(time.Second) }))
	defer slow.Close()
	api, _ := New(slow.URL, Auth{APIKey: "k"}, own)
	if _, err := api.Me(context.Background()); err == nil {
		t.Error("the caller's own client must be used")
	}
}
