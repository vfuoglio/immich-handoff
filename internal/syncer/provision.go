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

const WorkflowName = "immich-handoff"

// Provision makes sure every source account has the share tag and the workflow that wakes this app.
// It is idempotent, so it is safe to run on every start. Failures are logged, not fatal: the daily
// sweep still imports tagged photos even if a workflow could not be created.
func (s *Syncer) Provision(ctx context.Context, wh Webhook) (failures int) {
	s.provMu.Lock()
	defer s.provMu.Unlock()
	if wh.URL == "" {
		s.Log.Info("PUBLIC_WEBHOOK_URL not set: not creating workflows; only the startup and daily passes will import")
		return 0
	}
	for i, src := range s.Sources {
		log := s.Log.With("source", i+1)
		if s.Cfg.DryRun {
			log.Info("[dry-run] would ensure share tag and workflow")
			continue
		}
		tag, err := src.FindTag(ctx, s.Cfg.ShareTag, true)
		if err != nil || tag == nil {
			log.Error("cannot ensure share tag", "err", err)
			failures++
			continue
		}
		res, err := src.EnsureWorkflow(ctx, immich.WorkflowSpec{
			Name: WorkflowName, TagID: *tag, WebhookURL: wh.URL, HeaderName: wh.Header, HeaderSecret: wh.Secret,
		})
		if err != nil {
			log.Error("cannot ensure workflow (does the API key have workflow.create/read/update?)", "err", err)
			failures++
			continue
		}
		log.Info("workflow ready", "result", res)
	}
	return failures
}

// ProvisionRetry keeps trying in the background until every source is provisioned. Immich reports
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
