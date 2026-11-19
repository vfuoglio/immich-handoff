package dest

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/vfuoglio/immich-handoff/internal/bridge"
)

func init() {
	Register("sftp", func(s Spec, n Net) (Destination, error) {
		return NewSFTP(SFTPConfig{Host: s.Settings["host"], Port: s.Settings["port"], User: s.Settings["user"], Password: s.Settings["password"],
			PrivateKey: s.Settings["private_key"], Passphrase: s.Settings["passphrase"], HostKey: s.Settings["host_key"], Root: s.Settings["root"]}, n)
	})
}

// SFTPConfig is where an SFTP destination is, how it logs in and which server it trusts.
type SFTPConfig struct {
	Host, Port string
	User       string
	Password   string
	PrivateKey string // the text of a private key (instead of a password)
	Passphrase string // of the private key, when it has one
	// HostKey is the fingerprint ("SHA256:...") of the server's key that is trusted. Nothing is connected to without it: the app never
	// accepts a key it was not told to, and says what the server's key is so that the person can check it and pin it.
	HostKey string
	Root    string // the folder files go under on the server (empty: the login's home folder)
}

// HostKeyError says what is wrong with the server's key: it was not pinned yet (Pinned is empty) or it is not the one that was pinned.
type HostKeyError struct{ Pinned, Seen string }

func (e *HostKeyError) Error() string {
	if e.Pinned == "" {
		return fmt.Sprintf("the server has not been trusted yet: its host key is %s. Check that this is your server's key, then set host_key to it", e.Seen)
	}
	return fmt.Sprintf("the server's host key has CHANGED: it was %s and now it is %s. Nothing is sent until you have checked why (a reinstalled server, or someone else answering) and trust the new key", e.Pinned, e.Seen)
}

// SFTP is a Destination on an SSH server's SFTP service, with the pure-Go SSH client. The connection goes through the hardened dialer of
// bridges (a name is resolved and judged, a private address only with the administrator's switch), the host key is pinned and checked on
// every connection, a file is written under a temporary name and moved into place, and what was written is read back and compared by size
// and SHA-1 before the photo counts as held.
type SFTP struct {
	cfg    SFTPConfig
	policy bridge.Policy
	signer ssh.Signer

	mu   sync.Mutex
	ssh  *ssh.Client
	c    *sftp.Client
	base string // the folder files go under, once looked at
}

// NewSFTP makes a Destination of an SFTP server; it connects only when it is used.
func NewSFTP(cfg SFTPConfig, n Net) (*SFTP, error) {
	cfg.Host = strings.TrimSpace(cfg.Host)
	if cfg.Port == "" {
		cfg.Port = "22"
	}
	if err := CheckAddress(Spec{Kind: "sftp", Settings: map[string]string{"host": cfg.Host, "port": cfg.Port}}, n); err != nil {
		return nil, fmt.Errorf("%w: %v", bridge.ErrBadAddress, err)
	}
	if cfg.User == "" || (cfg.Password == "" && cfg.PrivateKey == "") {
		return nil, errors.New("an SFTP destination needs a user name and a password or a private key")
	}
	s := &SFTP{cfg: cfg, policy: bridge.Policy{AllowPrivate: n.AllowPrivate}}
	if cfg.PrivateKey != "" {
		var err error
		if cfg.Passphrase != "" {
			s.signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(cfg.PrivateKey), []byte(cfg.Passphrase))
		} else {
			s.signer, err = ssh.ParsePrivateKey([]byte(cfg.PrivateKey))
		}
		if err != nil {
			return nil, errors.New("the private key cannot be read (is it a key in OpenSSH or PEM form, and is its passphrase right?)")
		}
	}
	return s, nil
}

// Fingerprint is how the app writes a key: SHA256 of it, in base64, as OpenSSH shows it.
func Fingerprint(k ssh.PublicKey) string { return ssh.FingerprintSHA256(k) }

func normFingerprint(f string) string {
	f = strings.TrimSpace(f)
	if f != "" && !strings.HasPrefix(strings.ToUpper(f), "SHA256:") {
		f = "SHA256:" + f
	}
	if len(f) > 7 {
		f = "SHA256:" + f[7:]
	}
	return f
}

