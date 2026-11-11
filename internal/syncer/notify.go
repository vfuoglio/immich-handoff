package syncer

import (
	"fmt"
	"strings"

	"github.com/vfuoglio/immich-handoff/internal/notify"
)

// notify tells whoever runs the app, through the one notifier (NOTIFY_URL and NOTIFY_FILE); with none set it does
// nothing. It never blocks.
func (s *Syncer) notify(kind notify.Kind, text string) {
	if s.Notifier != nil {
		s.Notifier(notify.Message{Kind: kind, Text: text})
	}
}

// passSummary says in words how a pass that went wrong ended.
func passSummary(info *RunInfo) string {
	var parts []string
	c := info.Counts
	if c.Errors > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", c.Errors))
	}
	if c.Refused > 0 {
		parts = append(parts, fmt.Sprintf("%d refused", c.Refused))
	}
	if c.Imported > 0 {
		parts = append(parts, fmt.Sprintf("%d handed over", c.Imported))
	}
	text := "A hand-over pass ended with problems"
	if len(parts) > 0 {
		text += ": " + strings.Join(parts, ", ")
	}
	if info.Error != "" {
		text += ". " + info.Error
	}
	return text
}
