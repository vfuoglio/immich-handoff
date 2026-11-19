package dest_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/vfuoglio/immich-handoff/internal/bridge"
	"github.com/vfuoglio/immich-handoff/internal/dest"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/syncer"
)

type sftpServer struct {
	ln       net.Listener
	dir      string
	hostFP   string
	hostSign ssh.Signer
	clientPK ssh.PublicKey
	mu       sync.Mutex
	conns    []net.Conn
}

// newSFTP is an SSH server that serves SFTP on a folder, with a login (ann, hunter2, or a key) and a host key of its own.
func newSFTP(t *testing.T) *sftpServer {
	t.Helper()
	_, hpriv, _ := ed25519.GenerateKey(rand.Reader)
	hs, _ := ssh.NewSignerFromKey(hpriv)
	s := &sftpServer{dir: t.TempDir(), hostSign: hs, hostFP: ssh.FingerprintSHA256(hs.PublicKey())}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if c.User() == "ann" && string(pw) == "hunter2" {
				return nil, nil
			}
			return nil, errors.New("denied")
		},
		PublicKeyCallback: func(c ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			if s.clientPK != nil && c.User() == "ann" && bytes.Equal(k.Marshal(), s.clientPK.Marshal()) {
				return nil, nil
			}
			return nil, errors.New("denied")
		},
	}
	cfg.AddHostKey(hs)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.ln = ln
	t.Cleanup(func() { ln.Close(); s.dropAll() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.conns = append(s.conns, conn)
			s.mu.Unlock()
			go s.serve(conn, cfg)
		}
	}()
	return s
}

func (s *sftpServer) dropAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.conns {
		c.Close()
	}
	s.conns = nil
}

func (s *sftpServer) serve(conn net.Conn, cfg *ssh.ServerConfig) {
	sc, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		conn.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() != "session" {
			nc.Reject(ssh.UnknownChannelType, "no")
			continue
		}
		ch, creqs, _ := nc.Accept()
		go func() {
			for r := range creqs {
				ok := r.Type == "subsystem" && len(r.Payload) >= 8 && string(r.Payload[4:]) == "sftp"
				r.Reply(ok, nil)
				if ok {
					srv, err := sftp.NewServer(ch, sftp.WithServerWorkingDirectory(s.dir))
					if err == nil {
						srv.Serve()
						srv.Close()
					}
					return
				}
			}
		}()
	}
	sc.Close()
}

func (s *sftpServer) host() (string, string) {
	h, p, _ := net.SplitHostPort(s.ln.Addr().String())
	return h, p
}

