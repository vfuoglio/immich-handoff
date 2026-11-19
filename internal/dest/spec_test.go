package dest_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vfuoglio/immich-handoff/internal/dest"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestTheDestinationsFileIsParsedWithVariablesAndSecretFilesAndEveryProblemIsReported(t *testing.T) {
	raw := `destinations:
  nas:
    kind: folder
    root: /destinations/nas
  cloud:
    kind: S3
    bucket: photos
    access_key: ${S3_KEY}
    secret_key_file: /run/secrets/s3
`
	files := func(p string) ([]byte, error) {
		if p == "/run/secrets/s3" {
			return []byte("sekrit\n"), nil
		}
		return nil, errors.New("no such file")
	}
	got, err := dest.ParseFile([]byte(raw), env(map[string]string{"S3_KEY": "AK"}), files)
	if err != nil {
		t.Fatal(err)
	}
	if got["nas"].Kind != "folder" || got["nas"].Settings["root"] != "/destinations/nas" {
		t.Errorf("%+v", got["nas"])
	}
	if c := got["cloud"]; c.Kind != "s3" || c.Settings["access_key"] != "AK" || c.Settings["secret_key"] != "sekrit" || c.Settings["secret_key_file"] != "" {
		t.Errorf("a variable and a secret file: %+v", c)
	}
	bad := `destinations:
  a:
    kind: folder
    root: relative/path
  b:
    kind: ftp
  c:
    kind: webdav
    url: ${NOPE}
  "bad/name":
    kind: folder
    root: /x
  d:
    kind: s3
    bucket: b
    access_key: x
    secret_key_file: /missing
`
	_, err = dest.ParseFile([]byte(bad), env(nil), files)
	if err == nil {
		t.Fatal("problems")
	}
	for _, want := range []string{"destination a: a folder's root must be an absolute path", `destination b: the kind "ftp" is not one of`, "environment variable NOPE is not set", "destination bad/name: a name is 1 to 40", "destination d: secret_key_file: no such file"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message lacks %q: %v", want, err)
		}
	}
	if _, err := dest.ParseFile([]byte("destinations:\n  a:\n    kind: sftp\n    host: h\n    user: u\n"), env(nil), files); err == nil || !strings.Contains(err.Error(), "needs password or private_key") {
		t.Errorf("one of a group is needed: %v", err)
	}
	if _, err := dest.ParseFile([]byte("destinatons: {}"), env(nil), files); err == nil {
		t.Error("an unknown key is refused, not ignored")
	}
}

func TestAKindWithNoBuilderSaysSoAndARegisteredOneIsBuilt(t *testing.T) {
	if _, err := dest.Build(dest.Spec{Name: "x", Kind: "tape"}, dest.Net{}); !errors.Is(err, dest.ErrKindUnavailable) {
		t.Errorf("%v", err)
	}
	dest.Register("memory-test", func(s dest.Spec, n dest.Net) (dest.Destination, error) { return &dest.Memory{}, nil })
	if d, err := dest.Build(dest.Spec{Kind: "memory-test"}, dest.Net{}); err != nil || d == nil {
		t.Errorf("%v", err)
	}
}

func TestATemplateIsCheckedAndANastyValueCannotMakeAPathLeaveTheRoot(t *testing.T) {
	for _, bad := range []string{"/abs/{name}", "a/../b", "a\\b", "{nope}/x", "a{sender", "x}", strings.Repeat("a", 201), "a\x00b"} {
		if dest.ValidTemplate(bad) == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
	for _, ok := range []string{"", "{service}/{sender}/{yyyy}/{mm}", "{yyyy}-{mm}-{dd}_{name}", "backups/photos"} {
		if err := dest.ValidTemplate(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	at := time.Date(2026, 3, 9, 10, 0, 0, 0, time.UTC)
	if got := dest.Path("", dest.Vars{Service: "family", Sender: "Bob", Name: "IMG_1.jpg", Taken: at}); got != "family/Bob/2026/03/IMG_1.jpg" {
		t.Errorf("the default: %q", got)
	}
	if got := dest.Path("{yyyy}-{mm}-{dd}_{name}", dest.Vars{Name: "IMG_1.jpg", Taken: at}); got != "2026-03-09_IMG_1.jpg" {
		t.Errorf("{name} in the template: %q", got)
	}
	for _, v := range []dest.Vars{
		{Service: "..", Sender: "../../etc", Name: "../../passwd", Taken: at},
		{Service: "a/b", Sender: `c\d`, Name: "e:f*g?.jpg", Taken: at},
		{Service: "", Sender: "   ", Name: "..", Taken: at},
		{Service: strings.Repeat("é", 500), Sender: "x", Name: strings.Repeat("n", 500) + ".jpg", Taken: at},
	} {
		got := dest.Path("{service}/{sender}/{yyyy}/{name}", v)
		if strings.HasPrefix(got, "/") || strings.Contains(got, "\\") || strings.Contains(got, "//") || len(got) > 480 || got == "" {
			t.Errorf("%+v made %q", v, got)
		}
		for _, seg := range strings.Split(got, "/") {
			if seg == "" || seg == "." || seg == ".." || len([]rune(seg)) > 120 {
				t.Errorf("%+v made a bad piece %q in %q", v, seg, got)
			}
		}
	}
}