// ProbeHostKey connects to a server only to learn its key, which is what a person is shown to check and pin; no login is tried.
func ProbeHostKey(ctx context.Context, host, port string, n Net) (string, error) {
	if port == "" {
		port = "22"
	}
	if err := CheckAddress(Spec{Kind: "sftp", Settings: map[string]string{"host": host, "port": port}}, n); err != nil {
		return "", err
	}
	p := bridge.Policy{AllowPrivate: n.AllowPrivate}
	conn, err := p.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return "", fmt.Errorf("the server could not be reached: %w", scrub(err, host))
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(20 * time.Second))
	seen := ""
	cfg := &ssh.ClientConfig{User: "probe", Auth: []ssh.AuthMethod{ssh.Password("")}, HostKeyCallback: func(_ string, _ net.Addr, k ssh.PublicKey) error {
		seen = Fingerprint(k)
		return errors.New("stop here")
	}}
	ssh.NewClientConn(conn, net.JoinHostPort(host, port), cfg)
	if seen == "" {
		return "", errors.New("the server did not answer like an SSH server")
	}
	return seen, nil
}

// scrub takes the server's name out of a message (messages end up in logs, pages and notifications), where it stands alone: a short name
// must not be cut out of the middle of other words.
func scrub(err error, host string) error {
	msg := err.Error()
	if host == "" {
		return err
	}
	isName := func(b byte) bool {
		return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '-' || b == '_' || b == '.'
	}
	var out strings.Builder
	for i := 0; i < len(msg); {
		if strings.HasPrefix(msg[i:], host) && (i == 0 || !isName(msg[i-1])) && (i+len(host) == len(msg) || !isName(msg[i+len(host)])) {
			out.WriteString("the server")
			i += len(host)
			continue
		}
		out.WriteByte(msg[i])
		i++
	}
	return errors.New(out.String())
}

func (s *SFTP) hostKeyCheck(seen *string) ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, k ssh.PublicKey) error {
		fp := Fingerprint(k)
		*seen = fp
		pin := normFingerprint(s.cfg.HostKey)
		if pin == "" {
			return &HostKeyError{Seen: fp}
		}
		if pin != fp {
			return &HostKeyError{Pinned: pin, Seen: fp}
		}
		return nil
	}
}

// drop closes the connection (the next use makes a new one).
func (s *SFTP) drop() {
	if s.c != nil {
		s.c.Close()
	}
	if s.ssh != nil {
		s.ssh.Close()
	}
	s.c, s.ssh, s.base = nil, nil, ""
}

// connect opens the SSH connection and the SFTP session, with the host key checked and the login tried.
func (s *SFTP) connect(ctx context.Context) error {
	addr := net.JoinHostPort(s.cfg.Host, s.cfg.Port)
	conn, err := s.policy.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("the SFTP server could not be reached: %w", scrub(err, s.cfg.Host))
	}
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	var seen string
	var methods []ssh.AuthMethod
	if s.signer != nil {
		methods = append(methods, ssh.PublicKeys(s.signer))
	}
	if s.cfg.Password != "" {
		methods = append(methods, ssh.Password(s.cfg.Password))
	}
	cc, chans, reqs, err := ssh.NewClientConn(conn, addr, &ssh.ClientConfig{User: s.cfg.User, Auth: methods, HostKeyCallback: s.hostKeyCheck(&seen), Timeout: 30 * time.Second})
	if err != nil {
		conn.Close()
		var hk *HostKeyError
		switch {
		case errors.As(err, &hk):
			return hk
		case strings.Contains(err.Error(), "unable to authenticate"):
			return errors.New("the SFTP server did not accept the user name and password or key")
		}
		return fmt.Errorf("the SFTP server could not be used: %w", scrub(err, s.cfg.Host))
	}
	conn.SetDeadline(time.Time{})
	sc := ssh.NewClient(cc, chans, reqs)
	c, err := sftp.NewClient(sc, sftp.MaxPacket(1<<15))
	if err != nil {
		sc.Close()
		return errors.New("the server does not offer SFTP to this user")
	}
	s.ssh, s.c = sc, c
	return nil
}

