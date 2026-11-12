package hub_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/mailin"
	"github.com/vfuoglio/immich-handoff/internal/mailin/mailtest"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// photoBytes is something that starts like a JPEG and is large enough to count as a photo in a mail; each seed is another file.
func photoBytes(seed int) []byte {
	b := []byte{0xff, 0xd8, 0xff, 0xe0, 0, 0x10, 'J', 'F', 'I', 'F', 0}
	for len(b) < 12<<10 {
		b = append(b, byte(len(b)*seed))
	}
	return b
}

type attach struct {
	name string
	data []byte
}

func mailTo(to, from string, files ...attach) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "From: Sender <%s>\r\nTo: %s\r\nSubject: photos\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"B\"\r\n\r\n", from, to)
	b.WriteString("--B\r\nContent-Type: text/plain\r\n\r\nHere they are\r\n")
	for _, f := range files {
		enc := base64.StdEncoding.EncodeToString(f.data)
		var lines []string
		for len(enc) > 76 {
			lines, enc = append(lines, enc[:76]), enc[76:]
		}
		lines = append(lines, enc)
		fmt.Fprintf(&b, "--B\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=%q\r\nContent-Transfer-Encoding: base64\r\n\r\n%s\r\n", f.name, strings.Join(lines, "\r\n"))
	}
	b.WriteString("--B--\r\n")
	return []byte(b.String())
}

// withMailbox sets up a fake mailbox and a hub that reads it, and returns the address a box with a code has.
func withMailbox(t *testing.T, e *env, after string) (*hub.MailIn, *mailtest.Server) {
	t.Helper()
	srv := mailtest.Start(t, mailtest.Options{User: "drop", Password: "mailbox-secret"})
	host, port := srv.Addr()
	m := &hub.MailIn{Hub: e.h, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Settings: mailin.Settings{Host: host, Port: port, TLS: mailin.TLSNone, User: "drop", Password: "mailbox-secret", Address: "drop@example.org", Folder: "INBOX", After: after, Every: time.Minute, MaxBytes: 1 << 20}}
	e.h.MailIn = m
	return m, srv
}

func boxWithMail(t *testing.T, e *env, p hub.DropParams) (store.DropBox, string) {
	t.Helper()
	b := newBox(t, e, p)
	if err := e.h.SetDropMail(context.Background(), bob, b.ID, true); err != nil {
		t.Fatal(err)
	}
	got, _ := e.st.DropBox(context.Background(), b.ID)
	return got, e.h.MailAddress(got.MailCode)
}

func TestMailedPhotosArriveLikeUploadsAndTheMailIsRemovedAndNoAddressIsKept(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	m, srv := withMailbox(t, e, mailin.AfterDelete)
	box, addr := boxWithMail(t, e, hub.DropParams{Name: "Gala"})
	if !strings.HasPrefix(addr, "drop+") || !strings.HasSuffix(addr, "@example.org") || len(box.MailCode) != 10 {
		t.Fatalf("%q %q", addr, box.MailCode)
	}
	srv.Deliver(mailTo(addr, "grandma.private@example.net", attach{"IMG_1.JPG", photoBytes(3)}, attach{"IMG_2.JPG", photoBytes(5)}))

	n, err := m.Poll(ctx)
	if err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	if len(e.f.uploads) != 2 {
		t.Fatalf("both photos reach the owner's library: %v", e.f.uploads)
	}
	got, _ := e.st.DropBox(ctx, box.ID)
	if got.Files != 2 {
		t.Errorf("the box counts them: %d", got.Files)
	}
	if left := srv.Messages(); len(left) != 0 {
		t.Errorf("the mail must be removed from the mailbox once handled: %d left", len(left))
	}
	events, _ := e.st.DropEvents(ctx, box.ID, 50)
	brought := false
	for _, ev := range events {
		if strings.Contains(en(ev.Text), "A mail brought 2 files") {
			brought = true
			if len(ev.Who) != 6 {
				t.Errorf("the sender is a six-character mark: %q", ev.Who)
			}
		}
		if strings.Contains(ev.Text+ev.Who, "grandma") || strings.Contains(ev.Text, "example.net") {
			t.Errorf("the sender's address is in the box's log: %+v", ev)
		}
	}
	if !brought {
		t.Errorf("the log should say a mail brought two files: %+v", events)
	}
	// the address is in no row of the store: a copy of it, taken whole, does not have it
	copyPath := filepath.Join(t.TempDir(), "copy.db")
	if err := e.st.Backup(ctx, copyPath); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(copyPath); bytes.Contains(b, []byte("grandma.private")) || bytes.Contains(b, []byte("example.net")) {
		t.Error("the sender's address is in the data file")
	}
	if n, _ := m.Poll(ctx); n != 0 || len(e.f.uploads) != 2 {
		t.Error("a second look at an empty mailbox did something")
	}
}

