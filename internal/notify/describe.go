package notify

import (
	"context"
	"sync"
	"time"
)

// Described is a destination as it can be shown: its name, its format and the kinds it takes, never its address.
type Described struct {
	Name, Format string
	Kinds        []Kind // empty: every kind
	People       bool   // the server used to write to the people taking part; it takes no administrator messages
}

// Describe lists the destinations.
func (n *Notifier) Describe() []Described {
	if n == nil {
		return nil
	}
	out := make([]Described, 0, len(n.dests))
	for _, d := range n.dests {
		x := Described{Name: d.Name, Format: d.Format, People: d.Mail != nil && d.Mail.People}
		for _, k := range Kinds { // in the documented order
			if d.Kinds[k] {
				x.Kinds = append(x.Kinds, k)
			}
		}
		out = append(out, x)
	}
	return out
}

// TestResult is how one destination answered a test.
type TestResult struct {
	Name, Format string
	People       bool   // not tested here: each person has a button for their own mail
	Err          string // empty: delivered; otherwise why not, without the address
}

const testText = "This is a test notification from immich-handoff. If you can read it, this destination works."

// Test sends one message to every destination at once, whatever kinds it takes, and says how each answered. It makes
// one attempt each (no retries), so the answer is quick, and mail goes out at once instead of waiting for company.
func (n *Notifier) Test(ctx context.Context) []TestResult {
	if n == nil {
		return nil
	}
	out := make([]TestResult, len(n.dests))
	var wg sync.WaitGroup
	for i, d := range n.dests {
		out[i] = TestResult{Name: d.Name, Format: d.Format, People: d.Mail != nil && d.Mail.People}
		if out[i].People {
			continue
		}
		wg.Add(1)
		go func(i int, d Destination) {
			defer wg.Done()
			done := make(chan error, 1)
			go func() { done <- n.testOne(d) }()
			select {
			case err := <-done:
				if err != nil {
					out[i].Err = err.Error()
				}
			case <-ctx.Done():
				out[i].Err = "no answer in time"
			case <-time.After(60 * time.Second):
				out[i].Err = "no answer in time"
			}
		}(i, d)
	}
	wg.Wait()
	return out
}

func (n *Notifier) testOne(d Destination) error {
	m := Message{Kind: Test, Text: testText}
	if d.Mail != nil {
		body, err := n.buildMail(d, []stamped{{time.Now(), m}})
		if err != nil {
			return err
		}
		_, err = n.smtpSend(d.Mail, body)
		return err
	}
	_, err := n.post(d, m, n.linkFor(m))
	return err
}
