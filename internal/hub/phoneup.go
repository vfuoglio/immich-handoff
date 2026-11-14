package hub

import (
	"context"
	"crypto/rand"
	"crypto/sha1" // #nosec G505 -- Immich names a file by its SHA-1; this only compares with what the phone computed
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/immich"
)

// What a person's phone sends photos with: in chunks, so a lost connection costs a chunk and not a video. Immich has no
// resumable upload of its own, so the pieces wait here, in the data folder, until the file is whole; then it goes to the
// person's own Immich with their own session, tagged for the service they chose, and the pieces are deleted.

const (
	// PhoneChunk is the size of a piece the page is told to send.
	PhoneChunk = 8 << 20
	// phoneMaxChunk is the most one request may carry.
	phoneMaxChunk = 16 << 20
	// phoneMaxOpen is how many unfinished files one person may have at a time.
	phoneMaxOpen = 200
	// phoneDoneKeep is how long the answer for a finished file is kept, for a phone that never heard it.
	phoneDoneKeep = time.Hour
)

var (
	ErrPhoneOff        = errors.New("sending photos from a phone needs the app's data folder, which this install does not have")
	ErrPhoneTooBig     = errors.New("that file is larger than this server accepts")
	ErrPhoneFull       = errors.New("too many files are waiting to be sent; let them finish or cancel some first")
	ErrPhoneNoUpload   = errors.New("that upload is not there any more; it may have waited too long")
	ErrPhoneOffset     = errors.New("that piece does not follow the ones already received")
	ErrPhoneChunk      = errors.New("that piece is larger than allowed or goes past the end of the file")
	ErrPhoneIncomplete = errors.New("the file has not arrived completely yet")
	ErrPhoneKind       = errors.New("only photos and videos can be sent")
	ErrPhoneChecksum   = errors.New("the file arrived changed; send it again")
	ErrPhoneEmpty      = errors.New("that file is empty")
)

// PhoneOffsetError carries where the server is, for a piece that did not follow.
type PhoneOffsetError struct{ At int64 }

func (e *PhoneOffsetError) Error() string   { return ErrPhoneOffset.Error() }
func (e *PhoneOffsetError) Is(t error) bool { return t == ErrPhoneOffset }

// PhoneUpload is one file on its way.
type PhoneUpload struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	Service string    `json:"service"` // the service it is for; empty: the person's library only
	At      time.Time `json:"at"`      // when the photo was taken, as the phone says
	Made    time.Time `json:"made"`
	Done    string    `json:"done,omitempty"` // the photo's id in Immich once it is stored
	Dup     bool      `json:"dup,omitempty"`
}

func (h *Hub) phoneCap() int64 {
	if h.PhoneCap > 0 {
		return h.PhoneCap
	}
	return 4 << 30
}
func (h *Hub) phoneMaxFile() int64 {
	if h.PhoneMaxFile > 0 {
		return h.PhoneMaxFile
	}
	return 2 << 30
}
func (h *Hub) phoneKeep() time.Duration {
	if h.PhoneKeep > 0 {
		return h.PhoneKeep
	}
	return 48 * time.Hour
}

// PhoneOn is whether phones may send photos here: it needs the data folder.
func (h *Hub) PhoneOn() bool { return h.PhoneDir != "" }

// PhoneLimits are what the page tells its person: the most for a file, and for all that waits.
func (h *Hub) PhoneLimits() (file, waiting int64, keep time.Duration) {
	return h.phoneMaxFile(), h.phoneCap(), h.phoneKeep()
}

var phoneID = regexp.MustCompile(`^[0-9a-f]{32}$`)

func (h *Hub) phoneDir(user uuid.UUID) string { return filepath.Join(h.PhoneDir, user.String()) }

func (h *Hub) phonePaths(user uuid.UUID, id string) (meta, part string, err error) {
	if !h.PhoneOn() {
		return "", "", ErrPhoneOff
	}
	if !phoneID.MatchString(id) {
		return "", "", ErrPhoneNoUpload
	}
	d := h.phoneDir(user)
	return filepath.Join(d, id+".json"), filepath.Join(d, id+".part"), nil
}

func (h *Hub) phoneLock(id string) *sync.Mutex {
	m, _ := h.phoneLocks.LoadOrStore(id, &sync.Mutex{})
	return m.(*sync.Mutex)
}

