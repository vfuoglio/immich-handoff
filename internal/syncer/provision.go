package syncer

import (
	"context"
	"time"

	"github.com/vfuoglio/immich-handoff/internal/immich"
)

// Webhook describes how Immich should call this app.
type Webhook struct {
	URL, Header, Secret string
}

// Provision makes sure every sender has each service's tag and the workflow that wakes this app.
// It is idempotent, so it is safe to run on every start. Failures are logged, not fatal: the daily
// sweep still hands over tagged photos even if a workflow could not be created.
func (s *Syncer) Provision(ctx context.Context, wh Webhook) (failures int) {
	s.provMu.Lock()
	defer s.provMu.Unlock()
	if wh.URL == "" {
		s.Log.Info("PUBLIC_WEBHOOK_URL not set: not creating workflows; only the startup and daily passes will hand photos over")
		return 0
	}
	for _, svc := range s.Services {
		for _, snd := range svc.Senders {
			log := s.Log.With("service", svc.Label(), "sender", snd.Name)
			if s.Cfg.DryRun {
				log.Info("[dry-run] would ensure tag and workflow")
				continue
			}
			tag, err := snd.API.FindTag(ctx, svc.Tag, true)
			if err != nil || tag == nil {
				log.Error("cannot ensure tag", "tag", svc.Tag, "err", err)
				failures++
				continue
			}
			res, err := snd.API.EnsureWorkflow(ctx, immich.WorkflowSpec{
				Name: svc.WorkflowName(), TagID: *tag, WebhookURL: wh.URL, HeaderName: wh.Header, HeaderSecret: wh.Secret,
			})
			if err != nil {
				log.Error("cannot ensure workflow (does the API key have workflow.create/read/update?)", "err", err)
				failures++
				continue
			}
			log.Info("workflow ready", "result", res)
		}
	}
	return failures
}

// ProvisionRetry keeps trying in the background until every sender is provisioned. Immich reports
// healthy about half a minute before its workflow plugins finish loading, so the first attempts after
// an Immich (re)start can fail with "Unknown method"; that is expected and retried with backoff.
func (s *Syncer) ProvisionRetry(ctx context.Context, wh Webhook) {
	start, delay := time.Now(), 5*time.Second
	for s.Provision(ctx, wh) > 0 {
		if time.Since(start) > 15*time.Minute {
			s.Log.Error("giving up provisioning workflows; the daily sweep will retry")
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = min(delay*2, time.Minute)
	}
}
