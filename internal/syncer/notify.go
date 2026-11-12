package syncer

import (
	"fmt"

	"github.com/vfuoglio/immich-handoff/internal/notify"
)

// notify tells whoever runs the app, through the one notifier (NOTIFY_URL and NOTIFY_FILE); with none set it does
// nothing. It never blocks.
func (s *Syncer) notify(kind notify.Kind, text string) {
	if s.Notifier != nil {
		s.Notifier(notify.Message{Kind: kind, Text: text})
	}
}

// problem tells whoever runs the app about something that is wrong and may stay wrong: once, with a reminder after a
// day, and "fixed" when it ends (see notify.Tracker). Without a tracker every report is a message.
func (s *Syncer) problem(key string, m notify.Message) {
	switch {
	case s.Problems != nil:
		s.Problems.Report(key, m)
	default:
		s.notify(m.Kind, m.Text)
	}
}

// fixed says that a problem told with problem is over.
func (s *Syncer) fixed(key string, m notify.Message) {
	if s.Problems != nil {
		s.Problems.Clear(key, m)
	}
}

// reportPass tells what a pass found wrong, service by service. A service whose pass had failures is a problem; the
// next pass without any ends it. A service that did not run (paused, no receiver, broken) says nothing either way, so
// pausing a service is not mistaken for fixing it. Refused photos alone are not a problem: the receiver chose that,
// and the page and the sender's own notifications show it.
func (s *Syncer) reportPass(info *RunInfo, ran []*Service) {
	inServices := 0
	for _, svc := range ran {
		c := info.PerService[svc.Label()]
		inServices += c.Errors
		key := "pass/" + svc.Label()
		switch {
		case c.Errors > 0:
			s.problem(key, notify.Message{Kind: notify.Problem, Text: fmt.Sprintf("A hand-over pass for %s had problems: %s", svc.Label(), failed(c.Errors))})
		case !svc.Paused && svc.Broken == "" && len(svc.Receivers) > 0:
			s.fixed(key, notify.Message{Kind: notify.Problem, Text: fmt.Sprintf("Hand-overs for %s work again.", svc.Label())})
		}
	}
	// what went wrong outside any one service (the tidy-up of tags, the pass itself)
	if elsewhere := info.Counts.Errors - inServices; info.Error != "" || elsewhere > 0 {
		text := "A hand-over pass had problems outside any one service"
		if elsewhere > 0 {
			text += ": " + failed(elsewhere)
		}
		if info.Error != "" {
			text += ". " + info.Error
		}
		s.problem("pass/housekeeping", notify.Message{Kind: notify.Problem, Text: text})
	} else {
		s.fixed("pass/housekeeping", notify.Message{Kind: notify.Problem, Text: "The tidy-up after hand-overs works again."})
	}
}

func failed(n int) string {
	if n == 1 {
		return "1 failed."
	}
	return fmt.Sprintf("%d failed.", n)
}