// ready gives a working session: the one kept from before when it still answers, else a new one. The folder files go under must exist.
func (s *SFTP) ready(ctx context.Context) (*sftp.Client, error) {
	if s.c != nil {
		if _, err := s.c.Getwd(); err != nil {
			s.drop()
		}
	}
	if s.c == nil {
		if err := s.connect(ctx); err != nil {
			return nil, err
		}
	}
	if s.base == "" {
		base := s.cfg.Root
		if base == "" {
			wd, err := s.c.Getwd()
			if err != nil {
				s.drop()
				return nil, err
			}
			base = wd
		}
		base = path.Clean(base)
		fi, err := s.c.Stat(base)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return nil, fmt.Errorf("the folder %s does not exist on the SFTP server: make it, or correct the setting", base)
		case err != nil:
			return nil, err
		case !fi.IsDir():
			return nil, fmt.Errorf("%s on the SFTP server is not a folder", base)
		}
		s.base = base
	}
	return s.c, nil
}

// session runs fn on a working session, one operation at a time. A cancelled context closes the connection, which ends a transfer that is
// stuck; the session is made again next time.
func (s *SFTP) session(ctx context.Context, fn func(c *sftp.Client) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.ready(ctx)
	if err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() {
		if s.ssh != nil {
			s.ssh.Close()
		}
	})
	defer stop()
	err = fn(c)
	if err != nil && ctx.Err() != nil {
		s.drop()
		return ctx.Err()
	}
	if err != nil && isConnErr(err) {
		s.drop()
	}
	return err
}

func isConnErr(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, net.ErrClosed) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	m := err.Error()
	return strings.Contains(m, "connection lost") || strings.Contains(m, "broken pipe") || strings.Contains(m, "connection reset")
}

func (s *SFTP) full(name string) (string, error) {
	name = path.Clean("/" + strings.ReplaceAll(name, "\\", "/"))
	if name == "/" {
		return "", errors.New("a file needs a name")
	}
	return path.Join(s.base, name), nil
}

func (s *SFTP) ref(p string) string { return strings.TrimPrefix(strings.TrimPrefix(p, s.base), "/") }

// Put implements Destination. The file is written to a temporary name beside its place, read back and compared (size and SHA-1), and
// moved into place; a name already taken by a file with the same content is left alone, and a different file gets "name (2).ext".
func (s *SFTP) Put(ctx context.Context, name string, r io.Reader, size int64) (Stored, error) {
	var st Stored
	err := s.session(ctx, func(c *sftp.Client) error {
		target, err := s.full(name)
		if err != nil {
			return err
		}
		dir := path.Dir(target)
		if err := c.MkdirAll(dir); err != nil {
			return fmt.Errorf("cannot make the folder %s: %w", s.ref(dir), err)
		}
		tmp := path.Join(dir, tmpPrefix+randName())
		f, err := c.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
		if err != nil {
			return fmt.Errorf("cannot write in %s: %w", s.ref(dir), err)
		}
		done := false
		defer func() {
			if !done {
				f.Close()
				c.Remove(tmp)
			}
		}()
		h := sha1.New()
		n, err := io.Copy(f, io.TeeReader(r, h))
		if err != nil {
			return err
		}
		if size >= 0 && n != size {
			return fmt.Errorf("%d bytes arrived, %d were expected", n, size)
		}
		if err := f.Close(); err != nil {
			return err
		}
		sum := hex.EncodeToString(h.Sum(nil))
		// read back what the server holds
		if got, gn, err := s.hash(c, tmp); err != nil {
			return err
		} else if gn != n || got != sum {
			return fmt.Errorf("the server holds %d bytes of the file, and they are not the ones that were sent", gn)
		}
		final := target
		for i := 2; ; i++ {
			fi, err := c.Lstat(final)
			if errors.Is(err, fs.ErrNotExist) {
				break
			}
			if err != nil {
				return err
			}
			if fi.Mode().IsRegular() && fi.Size() == n {
				if have, _, err := s.hash(c, final); err == nil && have == sum {
					st = Stored{Ref: s.ref(final), Size: n, Sum: sum, Existed: true, Verified: "size and checksum"}
					return nil // already there (the temporary file is removed)
				}
			}
			final = suffixedRef(target, i)
			if i > 10000 {
				return errors.New("too many files with that name")
			}
		}
		if err := c.Rename(tmp, final); err != nil {
			return fmt.Errorf("cannot put the file in place: %w", err)
		}
		done = true
		st = Stored{Ref: s.ref(final), Size: n, Sum: sum, Verified: "size and checksum"}
		return nil
	})
	return st, err
}

