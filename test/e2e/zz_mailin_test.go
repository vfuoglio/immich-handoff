//go:build e2e

package e2e

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/vfuoglio/immich-handoff/internal/mailin/mailtest"
)

func mailWith(to string, files map[string][]byte) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "From: A Guest <guest.private@example.net>\r\nTo: %s\r\nSubject: photos\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"B\"\r\n\r\n--B\r\nContent-Type: text/plain\r\n\r\nHere they are\r\n", to)
	for name, data := range files {
		enc := base64.StdEncoding.EncodeToString(data)
		var lines []string
		for len(enc) > 76 {
			lines, enc = append(lines, enc[:76]), enc[76:]
		}
		lines = append(lines, enc)
		fmt.Fprintf(&b, "--B\r\nContent-Type: image/jpeg\r\nContent-Disposition: attachment; filename=%q\r\nContent-Transfer-Encoding: base64\r\n\r\n%s\r\n", name, strings.Join(lines, "\r\n"))
	}
	b.WriteString("--B--\r\n")
	return []byte(b.String())
}

// TestPhotosByEmail: a mailbox is set up, a box gets an address, and a mail with photos sent to it arrives in the owner's
// library under the box's tag, like an upload; the mail is removed; a mail to an address nobody has is dropped.
func TestPhotosByEmail(t *testing.T) {
	alice, _, _, _ := bootstrap(t)
	mailbox := mailtest.Start(t, mailtest.Options{User: "drop", Password: "e2e-mailbox-secret"})
	host, mport := mailbox.Addr()
	bin := buildApp(t)
	port := fmt.Sprint(atoi(appPort) + 64)
	launchApp(t, bin, port, "HANDOFF_STORE_KEY=an-e2e-store-key-0123456789", "DATA_DIR="+t.TempDir(),
		"MAIL_IN_HOST="+host, fmt.Sprintf("MAIL_IN_PORT=%d", mport), "MAIL_IN_TLS=none", "MAIL_IN_USER=drop", "MAIL_IN_PASSWORD=e2e-mailbox-secret",
		"MAIL_IN_ADDRESS=drop@e2e.test", "MAIL_IN_EVERY=5s")
	owner := loginCookie(t, "alice@e2e.test")

	code, body := postBody(t, port, "/dropbox", owner, url.Values{"name": {"Postbox"}, "opendays": {"7"}})
	if code != 204 {
		t.Fatalf("make a drop box: %d %s", code, body)
	}
	op := page(t, port, owner)
	boxID := regexp.MustCompile(`(?:&#34;|")service(?:&#34;|"):(?:&#34;|")([0-9a-f-]{36})`).FindStringSubmatch(op)[1]
	if !strings.Contains(op, "Give this box an email address") {
		t.Fatal("with a mailbox set up the box should offer an address")
	}
	if code, body := postBody(t, port, "/dropbox/mail", owner, url.Values{"service": {boxID}, "on": {"1"}}); code != 204 {
		t.Fatalf("give the box an address: %d %s", code, body)
	}
	m := regexp.MustCompile(`value="(drop\+[a-z0-9]{10}@e2e\.test)"`).FindStringSubmatch(page(t, port, owner))
	if m == nil {
		t.Fatal("the box's address is not on the owner's page")
	}
	address := m[1]
	before := len(searchTag(t, alice, "inbox/Postbox", false))

	// a mail to an address nobody has is dropped; the mail with photos arrives
	mailbox.Deliver(mailWith("drop+zzzzzzzzzz@e2e.test", map[string][]byte{"nobody.jpg": texturedJPEG(901)}))
	mailbox.Deliver(mailWith(address, map[string][]byte{"mail-1.jpg": texturedJPEG(902), "mail-2.jpg": texturedJPEG(903)}))
	eventually(t, "the mailed photos arrive in the library", 90*time.Second, func() bool {
		return len(searchTag(t, alice, "inbox/Postbox", false)) == before+2
	})
	eventually(t, "the mails are removed from the mailbox", 30*time.Second, func() bool { return len(mailbox.Messages()) == 0 })
	if got := len(searchTag(t, alice, "inbox/Postbox", false)); got != before+2 {
		t.Errorf("the mail to nobody must store nothing: %d photos", got)
	}
	op = page(t, port, owner)
	if strings.Contains(op, "guest.private") {
		t.Error("the sender's address must not be on any page")
	}
}
