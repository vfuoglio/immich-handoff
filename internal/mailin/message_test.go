package mailin

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"net/mail"
	"strings"
	"testing"
)

func jpegLike(n int) []byte {
	b := []byte{0xff, 0xd8, 0xff, 0xe0, 0, 0x10, 'J', 'F', 'I', 'F', 0}
	for len(b) < n {
		b = append(b, byte(len(b)*7))
	}
	return b
}

func b64(data []byte) string {
	s := base64.StdEncoding.EncodeToString(data)
	var out strings.Builder
	for len(s) > 76 {
		out.WriteString(s[:76] + "\r\n")
		s = s[76:]
	}
	return out.String() + s
}

func header(extra ...string) string {
	return "From: Bob <Bob@Example.com>\r\nSubject: photos\r\n" + strings.Join(extra, "\r\n") + "\r\n"
}

func TestTheBoxCodeIsReadFromTheAddressTheMessageWasDeliveredTo(t *testing.T) {
	cases := map[string]string{
		"To: drop+abc123xyz9@example.org":                           "abc123xyz9",
		"To: Drop <DROP+ABC123xyz9@Example.ORG>":                    "abc123xyz9",
		"Delivered-To: drop+abc123xyz9@example.org":                 "abc123xyz9",
		"X-Original-To: drop+abc123xyz9@example.org":                "abc123xyz9",
		"To: friend@example.com\r\nCc: drop+abc123xyz9@example.org": "abc123xyz9",
		"To: someone@example.com, drop+abc123xyz9@example.org":      "abc123xyz9",
		"To: drop@example.org":                                      "", // the mailbox itself, no code
		"To: drop+abc123xyz9@other.org":                             "", // another domain
		"To: other+abc123xyz9@example.org":                          "", // another mailbox
		"To: drop+a@example.org":                                    "", // too short to be a code
		"To: drop+abc 123@example.org":                              "",
		"To: drop+abc123xyz9+more@example.org":                      "",
		"":                                                          "",
	}
	for h, want := range cases {
		msg, err := mail.ReadMessage(strings.NewReader(header(h) + "\r\nbody"))
		if err != nil {
			t.Fatal(err)
		}
		if got := CodeOf(msg.Header, "drop@example.org"); got != want {
			t.Errorf("%q: %q, want %q", h, got, want)
		}
	}
	// what the server wrote about the delivery wins over what the sender wrote in To
	msg, _ := mail.ReadMessage(strings.NewReader(header("Delivered-To: drop+realone0001@example.org", "To: drop+fakeone0002@example.org") + "\r\nbody"))
	if got := CodeOf(msg.Header, "drop@example.org"); got != "realone0001" {
		t.Errorf("%q", got)
	}
}

func TestPhotosAreTakenFromNestedPartsInEveryEncodingAndTextIsLeftOut(t *testing.T) {
	photo, second, third := jpegLike(20<<10), jpegLike(30<<10), jpegLike(12<<10)
	tiny := jpegLike(2 << 10)
	raw := header("To: drop+abc123xyz9@example.org", `Content-Type: multipart/mixed; boundary="OUTER"`) + "\r\n" +
		"--OUTER\r\nContent-Type: multipart/alternative; boundary=\"INNER\"\r\n\r\n" +
		"--INNER\r\nContent-Type: text/plain\r\n\r\nHere are the photos\r\n" +
		"--INNER\r\nContent-Type: text/html\r\n\r\n<p>Here are the photos</p>\r\n--INNER--\r\n" +
		"--OUTER\r\nContent-Type: image/jpeg; name=\"one.jpg\"\r\nContent-Disposition: attachment; filename=\"IMG_0001.JPG\"\r\nContent-Transfer-Encoding: base64\r\n\r\n" + b64(photo) + "\r\n" +
		"--OUTER\r\nContent-Type: application/octet-stream\r\nContent-Disposition: inline; filename*=UTF-8''caf%C3%A9.heic\r\nContent-Transfer-Encoding: base64\r\n\r\n" + b64(second) + "\r\n" +
		"--OUTER\r\nContent-Type: image/png; name=\"logo.png\"\r\nContent-Transfer-Encoding: base64\r\n\r\n" + b64(tiny) + "\r\n" +
		"--OUTER\r\nContent-Type: image/jpeg\r\nContent-Disposition: attachment; filename=\"../../etc/passwd.jpg\"\r\nContent-Transfer-Encoding: 8bit\r\n\r\n" + string(third) + "\r\n" +
		"--OUTER--\r\n"
	p, err := Parse([]byte(raw), "drop@example.org", 10<<20)
	if err != nil {
		t.Fatal(err)
	}
	if p.Code != "abc123xyz9" || p.From != "bob@example.com" {
		t.Errorf("%q %q", p.Code, p.From)
	}
	if len(p.Files) != 3 {
		t.Fatalf("three photos; the signature icon and the text are left out: %d", len(p.Files))
	}
	if p.Files[0].Name != "IMG_0001.JPG" || !bytes.Equal(p.Files[0].Data, photo) {
		t.Errorf("first: %q", p.Files[0].Name)
	}
	if p.Files[1].Name != "café.heic" || !bytes.Equal(p.Files[1].Data, second) {
		t.Errorf("second: %q", p.Files[1].Name)
	}
	if p.Files[2].Name != "passwd.jpg" || strings.ContainsAny(p.Files[2].Name, "/\\") {
		t.Errorf("a file name is only a name: %q", p.Files[2].Name)
	}
}