func TestMailThatIsNotForABoxOfOursIsDroppedWithoutAWord(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	m, srv := withMailbox(t, e, mailin.AfterDelete)
	box, addr := boxWithMail(t, e, hub.DropParams{Name: "Gala"})

	srv.Deliver(mailTo("drop@example.org", "a@example.net", attach{"x.jpg", photoBytes(3)}))                   // no code
	srv.Deliver(mailTo("drop+zzzzzzzzzz@example.org", "a@example.net", attach{"x.jpg", photoBytes(3)}))        // a code nobody has
	srv.Deliver(mailTo("other+"+box.MailCode+"@example.org", "a@example.net", attach{"x.jpg", photoBytes(3)})) // another mailbox
	srv.Deliver(mailTo(addr, "a@example.net"))                                                                 // nothing attached
	srv.Deliver([]byte("this is not a mail"))
	n, err := m.Poll(ctx)
	if err != nil || n != 5 {
		t.Fatalf("every one of them is handled: %d %v", n, err)
	}
	if len(e.f.uploads) != 0 || len(srv.Messages()) != 0 {
		t.Errorf("nothing is stored and nothing is left: %d uploads, %d messages", len(e.f.uploads), len(srv.Messages()))
	}
	// a box that is paused, and one that has been revoked, take nothing
	e.h.SetDropBoxPaused(ctx, bob, box.ID, true)
	srv.Deliver(mailTo(addr, "a@example.net", attach{"x.jpg", photoBytes(3)}))
	m.Poll(ctx)
	e.h.SetDropBoxPaused(ctx, bob, box.ID, false)
	if len(e.f.uploads) != 0 {
		t.Error("a paused box took a mailed photo")
	}
	e.h.RevokeDropBox(ctx, e.sess, bob, box.ID)
	srv.Deliver(mailTo(addr, "a@example.net", attach{"x.jpg", photoBytes(3)}))
	m.Poll(ctx)
	if len(e.f.uploads) != 0 {
		t.Error("a revoked box took a mailed photo")
	}
}

func TestMailedPhotosFollowTheBoxsRules(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	m, srv := withMailbox(t, e, mailin.AfterDelete)
	box, addr := boxWithMail(t, e, hub.DropParams{Name: "Small", MaxFiles: 2})
	pdf := append([]byte("%PDF-1.4\n"), bytes.Repeat([]byte("x"), 12<<10)...)
	srv.Deliver(mailTo(addr, "a@example.net", attach{"1.jpg", photoBytes(3)}, attach{"doc.pdf", pdf}, attach{"2.jpg", photoBytes(5)}, attach{"3.jpg", photoBytes(7)}))
	if n, err := m.Poll(ctx); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	if got, _ := e.st.DropBox(ctx, box.ID); got.Files != 2 || len(e.f.uploads) != 2 {
		t.Errorf("the box takes two files and no pdf: %d, %v", got.Files, e.f.uploads)
	}
	if len(srv.Messages()) != 0 {
		t.Error("a mail whose files the box turned away is still handled")
	}
	var why []string
	events, _ := e.st.DropEvents(ctx, box.ID, 50)
	for _, ev := range events {
		why = append(why, en(ev.Text))
	}
	log := strings.Join(why, "\n")
	if !strings.Contains(log, "doc.pdf") && !strings.Contains(strings.ToLower(log), "not accepted") {
		t.Logf("the log: %s", log)
	}

	// a box that waits for approval keeps the files for a host to decide
	e.h.StageDir = t.TempDir()
	appr, aaddr := boxWithMail(t, e, hub.DropParams{Name: "Approve", Approve: true})
	before := len(e.f.uploads)
	srv.Deliver(mailTo(aaddr, "a@example.net", attach{"p1.jpg", photoBytes(9)}, attach{"p2.jpg", photoBytes(11)}))
	m.Poll(ctx)
	if len(e.f.uploads) != before {
		t.Errorf("files that wait for approval must not reach the library yet: %v", e.f.uploads[before:])
	}
	if c := e.h.DropPendingCount(ctx, appr.ID); c != 2 {
		t.Errorf("%d files wait for a host", c)
	}
}

func TestAMessageIsKeptAndMarkedReadWhenThatIsWhatWasAsked(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	m, srv := withMailbox(t, e, mailin.AfterSeen)
	_, addr := boxWithMail(t, e, hub.DropParams{Name: "Gala"})
	srv.Deliver(mailTo(addr, "a@example.net", attach{"1.jpg", photoBytes(3)}))
	m.Poll(ctx)
	msgs := srv.Messages()
	if len(msgs) != 1 || !msgs[0].Seen || msgs[0].Deleted {
		t.Fatalf("%+v", msgs)
	}
	m.Poll(ctx) // and it is not handled again
	if len(e.f.uploads) != 1 {
		t.Errorf("%d uploads", len(e.f.uploads))
	}
}

