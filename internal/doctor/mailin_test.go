package doctor

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/vfuoglio/immich-handoff/internal/mailin/mailtest"
)

func mailReport(t *testing.T, env map[string]string) (string, *Report) {
	t.Helper()
	var buf bytes.Buffer
	r := &Report{W: &buf}
	mailIn(context.Background(), r, Input{Env: func(k string) string { return env[k] }})
	return buf.String(), r
}

func mailEnv(srv *mailtest.Server, password string) map[string]string {
	host, port := srv.Addr()
	return map[string]string{"MAIL_IN_HOST": host, "MAIL_IN_PORT": fmt.Sprint(port), "MAIL_IN_TLS": "none", "MAIL_IN_USER": "drop", "MAIL_IN_PASSWORD": password,
		"MAIL_IN_ADDRESS": "drop@example.org", "HANDOFF_STORE_KEY": "a-long-enough-store-key"}
}

func TestNothingIsSaidAboutPhotosByEmailWhenItIsOff(t *testing.T) {
	if out, r := mailReport(t, nil); out != "" || r.Fails+r.Warns != 0 {
		t.Errorf("%q", out)
	}
}

func TestTheMailboxIsLoggedInToAndNothingIsReadOrPrinted(t *testing.T) {
	srv := mailtest.Start(t, mailtest.Options{User: "drop", Password: "right-password-77"})
	srv.Deliver([]byte("Subject: private\r\n\r\nsecret words"))
	out, r := mailReport(t, mailEnv(srv, "right-password-77"))
	if r.Fails != 0 || !strings.Contains(out, "can be read") || !strings.Contains(out, "drop+<code>@example.org") {
		t.Errorf("%d fails:\n%s", r.Fails, out)
	}
	if strings.Contains(out, "right-password-77") || strings.Contains(out, "secret words") {
		t.Errorf("the password or a mail was printed:\n%s", out)
	}
	for _, c := range srv.Commands() {
		if strings.Contains(c, "FETCH") || strings.Contains(c, "STORE") || strings.Contains(c, "EXPUNGE") {
			t.Errorf("the check read or changed mail: %q", c)
		}
	}
	if m := srv.Messages(); len(m) != 1 || m[0].Seen || m[0].Deleted {
		t.Errorf("the check changed the mailbox: %+v", m)
	}
}

func TestAWrongPasswordAnInvalidSettingAndAMissingStoreKeyAreFailures(t *testing.T) {
	srv := mailtest.Start(t, mailtest.Options{User: "drop", Password: "right-password-77"})
	out, r := mailReport(t, mailEnv(srv, "wrong-password-88"))
	if r.Fails != 1 || !strings.Contains(out, "cannot be read") || strings.Contains(out, "wrong-password-88") {
		t.Errorf("%d fails:\n%s", r.Fails, out)
	}
	bad := mailEnv(srv, "x")
	bad["MAIL_IN_ADDRESS"] = "nonsense"
	if out, r := mailReport(t, bad); r.Fails != 1 || !strings.Contains(out, "will not start") {
		t.Errorf("%s", out)
	}
	nostore := mailEnv(srv, "right-password-77")
	delete(nostore, "HANDOFF_STORE_KEY")
	if out, r := mailReport(t, nostore); r.Fails != 1 || !strings.Contains(out, "HANDOFF_STORE_KEY") {
		t.Errorf("%s", out)
	}
}
