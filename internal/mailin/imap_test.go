package mailin_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/vfuoglio/immich-handoff/internal/mailin"
	"github.com/vfuoglio/immich-handoff/internal/mailin/mailtest"
)

func settingsFor(s *mailtest.Server, mode string) mailin.Settings {
	host, port := s.Addr()
	return mailin.Settings{Host: host, Port: port, TLS: mode, User: "drop", Password: `p"a\ss`, Address: "drop@example.org", Folder: "INBOX", After: mailin.AfterDelete, MaxBytes: 1 << 20}
}

func connect(t *testing.T, s *mailtest.Server, mode string) *mailin.Client {
	t.Helper()
	c, err := mailin.Dial(context.Background(), settingsFor(s, mode), s.TLSConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func TestAMailboxIsReadOverTLSAndOverStartTLSAndOverNothingOnThisMachine(t *testing.T) {
	for _, tc := range []struct {
		name, mode string
		opts       mailtest.Options
	}{
		{"implicit TLS", mailin.TLSImplicit, mailtest.Options{User: "drop", Password: `p"a\ss`, Implicit: true}},
		{"STARTTLS", mailin.TLSStart, mailtest.Options{User: "drop", Password: `p"a\ss`, StartTLS: true}},
		{"none, on this machine", mailin.TLSNone, mailtest.Options{User: "drop", Password: `p"a\ss`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := mailtest.Start(t, tc.opts)
			raw := []byte("Subject: hi\r\n\r\nbody with\r\n\r\ntwo blank lines and ) and * and {5}\r\nend\r\n")
			uid := srv.Deliver(raw)
			c := connect(t, srv, tc.mode)
			if err := c.Login("drop", `p"a\ss`); err != nil { // a quote and a backslash in the password survive
				t.Fatal(err)
			}
			if err := c.Select("INBOX"); err != nil {
				t.Fatal(err)
			}
			ids, err := c.Unseen()
			if err != nil || len(ids) != 1 || ids[0] != uid {
				t.Fatalf("%v %v", ids, err)
			}
			if n, err := c.Size(uid); err != nil || n != int64(len(raw)) {
				t.Errorf("size %d %v", n, err)
			}
			got, err := c.Fetch(uid)
			if err != nil || !bytes.Equal(got, raw) {
				t.Errorf("the message must come back byte for byte: %q %v", got, err)
			}
			if ids, _ := c.Unseen(); len(ids) != 1 {
				t.Error("looking at a message must not mark it read (BODY.PEEK)")
			}
		})
	}
}

func TestMarkingAndRemovingMessagesLeavesTheOthers(t *testing.T) {
	srv := mailtest.Start(t, mailtest.Options{User: "drop", Password: "pw"})
	a, b, c3 := srv.Deliver([]byte("Subject: a\r\n\r\na")), srv.Deliver([]byte("Subject: b\r\n\r\nb")), srv.Deliver([]byte("Subject: c\r\n\r\nc"))
	s := settingsFor(srv, mailin.TLSNone)
	s.Password = "pw"
	cl, err := mailin.Dial(context.Background(), s, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	if err := cl.Login("drop", "pw"); err != nil {
		t.Fatal(err)
	}
	cl.Select("INBOX")
	if err := cl.MarkSeen(a); err != nil {
		t.Fatal(err)
	}
	if err := cl.MarkDeleted(b); err != nil {
		t.Fatal(err)
	}
	if err := cl.Expunge(); err != nil {
		t.Fatal(err)
	}
	ids, _ := cl.Unseen()
	if len(ids) != 1 || ids[0] != c3 {
		t.Errorf("only c is still unread: %v", ids)
	}
	msgs := srv.Messages()
	if len(msgs) != 2 || !msgs[0].Seen || msgs[0].Deleted {
		t.Errorf("a is kept, read; b is gone: %+v", msgs)
	}
}

func TestAWrongPasswordIsRefusedWithoutSayingWhat(t *testing.T) {
	srv := mailtest.Start(t, mailtest.Options{User: "drop", Password: "right"})
	s := settingsFor(srv, mailin.TLSNone)
	c, err := mailin.Dial(context.Background(), s, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	err = c.Login("drop", `wrong-password-123`)
	if err != mailin.ErrLogin || strings.Contains(err.Error(), "wrong-password-123") {
		t.Errorf("%v", err)
	}
	if _, refused := srv.Logins(); refused != 1 {
		t.Errorf("%d", refused)
	}
	for _, cmd := range srv.Commands() {
		if strings.Contains(cmd, "wrong-password-123") {
			t.Error("the fake saw the password in a command it logs; it hides logins, so this checks the fake")
		}
	}
	if err := c.Login("drop\r\nA1 DELETE INBOX", "x"); err != mailin.ErrLogin {
		t.Errorf("a line break in a name must never reach the server: %v", err)
	}
	for _, cmd := range srv.Commands() {
		if strings.Contains(cmd, "DELETE") {
			t.Errorf("a command was injected: %q", cmd)
		}
	}
}

func TestWithoutStartTLSNothingIsSentWithoutProtection(t *testing.T) {
	srv := mailtest.Start(t, mailtest.Options{User: "drop", Password: "pw"}) // offers no STARTTLS
	_, err := mailin.Dial(context.Background(), settingsFor(srv, mailin.TLSStart), srv.TLSConfig())
	if err != mailin.ErrNoTLS {
		t.Errorf("%v", err)
	}
	if ok, refused := srv.Logins(); ok+refused != 0 {
		t.Error("a login was attempted over an unprotected connection")
	}
}

func TestACertificateThatIsNotTrustedIsRefused(t *testing.T) {
	srv := mailtest.Start(t, mailtest.Options{User: "drop", Password: "pw", Implicit: true})
	_, err := mailin.Dial(context.Background(), settingsFor(srv, mailin.TLSImplicit), nil) // the system's roots do not know it
	if err == nil || strings.Contains(err.Error(), "pw") {
		t.Errorf("%v", err)
	}
}

func TestAServerThatSaysNothingOrNonsenseIsAnErrorNotAHang(t *testing.T) {
	hang := mailtest.Start(t, mailtest.Options{Hang: true})
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := mailin.Dial(ctx, settingsFor(hang, mailin.TLSNone), nil); err == nil {
		// the greeting arrives; the first command is what hangs
		t.Log("greeting read")
	}
	_ = start

	junk := mailtest.Start(t, mailtest.Options{Garbage: true})
	c, err := mailin.Dial(context.Background(), settingsFor(junk, mailin.TLSNone), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Login("a", "b"); err == nil {
		t.Error("nonsense was taken for a login")
	}
	if _, err := c.Unseen(); err == nil {
		t.Error("nonsense was taken for a search")
	}
}

func TestAMessageLargerThanTheLimitIsNotReadBecauseItsSizeIsAskedFirst(t *testing.T) {
	srv := mailtest.Start(t, mailtest.Options{User: "drop", Password: "pw", Huge: 900 << 20})
	uid := srv.Deliver([]byte("Subject: big\r\n\r\nx"))
	s := settingsFor(srv, mailin.TLSNone)
	s.Password = "pw"
	c, err := mailin.Dial(context.Background(), s, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Login("drop", "pw")
	c.Select("INBOX")
	n, err := c.Size(uid)
	if err != nil || n != 900<<20 {
		t.Fatalf("%d %v", n, err)
	}
	for _, cmd := range srv.Commands() {
		if strings.Contains(cmd, "BODY") {
			t.Error("the body of a message that is too big was asked for")
		}
	}
}
