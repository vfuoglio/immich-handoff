package syncer

import (
	"context"
	"net/http"
	"strings"
	"time"
)

func (s *Syncer) notify(msg string) {
	if s.Cfg.NotifyURL == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.Cfg.NotifyURL, strings.NewReader(msg))
	if err != nil {
		return
	}
	if resp, err := http.DefaultClient.Do(req); err != nil {
		s.Log.Warn("notification failed", "err", err)
	} else {
		resp.Body.Close()
	}
}
