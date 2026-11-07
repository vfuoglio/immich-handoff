package web

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"

	"github.com/vfuoglio/immich-handoff/internal/backup"
)

// The backup endpoints are for `immich-handoff backup`, which runs next to the app and shares its webhook secret.
// They are not for browsers, and need that secret like /metrics does.
func (w *Server) backupAllowed(rw http.ResponseWriter, r *http.Request) bool {
	if subtle.ConstantTimeCompare([]byte(r.Header.Get(w.WebhookHeader)), []byte(w.WebhookSecret)) != 1 {
		http.Error(rw, "bad secret", http.StatusUnauthorized)
		return false
	}
	if w.Backups == nil {
		http.Error(rw, "nothing to back up: people cannot join from the page here, so no data is stored", http.StatusNotFound)
		return false
	}
	return true
}

type backupReply struct {
	backup.Job
	Already bool `json:"already"`
}

func (w *Server) queueBackup(rw http.ResponseWriter, r *http.Request) {
	if !w.backupAllowed(rw, r) {
		return
	}
	job, already := w.Backups.Queue(w.BackgroundCtx)
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(http.StatusAccepted)
	json.NewEncoder(rw).Encode(backupReply{job, already})
}

func (w *Server) backupStatus(rw http.ResponseWriter, r *http.Request) {
	if !w.backupAllowed(rw, r) {
		return
	}
	job, ok := w.Backups.Latest()
	if id := r.PathValue("id"); id != "" {
		job, ok = w.Backups.Get(id)
	}
	if !ok {
		http.Error(rw, "no such backup", http.StatusNotFound)
		return
	}
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(backupReply{Job: job})
}