func (h *Hub) phoneRead(user uuid.UUID, id string) (PhoneUpload, string, error) {
	meta, part, err := h.phonePaths(user, id)
	if err != nil {
		return PhoneUpload{}, "", err
	}
	b, err := os.ReadFile(meta)
	if err != nil {
		return PhoneUpload{}, "", ErrPhoneNoUpload
	}
	var u PhoneUpload
	if json.Unmarshal(b, &u) != nil || u.ID != id {
		return PhoneUpload{}, "", ErrPhoneNoUpload
	}
	return u, part, nil
}

func (h *Hub) phoneWrite(user uuid.UUID, u PhoneUpload) error {
	meta, _, err := h.phonePaths(user, u.ID)
	if err != nil {
		return err
	}
	b, _ := json.Marshal(u)
	tmp := meta + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, meta)
}

func partSize(part string) int64 {
	if st, err := os.Stat(part); err == nil {
		return st.Size()
	}
	return 0
}

// phoneWaiting is the room the person's unfinished files have reserved (what each one will be, once whole) and how many
// there are.
func (h *Hub) phoneWaiting(user uuid.UUID) (reserved int64, open int) {
	entries, _ := os.ReadDir(h.phoneDir(user))
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		u, _, err := h.phoneRead(user, strings.TrimSuffix(e.Name(), ".json"))
		if err != nil || u.Done != "" {
			continue
		}
		reserved += u.Size
		open++
	}
	return
}

// PhoneStart begins a file: it checks the limits and returns the upload the pieces are sent to.
func (h *Hub) PhoneStart(user uuid.UUID, name string, size int64, service string, at time.Time) (PhoneUpload, error) {
	if !h.PhoneOn() {
		return PhoneUpload{}, ErrPhoneOff
	}
	switch {
	case size <= 0:
		return PhoneUpload{}, ErrPhoneEmpty
	case size > h.phoneMaxFile():
		return PhoneUpload{}, fmt.Errorf("%w (%d MB at most)", ErrPhoneTooBig, h.phoneMaxFile()>>20)
	}
	if err := os.MkdirAll(h.phoneDir(user), 0o700); err != nil {
		return PhoneUpload{}, err
	}
	h.phoneStartMu.Lock()
	defer h.phoneStartMu.Unlock()
	reserved, open := h.phoneWaiting(user)
	if open >= phoneMaxOpen {
		return PhoneUpload{}, ErrPhoneFull
	}
	if reserved+size > h.phoneCap() {
		return PhoneUpload{}, fmt.Errorf("%w (%d MB are allowed to wait at a time)", ErrPhoneFull, h.phoneCap()>>20)
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return PhoneUpload{}, err
	}
	now := h.now()
	if at.IsZero() || at.After(now.Add(24*time.Hour)) {
		at = now
	}
	u := PhoneUpload{ID: hex.EncodeToString(b), Name: name, Size: size, Service: service, At: at, Made: now}
	if err := h.phoneWrite(user, u); err != nil {
		return PhoneUpload{}, err
	}
	return u, nil
}

// PhoneStatus says how much of a file has arrived.
func (h *Hub) PhoneStatus(user uuid.UUID, id string) (PhoneUpload, int64, error) {
	u, part, err := h.phoneRead(user, id)
	if err != nil {
		return PhoneUpload{}, 0, err
	}
	if u.Done != "" {
		return u, u.Size, nil
	}
	return u, partSize(part), nil
}