func (s *sftpServer) dest(t *testing.T, mod func(*dest.SFTPConfig)) *dest.SFTP {
	t.Helper()
	h, p := s.host()
	cfg := dest.SFTPConfig{Host: h, Port: p, User: "ann", Password: "hunter2", HostKey: s.hostFP, Root: s.dir}
	if mod != nil {
		mod(&cfg)
	}
	d, err := dest.NewSFTP(cfg, dest.Net{AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestAnSFTPServerGetsFilesWholeReadBackCheckedSkippedWhenTheSameAndSuffixedWhenDifferent(t *testing.T) {
	s := newSFTP(t)
	d := s.dest(t, nil)
	a := put(t, d, "family/Bob/2026/03/IMG_1.jpg", "one")
	if a.Ref != "family/Bob/2026/03/IMG_1.jpg" || a.Existed || a.Size != 3 || a.Verified != "size and checksum" || len(a.Sum) != 40 {
		t.Fatalf("%+v", a)
	}
	if b, _ := os.ReadFile(filepath.Join(s.dir, "family/Bob/2026/03/IMG_1.jpg")); string(b) != "one" {
		t.Error("the bytes are there, in the folders it made")
	}
	if same := put(t, d, "family/Bob/2026/03/IMG_1.jpg", "one"); !same.Existed || same.Ref != a.Ref {
		t.Errorf("the same file: %+v", same)
	}
	if other := put(t, d, "family/Bob/2026/03/IMG_1.jpg", "two"); other.Ref != "family/Bob/2026/03/IMG_1 (2).jpg" || other.Existed {
		t.Errorf("a different file: %+v", other)
	}
	if again := put(t, d, "family/Bob/2026/03/IMG_1.jpg", "two"); !again.Existed || again.Ref != "family/Bob/2026/03/IMG_1 (2).jpg" {
		t.Errorf("a repeat finds it: %+v", again)
	}
	var tmp []string
	filepath.WalkDir(s.dir, func(p string, e os.DirEntry, err error) error {
		if err == nil && strings.Contains(e.Name(), ".handoff-tmp-") {
			tmp = append(tmp, p)
		}
		return nil
	})
	if len(tmp) != 0 {
		t.Errorf("no temporary file is left: %v", tmp)
	}
	if info, err := d.Stat(context.Background(), a.Ref); err != nil || info.Size != 3 {
		t.Errorf("%+v %v", info, err)
	}
	if _, err := d.Stat(context.Background(), "nope.jpg"); !errors.Is(err, dest.ErrNotFound) {
		t.Errorf("%v", err)
	}
	r, err := d.Open(context.Background(), a.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := io.ReadAll(r); string(b) != "one" {
		t.Error("open reads it")
	}
	r.Close()
	if all, err := d.List(context.Background(), ""); err != nil || len(all) != 2 || all[0].Size != 3 {
		t.Errorf("%+v %v", all, err)
	}
	// deleting removes the folders it leaves empty up to the root, and never the root
	if err := d.Delete(context.Background(), a.Ref); err != nil {
		t.Fatal(err)
	}
	d.Delete(context.Background(), "family/Bob/2026/03/IMG_1 (2).jpg")
	if _, err := os.Stat(filepath.Join(s.dir, "family")); err == nil {
		t.Error("the empty folders went")
	}
	if _, err := os.Stat(s.dir); err != nil {
		t.Error("the root stays")
	}
	if err := d.Delete(context.Background(), "not/there.jpg"); err != nil {
		t.Errorf("a file that is not there is not an error: %v", err)
	}
}

func TestALargeFileIsStreamedToAnSFTPServerAndReadBack(t *testing.T) {
	s := newSFTP(t)
	d := s.dest(t, nil)
	big := make([]byte, 6<<20)
	rand.Read(big)
	st, err := d.Put(context.Background(), "big.mov", bytes.NewReader(big), int64(len(big)))
	if err != nil || st.Size != int64(len(big)) {
		t.Fatalf("%+v %v", st, err)
	}
	if got, _ := os.ReadFile(filepath.Join(s.dir, "big.mov")); !bytes.Equal(got, big) {
		t.Error("the same bytes")
	}
	if _, err := d.Put(context.Background(), "short.mov", bytes.NewReader([]byte("abc")), 10); err == nil {
		t.Error("a short body")
	}
	if left, _ := os.ReadDir(s.dir); len(left) != 1 {
		t.Errorf("nothing is left of the failed write: %v", left)
	}
}

func TestTheServersKeyMustBePinnedAndAChangedKeyStopsEverythingAndIsSaidInWords(t *testing.T) {
	s := newSFTP(t)
	var hk *dest.HostKeyError
	_, err := s.dest(t, func(c *dest.SFTPConfig) { c.HostKey = "" }).Check(context.Background())
	if !errors.As(err, &hk) || hk.Seen != s.hostFP || hk.Pinned != "" || !strings.Contains(err.Error(), s.hostFP) || !strings.Contains(err.Error(), "set host_key") {
		t.Fatalf("an unpinned server is never trusted, and its key is shown: %v", err)
	}
	other := "SHA256:" + strings.Repeat("A", 43)
	d := s.dest(t, func(c *dest.SFTPConfig) { c.HostKey = other })
	_, err = d.Check(context.Background())
	if !errors.As(err, &hk) || hk.Pinned != other || hk.Seen != s.hostFP || !strings.Contains(err.Error(), "CHANGED") {
		t.Fatalf("a different key: %v", err)
	}
	if _, err := d.Put(context.Background(), "x.jpg", strings.NewReader("x"), 1); err == nil {
		t.Error("nothing is sent to a server whose key changed")
	}
	if left, _ := os.ReadDir(s.dir); len(left) != 0 {
		t.Errorf("%v", left)
	}
	// the pin may be written with or without the SHA256: prefix
	if _, err := s.dest(t, func(c *dest.SFTPConfig) { c.HostKey = strings.TrimPrefix(s.hostFP, "SHA256:") }).Check(context.Background()); err != nil {
		t.Errorf("%v", err)
	}
	// and a person is shown the key to check before pinning it
	h, p := s.host()
	fp, err := dest.ProbeHostKey(context.Background(), h, p, dest.Net{AllowPrivate: true})
	if err != nil || fp != s.hostFP {
		t.Errorf("%q %v", fp, err)
	}
	if _, err := dest.ProbeHostKey(context.Background(), h, p, dest.Net{}); err == nil {
		t.Error("a private address is not probed without the switch")
	}
}

func TestACheckOfAnSFTPServerSaysWhatIsWrongInWordsAndNeverShowsThePasswordOrTheAddress(t *testing.T) {
	s := newSFTP(t)
	if msg, err := s.dest(t, nil).Check(context.Background()); err != nil || !strings.Contains(msg, "can write in") {
		t.Fatalf("%q %v", msg, err)
	}
	if left, _ := os.ReadDir(s.dir); len(left) != 0 {
		t.Errorf("the test file is removed: %v", left)
	}
	h, p := s.host()
	fail := func(d dest.Destination) string {
		_, err := d.Check(context.Background())
		if err == nil {
			t.Fatal("it should fail")
		}
		for _, secret := range []string{"hunter2", "wrong", h + ":" + p} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("gives away %q: %v", secret, err)
			}
		}
		return err.Error()
	}
	if msg := fail(s.dest(t, func(c *dest.SFTPConfig) { c.Password = "wrong" })); !strings.Contains(msg, "did not accept the user name") {
		t.Errorf("%q", msg)
	}
	if msg := fail(s.dest(t, func(c *dest.SFTPConfig) { c.Root = filepath.Join(s.dir, "not-there") })); !strings.Contains(msg, "does not exist on the SFTP server") {
		t.Errorf("%q", msg)
	}
	d := s.dest(t, nil)
	s.ln.Close()
	s.dropAll()
	if msg := fail(d); !strings.Contains(msg, "SFTP server") {
		t.Errorf("%q", msg)
	}
}

func TestALoginByPrivateKeyWorksAndAKeyThatCannotBeReadIsSaidSo(t *testing.T) {
	s := newSFTP(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	sshPub, _ := ssh.NewPublicKey(pub)
	s.clientPK = sshPub
	blk, _ := ssh.MarshalPrivateKey(priv, "")
	pemKey := string(pem.EncodeToMemory(blk))
	d := s.dest(t, func(c *dest.SFTPConfig) { c.Password, c.PrivateKey = "", pemKey })
	if _, err := d.Check(context.Background()); err != nil {
		t.Fatalf("%v", err)
	}
	h, p := s.host()
	if _, err := dest.NewSFTP(dest.SFTPConfig{Host: h, Port: p, User: "ann", PrivateKey: "not a key", HostKey: s.hostFP}, dest.Net{AllowPrivate: true}); err == nil || !strings.Contains(err.Error(), "private key cannot be read") {
		t.Errorf("%v", err)
	}
	if _, err := dest.NewSFTP(dest.SFTPConfig{Host: h, Port: p, User: "ann"}, dest.Net{AllowPrivate: true}); err == nil {
		t.Error("a password or a key is needed")
	}
}

func TestASessionThatWasDroppedIsMadeAgainAndAPrivateAddressNeedsTheSwitch(t *testing.T) {
	s := newSFTP(t)
	d := s.dest(t, nil)
	put(t, d, "a.jpg", "a")
	s.dropAll() // the server went away and came back
	if st := put(t, d, "b.jpg", "b"); st.Ref != "b.jpg" {
		t.Errorf("%+v", st)
	}
	h, p := s.host()
	if _, err := dest.NewSFTP(dest.SFTPConfig{Host: h, Port: p, User: "ann", Password: "x", HostKey: s.hostFP}, dest.Net{}); !errors.Is(err, bridge.ErrBadAddress) {
		t.Errorf("a private address without the switch: %v", err)
	}
	// a name that points inside is refused when it connects
	n, err := dest.NewSFTP(dest.SFTPConfig{Host: "localhost", Port: p, User: "ann", Password: "x", HostKey: s.hostFP}, dest.Net{})
	if err != nil {
		t.Skip("localhost is refused by name here")
	}
	if _, err := n.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "private network") {
		t.Errorf("%v", err)
	}
}

func TestAWriteThatHangsEndsWithTheContext(t *testing.T) {
	s := newSFTP(t)
	d := s.dest(t, nil)
	pr, pw := io.Pipe()
	defer pw.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	go func() { // the source of a real transfer is bound to the context (an HTTP body is), so a cancel ends its read
		<-ctx.Done()
		pw.CloseWithError(ctx.Err())
	}()
	start := time.Now()
	if _, err := d.Put(ctx, "x.jpg", pr, -1); err == nil || time.Since(start) > 10*time.Second {
		t.Fatalf("%v after %v", err, time.Since(start))
	}
	pw.Close()
	if _, err := d.Check(context.Background()); err != nil {
		t.Errorf("it works again afterwards: %v", err)
	}
}

func TestAnSFTPServerAsAReceiverGetsThePhotoSidecarRecordAndClipAndTakeBackDeletesThemAll(t *testing.T) {
	s := newSFTP(t)
	d := s.dest(t, nil)
	a, id := photo("IMG_4.HEIC")
	rating := 3
	a.ExifInfo = &immich.ExifResponseDto{Rating: &rating}
	clip, cid := photo("IMG_4.MOV")
	api := immichWith(t, map[string][]byte{id: []byte("still"), cid: []byte("clip")}, false)
	r := &dest.Remote{Ident: "sftp", Name: "the server", Service: "family", D: d}
	cp, err := r.Deliver(context.Background(), api, a, &clip, syncer.RemoteJob{Sender: "Bob", Details: true})
	if err != nil || cp.Ref != "IMG_4.HEIC" || cp.Clip != "IMG_4.MOV" {
		t.Fatalf("%+v %v", cp, err)
	}
	if left, _ := os.ReadDir(s.dir); len(left) != 4 {
		t.Errorf("the photo, sidecar, record and clip: %v", left)
	}
	if rec, _ := os.ReadFile(filepath.Join(s.dir, "IMG_4.HEIC.handoff.json")); !strings.Contains(string(rec), `"verified": "size and checksum"`) {
		t.Errorf("%s", rec)
	}
	if err := r.TakeBackRefs(context.Background(), []string{cp.Ref, cp.Clip}); err != nil {
		t.Fatal(err)
	}
	if left, _ := os.ReadDir(s.dir); len(left) != 0 {
		t.Errorf("%v", left)
	}
}