func TestAMessageThatCannotBeStoredYetIsTriedAgainAndThenLetGo(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	m, srv := withMailbox(t, e, mailin.AfterDelete)
	_, addr := boxWithMail(t, e, hub.DropParams{Name: "Gala"})
	realConnect := e.h.Connect
	e.h.Connect = func(key string) (*immich.API, error) { return nil, errors.New("Immich is down") }
	srv.Deliver(mailTo(addr, "a@example.net", attach{"1.jpg", photoBytes(3)}))
	for i := 1; i <= 2; i++ {
		if n, _ := m.Poll(ctx); n != 0 || len(srv.Messages()) != 1 {
			t.Fatalf("look %d: the mail must wait, handled=%d, left=%d", i, n, len(srv.Messages()))
		}
	}
	e.h.Connect = realConnect // it comes back before the third try
	if n, _ := m.Poll(ctx); n != 1 || len(e.f.uploads) != 1 || len(srv.Messages()) != 0 {
		t.Errorf("handled=%d uploads=%d left=%d", n, len(e.f.uploads), len(srv.Messages()))
	}

	e.h.Connect = func(key string) (*immich.API, error) { return nil, errors.New("Immich is down for good") }
	srv.Deliver(mailTo(addr, "a@example.net", attach{"2.jpg", photoBytes(5)}))
	for i := 1; i <= 3; i++ {
		m.Poll(ctx)
	}
	if len(srv.Messages()) != 0 {
		t.Error("after three tries a mail that cannot be stored is let go, or it would be tried for ever")
	}
}

func TestAMailBiggerThanTheLimitIsNeverRead(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	m, srv := withMailbox(t, e, mailin.AfterDelete)
	m.Settings.MaxBytes = 5 << 10
	_, addr := boxWithMail(t, e, hub.DropParams{Name: "Gala"})
	srv.Deliver(mailTo(addr, "a@example.net", attach{"1.jpg", photoBytes(3)}))
	if n, _ := m.Poll(ctx); n != 1 || len(e.f.uploads) != 0 || len(srv.Messages()) != 0 {
		t.Errorf("handled=%d uploads=%d left=%d", n, len(e.f.uploads), len(srv.Messages()))
	}
	for _, c := range srv.Commands() {
		if strings.Contains(c, "BODY") {
			t.Error("the body of a mail over the limit was fetched")
		}
	}
}

func TestOnlyWhoManagesABoxGivesItAnAddressAndOnlyWhenAMailboxIsSetUpAndThereIsNoPassword(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	b := newBox(t, e, hub.DropParams{Name: "Gala"})
	if err := e.h.SetDropMail(ctx, bob, b.ID, true); !errors.Is(err, hub.ErrMailInOff) {
		t.Errorf("no mailbox set up: %v", err)
	}
	if e.h.MailAddress("abc") != "" {
		t.Error("an address without a mailbox")
	}
	m, srv := withMailbox(t, e, mailin.AfterDelete)
	_ = srv
	if err := e.h.SetDropMail(ctx, ann, b.ID, true); err == nil {
		t.Error("someone with no part in the box gave it an address")
	}
	e.h.AddDropHost(ctx, e.sess, bob, b.ID, "ann@x.test", store.HostView)
	if err := e.h.SetDropMail(ctx, ann, b.ID, true); !errors.Is(err, hub.ErrDropRole) {
		t.Errorf("a co-host who only looks: %v", err)
	}
	locked := newBox(t, e, hub.DropParams{Name: "Locked", Password: "secret"})
	if err := e.h.SetDropMail(ctx, bob, locked.ID, true); !errors.Is(err, hub.ErrDropMailLocked) {
		t.Errorf("a box with a password: %v", err)
	}
	if err := e.h.SetDropMail(ctx, bob, b.ID, true); err != nil {
		t.Fatal(err)
	}
	first, _ := e.st.DropBox(ctx, b.ID)
	if err := e.h.SetDropMail(ctx, bob, b.ID, true); err != nil {
		t.Fatal(err)
	}
	if again, _ := e.st.DropBox(ctx, b.ID); again.MailCode != first.MailCode {
		t.Error("asking again changed the address")
	}
	other := newBox(t, e, hub.DropParams{Name: "Other"})
	e.h.SetDropMail(ctx, bob, other.ID, true)
	if o, _ := e.st.DropBox(ctx, other.ID); o.MailCode == first.MailCode || o.MailCode == "" {
		t.Error("two boxes share an address")
	}
	if err := e.h.SetDropMail(ctx, bob, b.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.DropBoxByMailCode(ctx, first.MailCode); err == nil {
		t.Error("a removed address still finds the box")
	}
	_ = m
}

func TestAWrongMailboxPasswordIsAnErrorThatDoesNotSayIt(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	m, _ := withMailbox(t, e, mailin.AfterDelete)
	m.Settings.Password = "not-the-password-xyz"
	_, err := m.Poll(ctx)
	if err == nil || strings.Contains(err.Error(), "not-the-password-xyz") {
		t.Errorf("%v", err)
	}
}