// PhoneChunk adds the piece that starts at offset. A piece must follow the ones received; when it does not, the error says
// where the server is, so the phone carries on from there. A piece cut off in the middle keeps what arrived of it.
func (h *Hub) PhoneChunk(user uuid.UUID, id string, offset int64, r io.Reader) (int64, error) {
	u, part, err := h.phoneRead(user, id)
	if err != nil {
		return 0, err
	}
	m := h.phoneLock(id)
	m.Lock()
	defer m.Unlock()
	have := partSize(part)
	if u.Done != "" {
		return u.Size, nil
	}
	if offset != have {
		return have, &PhoneOffsetError{At: have}
	}
	f, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return have, err
	}
	n, err := io.Copy(f, io.LimitReader(r, phoneMaxChunk+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if n > phoneMaxChunk || have+n > u.Size {
		os.Truncate(part, have)
		return have, ErrPhoneChunk
	}
	if n > 0 {
		now := h.now()
		os.Chtimes(part, now, now)
	}
	return have + n, err
}

// PhoneCancel throws a file away.
func (h *Hub) PhoneCancel(user uuid.UUID, id string) error {
	meta, part, err := h.phonePaths(user, id)
	if err != nil {
		return err
	}
	m := h.phoneLock(id)
	m.Lock()
	defer m.Unlock()
	os.Remove(part)
	os.Remove(meta)
	h.phoneLocks.Delete(id)
	return nil
}

// PhoneFinish sends a whole file to the person's own Immich, tags it with tag (empty: only into the library) and deletes
// the pieces. A phone that asks again for a file that is done gets the same answer. sum, when given, is the file's SHA-1
// in hex; a file that does not match is dropped so the phone sends it again.
func (h *Hub) PhoneFinish(ctx context.Context, sess *immich.API, user uuid.UUID, id, tag, sum string) (photo string, duplicate bool, err error) {
	u, part, err := h.phoneRead(user, id)
	if err != nil {
		return "", false, err
	}
	m := h.phoneLock(id)
	m.Lock()
	defer m.Unlock()
	if u.Done != "" {
		return u.Done, u.Dup, nil
	}
	if have := partSize(part); have != u.Size {
		return "", false, &PhoneOffsetError{At: have}
	}
	f, err := os.Open(part)
	if err != nil {
		return "", false, ErrPhoneNoUpload
	}
	defer f.Close()
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	if kind, _ := sniffDrop(head[:n]); kind == "" {
		os.Remove(part)
		os.Remove(strings.TrimSuffix(part, ".part") + ".json")
		return "", false, ErrPhoneKind
	}
	_, ext := sniffDrop(head[:n])
	if sum != "" {
		f.Seek(0, io.SeekStart)
		hash := sha1.New() // #nosec G401
		if _, err := io.Copy(hash, f); err != nil {
			return "", false, err
		}
		if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), strings.TrimSpace(sum)) {
			os.Remove(part)
			return "", false, ErrPhoneChecksum
		}
	}
	f.Seek(0, io.SeekStart)
	uid, dup, err := sess.UploadFile(ctx, dropFileName(u.Name, ext), f, u.At)
	if err != nil {
		return "", false, err
	}
	photo = uid.String()
	if tag != "" {
		t, err := sess.UpsertTag(ctx, tag)
		if err != nil {
			return "", false, fmt.Errorf("the photo is in your library, but it could not be tagged: %w", err)
		}
		if err := sess.TagAssets(ctx, t, uid); err != nil {
			return "", false, fmt.Errorf("the photo is in your library, but it could not be tagged: %w", err)
		}
	}
	os.Remove(part)
	u.Done, u.Dup = photo, dup
	if err := h.phoneWrite(user, u); err != nil {
		return photo, dup, nil // stored; only the answer for a repeat is lost
	}
	return photo, dup, nil
}

// PhoneMaintenance deletes files nobody finished (after the keep time) and the answers for finished ones, and returns how
// many unfinished files it removed.
func (h *Hub) PhoneMaintenance() int {
	if !h.PhoneOn() {
		return 0
	}
	now, removed := h.now(), 0
	people, _ := os.ReadDir(h.PhoneDir)
	for _, p := range people {
		dir := filepath.Join(h.PhoneDir, p.Name())
		files, _ := os.ReadDir(dir)
		for _, e := range files {
			name := e.Name()
			if !strings.HasSuffix(name, ".json") {
				if strings.HasSuffix(name, ".part") { // pieces whose record is gone
					if _, err := os.Stat(filepath.Join(dir, strings.TrimSuffix(name, ".part")+".json")); err != nil {
						os.Remove(filepath.Join(dir, name))
					}
				}
				continue
			}
			id := strings.TrimSuffix(name, ".json")
			st, err := e.Info()
			if err != nil {
				continue
			}
			last := st.ModTime()
			if ps, err := os.Stat(filepath.Join(dir, id+".part")); err == nil && ps.ModTime().After(last) {
				last = ps.ModTime()
			}
			b, _ := os.ReadFile(filepath.Join(dir, name))
			var u PhoneUpload
			json.Unmarshal(b, &u)
			age := now.Sub(last)
			switch {
			case u.Done != "" && age > phoneDoneKeep:
			case u.Done == "" && age > h.phoneKeep():
				removed++
			default:
				continue
			}
			os.Remove(filepath.Join(dir, id+".part"))
			os.Remove(filepath.Join(dir, name))
			h.phoneLocks.Delete(id)
		}
		os.Remove(dir) // only succeeds when empty
	}
	return removed
}