func TestAQuotedPrintableAndASinglePartMessageAreRead(t *testing.T) {
	photo := jpegLike(9 << 10)
	var qp strings.Builder
	for i, b := range photo {
		qp.WriteString(fmt.Sprintf("=%02X", b))
		if i%24 == 23 {
			qp.WriteString("=\r\n") // a soft line break: mail lines are short
		}
	}
	raw := header("To: drop+abc123xyz9@example.org", "Content-Type: image/jpeg", "Content-Disposition: attachment; filename=one.jpg", "Content-Transfer-Encoding: quoted-printable") + "\r\n" + qp.String()
	p, err := Parse([]byte(raw), "drop@example.org", 10<<20)
	if err != nil || len(p.Files) != 1 || !bytes.Equal(p.Files[0].Data, photo) || p.Files[0].Name != "one.jpg" {
		t.Fatalf("%d files, %v", len(p.Files), err)
	}
}

func TestAMessageForNoBoxIsNotDecodedAndAMessageOfNothingGivesNothing(t *testing.T) {
	big := b64(jpegLike(50 << 10))
	raw := header("To: drop@example.org", `Content-Type: multipart/mixed; boundary="X"`) + "\r\n--X\r\nContent-Type: image/jpeg\r\nContent-Transfer-Encoding: base64\r\n\r\n" + big + "\r\n--X--\r\n"
	p, err := Parse([]byte(raw), "drop@example.org", 10<<20)
	if err != nil || p.Code != "" || len(p.Files) != 0 {
		t.Errorf("%+v %v", p, err)
	}
	text := header("To: drop+abc123xyz9@example.org", "Content-Type: text/plain") + "\r\njust words"
	if p, err := Parse([]byte(text), "drop@example.org", 10<<20); err != nil || p.Code == "" || len(p.Files) != 0 {
		t.Errorf("%+v %v", p, err)
	}
	if _, err := Parse([]byte("this is not a message"), "drop@example.org", 10<<20); err == nil {
		t.Error("nonsense was taken for a message")
	}
}

func TestHostileMessagesCostLittle(t *testing.T) {
	// more parts than are looked at, nested deeper than are followed, a part that lies about its encoding, a boundary missing
	var many strings.Builder
	many.WriteString(header("To: drop+abc123xyz9@example.org", `Content-Type: multipart/mixed; boundary="B"`) + "\r\n")
	for i := 0; i < 200; i++ {
		many.WriteString("--B\r\nContent-Type: image/jpeg\r\nContent-Transfer-Encoding: base64\r\n\r\n" + b64(jpegLike(9<<10)) + "\r\n")
	}
	many.WriteString("--B--\r\n")
	p, err := Parse([]byte(many.String()), "drop@example.org", 100<<20)
	if err != nil || len(p.Files) > maxFiles {
		t.Errorf("%d files, %v", len(p.Files), err)
	}

	deep := header("To: drop+abc123xyz9@example.org", `Content-Type: multipart/mixed; boundary="L0"`) + "\r\n"
	for i := 0; i < 10; i++ {
		deep += fmt.Sprintf("--L%d\r\nContent-Type: multipart/mixed; boundary=\"L%d\"\r\n\r\n", i, i+1)
	}
	deep += "--L10\r\nContent-Type: image/jpeg\r\nContent-Transfer-Encoding: base64\r\n\r\n" + b64(jpegLike(9<<10)) + "\r\n"
	if p, err := Parse([]byte(deep), "drop@example.org", 10<<20); err != nil || len(p.Files) != 0 {
		t.Errorf("nesting deeper than %d must not be followed: %d %v", maxDepth, len(p.Files), err)
	}

	lie := header("To: drop+abc123xyz9@example.org", "Content-Type: image/jpeg", "Content-Transfer-Encoding: base64") + "\r\n!!!! not base64 !!!!"
	if p, err := Parse([]byte(lie), "drop@example.org", 10<<20); err != nil || len(p.Files) != 0 {
		t.Errorf("damaged base64: %d %v", len(p.Files), err)
	}
	noBoundary := header("To: drop+abc123xyz9@example.org", "Content-Type: multipart/mixed") + "\r\n--x\r\n"
	if p, err := Parse([]byte(noBoundary), "drop@example.org", 10<<20); err != nil || len(p.Files) != 0 {
		t.Errorf("no boundary: %d %v", len(p.Files), err)
	}

	// the total kept is bounded: 3 photos of 400 KB with a 1 MB allowance keeps two and stops
	var three strings.Builder
	three.WriteString(header("To: drop+abc123xyz9@example.org", `Content-Type: multipart/mixed; boundary="B"`) + "\r\n")
	for i := 0; i < 3; i++ {
		three.WriteString("--B\r\nContent-Type: image/jpeg\r\nContent-Transfer-Encoding: base64\r\n\r\n" + b64(jpegLike(400<<10)) + "\r\n")
	}
	three.WriteString("--B--\r\n")
	if p, _ := Parse([]byte(three.String()), "drop@example.org", 1<<20); len(p.Files) != 2 {
		t.Errorf("the allowance is for all the files of a message: %d kept", len(p.Files))
	}
}

func TestFileNamesAreOnlyNames(t *testing.T) {
	for in, want := range map[string]string{
		"IMG_1.JPG": "IMG_1.JPG", "../../x.jpg": "x.jpg", `C:\Users\me\a.jpg`: "a.jpg", "a\x00b\x1f.jpg": "ab.jpg", "": "mail-attachment", "/": "mail-attachment",
		"=?UTF-8?B?Y2Fmw6kuanBn?=": "café.jpg", strings.Repeat("é", 300): strings.Repeat("é", 100),
	} {
		if got := cleanName(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
