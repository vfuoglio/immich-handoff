package syncer

import (
	"strings"
	"testing"
	"time"

	"github.com/vfuoglio/immich-handoff/internal/notify"
)

type pass struct {
	s    *Syncer
	told []string
	now  time.Time
}

func newPass() *pass {
	p := &pass{now: time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)}
	send := func(m notify.Message) { p.told = append(p.told, m.Text) }
	p.s = &Syncer{Notifier: send, Problems: &notify.Tracker{Send: send, Now: func() time.Time { return p.now }}}
	return p
}

func healthy(name string) *Service {
	return &Service{Name: name, Receivers: []*Account{{Name: "r"}}}
}

func result(errs map[string]int, refused int, extra int) *RunInfo {
	info := &RunInfo{PerService: map[string]Counts{}}
	for name, n := range errs {
		info.PerService[name] = Counts{Errors: n, Refused: refused}
		info.Counts.Errors += n
	}
	info.Counts.Errors += extra
	return info
}

func TestAServiceThatKeepsFailingIsToldOnceAndWhenItWorksAgain(t *testing.T) {
	p := newPass()
	holiday := healthy("holiday")
	for i := 0; i < 5; i++ { // five passes in a row
		p.s.reportPass(result(map[string]int{"holiday": 2}, 0, 0), []*Service{holiday})
	}
	if len(p.told) != 1 || p.told[0] != "A hand-over pass for holiday had problems: 2 failed." {
		t.Fatalf("%q", p.told)
	}
	p.s.reportPass(result(map[string]int{"holiday": 0}, 0, 0), []*Service{holiday})
	if len(p.told) != 2 || p.told[1] != "Hand-overs for holiday work again." {
		t.Fatalf("%q", p.told)
	}
	p.s.reportPass(result(map[string]int{"holiday": 0}, 0, 0), []*Service{holiday}) // and a clean pass after that says nothing
	if len(p.told) != 2 {
		t.Errorf("%q", p.told)
	}
}

func TestRefusedPhotosAloneAreNotAProblemForTheAdministrator(t *testing.T) {
	p := newPass()
	p.s.reportPass(result(map[string]int{"holiday": 0}, 7, 0), []*Service{healthy("holiday")})
	if len(p.told) != 0 {
		t.Errorf("%q", p.told)
	}
}

func TestOneFailingServiceDoesNotHideAnotherAndPausingIsNotFixing(t *testing.T) {
	p := newPass()
	a, b := healthy("a"), healthy("b")
	p.s.reportPass(result(map[string]int{"a": 1, "b": 0}, 0, 0), []*Service{a, b})
	p.s.reportPass(result(map[string]int{"a": 1, "b": 3}, 0, 0), []*Service{a, b})
	if len(p.told) != 2 || !strings.Contains(p.told[0], "for a") || !strings.Contains(p.told[1], "for b") || !strings.Contains(p.told[1], "3 failed") {
		t.Fatalf("%q", p.told)
	}
	a.Paused = true // a paused service runs nothing, so its count is zero: that is not a repair
	p.s.reportPass(result(map[string]int{"a": 0, "b": 3}, 0, 0), []*Service{a, b})
	b.Broken = "key deleted"
	p.s.reportPass(result(map[string]int{"a": 0, "b": 0}, 0, 0), []*Service{a, b})
	if len(p.told) != 2 {
		t.Errorf("a paused or broken service must not be announced as fixed: %q", p.told)
	}
	a.Paused = false
	p.s.reportPass(result(map[string]int{"a": 0, "b": 0}, 0, 0), []*Service{a, b})
	if len(p.told) != 3 || !strings.Contains(p.told[2], "work again") || !strings.Contains(p.told[2], "for a") {
		t.Errorf("%q", p.told)
	}
}

func TestWhatGoesWrongOutsideAnyServiceIsOneProblemOfItsOwn(t *testing.T) {
	p := newPass()
	for i := 0; i < 3; i++ {
		p.s.reportPass(result(map[string]int{"holiday": 0}, 0, 2), []*Service{healthy("holiday")})
	}
	if len(p.told) != 1 || !strings.Contains(p.told[0], "outside any one service") || !strings.Contains(p.told[0], "2 failed") {
		t.Fatalf("%q", p.told)
	}
	p.s.reportPass(result(map[string]int{"holiday": 0}, 0, 0), []*Service{healthy("holiday")})
	if len(p.told) != 2 || !strings.Contains(p.told[1], "works again") {
		t.Errorf("%q", p.told)
	}
}

func TestWithoutATrackerEveryReportIsAMessageAsBefore(t *testing.T) {
	var told []string
	s := &Syncer{Notifier: func(m notify.Message) { told = append(told, m.Text) }}
	for i := 0; i < 3; i++ {
		s.reportPass(result(map[string]int{"holiday": 1}, 0, 0), []*Service{healthy("holiday")})
	}
	if len(told) != 3 {
		t.Errorf("%q", told)
	}
}

func TestAReminderComesAfterADayOfTheSameProblem(t *testing.T) {
	p := newPass()
	holiday := healthy("holiday")
	p.s.reportPass(result(map[string]int{"holiday": 1}, 0, 0), []*Service{holiday})
	p.now = p.now.Add(25 * time.Hour)
	p.s.reportPass(result(map[string]int{"holiday": 1}, 0, 0), []*Service{holiday})
	if len(p.told) != 2 || !strings.HasPrefix(p.told[1], "Still not fixed after 25 hours: ") {
		t.Errorf("%q", p.told)
	}
}
