package notify

import (
	"fmt"
	"testing"
	"time"
)

type told struct{ msgs []Message }

func (c *told) send(m Message) { c.msgs = append(c.msgs, m) }

func newClock() (func() time.Time, func(time.Duration)) {
	now := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	return func() time.Time { return now }, func(d time.Duration) { now = now.Add(d) }
}

func TestAProblemIsToldOnceRemindedAfterADayAndToldWhenItIsOver(t *testing.T) {
	var c told
	now, advance := newClock()
	tr := &Tracker{Send: c.send, Now: now}
	m := Message{Kind: Problem, Text: "pass for holiday failed"}

	tr.Report("pass/holiday", m)
	for i := 0; i < 70; i++ { // every pass for a day and a bit
		advance(25 * time.Minute)
		if i < 60 {
			tr.Report("pass/holiday", m)
		}
	}
	if len(c.msgs) != 2 {
		t.Fatalf("one message and one reminder, got %d: %+v", len(c.msgs), c.msgs)
	}
	if c.msgs[0].Text != "pass for holiday failed" {
		t.Errorf("%q", c.msgs[0].Text)
	}
	if got := c.msgs[1].Text; got != "Still not fixed after 24 hours: pass for holiday failed" && got != "Still not fixed after 25 hours: pass for holiday failed" {
		t.Errorf("%q", got)
	}
	if c.msgs[1].Kind != Problem {
		t.Error("the reminder keeps its kind, so a destination that takes problems still gets it")
	}

	tr.Clear("pass/holiday", Message{Kind: Problem, Text: "works again"})
	tr.Clear("pass/holiday", Message{Kind: Problem, Text: "works again"}) // already over: nothing more
	if len(c.msgs) != 3 || c.msgs[2].Text != "works again" {
		t.Errorf("%+v", c.msgs)
	}

	tr.Report("pass/holiday", m) // it came back: a new problem, told at once
	if len(c.msgs) != 4 || c.msgs[3].Text != "pass for holiday failed" {
		t.Errorf("%+v", c.msgs)
	}
}

func TestTheReminderRepeatsOnlyAfterAnotherDay(t *testing.T) {
	var c told
	now, advance := newClock()
	tr := &Tracker{Send: c.send, Now: now, Remind: 24 * time.Hour}
	for i := 0; i < 72*4; i++ { // three days, a pass every 15 minutes
		tr.Report("backup", Message{Kind: Backup, Text: "backup failed"})
		advance(15 * time.Minute)
	}
	if len(c.msgs) != 3 {
		t.Fatalf("day 0, day 1, day 2: %d messages", len(c.msgs))
	}
	if c.msgs[2].Text != "Still not fixed after 2 days: backup failed" && c.msgs[2].Text != "Still not fixed after 48 hours: backup failed" {
		t.Errorf("%q", c.msgs[2].Text)
	}
}

func TestProblemsAreIndependentAndClearingOneUnreportedSaysNothing(t *testing.T) {
	var c told
	now, _ := newClock()
	tr := &Tracker{Send: c.send, Now: now}
	tr.Report("a", Message{Text: "a"})
	tr.Report("b", Message{Text: "b"})
	tr.Report("a", Message{Text: "a"})
	tr.Clear("never", Message{Text: "x"})
	tr.Clear("a", Message{Text: "a is fine"})
	tr.Report("b", Message{Text: "b"}) // b is still the same problem
	if len(c.msgs) != 3 || c.msgs[2].Text != "a is fine" {
		t.Errorf("%+v", c.msgs)
	}
}

func TestTheTrackerIsBoundedAndNeverLosesAMessage(t *testing.T) {
	var c told
	now, _ := newClock()
	tr := &Tracker{Send: c.send, Now: now}
	for i := 0; i < maxTracked+50; i++ {
		tr.Report(fmt.Sprint("k", i), Message{Text: "x"})
	}
	if len(c.msgs) != maxTracked+50 || len(tr.seen) != maxTracked {
		t.Errorf("%d messages, %d remembered", len(c.msgs), len(tr.seen))
	}
}

func TestAgoIsReadableAtAGlance(t *testing.T) {
	for d, want := range map[time.Duration]string{20 * time.Minute: "an hour", time.Hour: "1 hour", 25 * time.Hour: "25 hours", 47 * time.Hour: "47 hours", 49 * time.Hour: "2 days", 100 * time.Hour: "4 days"} {
		if got := ago(d); got != want {
			t.Errorf("%v: %q, want %q", d, got, want)
		}
	}
	var nilSend Tracker
	nilSend.Report("x", Message{}) // no sender: no crash
	nilSend.Clear("x", Message{})
}