// hash reads a file of the server and returns its SHA-1 and size.
func (s *SFTP) hash(c *sftp.Client, p string) (string, int64, error) {
	f, err := c.Open(p)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha1.New()
	n, err := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, err
}

// Stat implements Destination (the size; reading a file only to give its checksum is left to Put).
func (s *SFTP) Stat(ctx context.Context, ref string) (Info, error) {
	var info Info
	err := s.session(ctx, func(c *sftp.Client) error {
		p, err := s.full(ref)
		if err != nil {
			return err
		}
		fi, err := c.Stat(p)
		if errors.Is(err, fs.ErrNotExist) || err == nil && !fi.Mode().IsRegular() {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		info = Info{Size: fi.Size(), Modified: fi.ModTime()}
		return nil
	})
	return info, err
}

// Delete implements Destination: the file goes, and the folders it leaves empty go with it up to the folder files are put under (SFTP only
// removes a folder that is empty, so nothing else can be lost).
func (s *SFTP) Delete(ctx context.Context, ref string) error {
	return s.session(ctx, func(c *sftp.Client) error {
		p, err := s.full(ref)
		if err != nil {
			return err
		}
		if err := c.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		for dir := path.Dir(p); dir != s.base && strings.HasPrefix(dir, s.base+"/"); dir = path.Dir(dir) {
			if c.RemoveDirectory(dir) != nil {
				break
			}
		}
		return nil
	})
}

// List implements Destination.
func (s *SFTP) List(ctx context.Context, prefix string) ([]Entry, error) {
	var out []Entry
	err := s.session(ctx, func(c *sftp.Client) error {
		start := s.base
		if p := strings.Trim(strings.ReplaceAll(prefix, "\\", "/"), "/"); p != "" {
			start = path.Join(s.base, path.Clean("/"+p))
		}
		w := c.Walk(start)
		for w.Step() {
			if err := w.Err(); err != nil {
				if errors.Is(err, fs.ErrNotExist) && w.Path() == start {
					return nil
				}
				return err
			}
			if cerr := ctx.Err(); cerr != nil {
				return cerr
			}
			fi := w.Stat()
			if fi == nil || !fi.Mode().IsRegular() || strings.HasPrefix(fi.Name(), tmpPrefix) {
				continue
			}
			out = append(out, Entry{Ref: s.ref(w.Path()), Size: fi.Size(), Modified: fi.ModTime()})
			if len(out) > ListLimit {
				return ErrTooManyFiles
			}
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Ref < out[j].Ref })
	return out, err
}

// Open implements Destination. The session is held, and nothing else is done with this destination, until the reader is closed.
func (s *SFTP) Open(ctx context.Context, ref string) (io.ReadCloser, error) {
	s.mu.Lock()
	c, err := s.ready(ctx)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	p, err := s.full(ref)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	f, err := c.Open(p)
	if err != nil {
		s.mu.Unlock()
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &heldFile{f: f, unlock: s.mu.Unlock}, nil
}

type heldFile struct {
	f      *sftp.File
	unlock func()
	once   sync.Once
}

func (h *heldFile) Read(p []byte) (int, error) { return h.f.Read(p) }
func (h *heldFile) Close() error {
	err := h.f.Close()
	h.once.Do(h.unlock)
	return err
}

// Check implements Destination: the server answers, its key is the pinned one, the login is accepted, the folder exists, and the app can
// write a small file there and remove it.
func (s *SFTP) Check(ctx context.Context) (string, error) {
	err := s.session(ctx, func(c *sftp.Client) error {
		test := path.Join(s.base, tmpPrefix+"check-"+randName())
		f, err := c.OpenFile(test, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
		if err != nil {
			return fmt.Errorf("the app cannot write in %s on the SFTP server: %w", s.base, err)
		}
		_, werr := f.Write([]byte(time.Now().UTC().Format(time.RFC3339)))
		f.Close()
		if err := c.Remove(test); err != nil {
			return fmt.Errorf("the app wrote a test file but could not remove it: %w", err)
		}
		return werr
	})
	if err != nil {
		return "", err
	}
	return "the SFTP server answers with the trusted key, the login is accepted and the app can write in " + s.base, nil
}
