package dest

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

// Memory is a Destination that keeps its files in memory, for tests of everything that uses one. Fail makes Put, Delete and Check
// answer with that error, as a destination that is down does.
type Memory struct {
	mu    sync.Mutex
	files map[string][]byte
	Fail  error
	// Puts counts the writes that were made (not the ones skipped as already there).
	Puts int
}

func (m *Memory) Put(_ context.Context, name string, r io.Reader, size int64) (Stored, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Fail != nil {
		return Stored{}, m.Fail
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return Stored{}, err
	}
	if size >= 0 && int64(len(b)) != size {
		return Stored{}, fmt.Errorf("%d bytes arrived, %d were expected", len(b), size)
	}
	sum := sha1.Sum(b)
	st := Stored{Ref: name, Size: int64(len(b)), Sum: hex.EncodeToString(sum[:])}
	if m.files == nil {
		m.files = map[string][]byte{}
	}
	if old, ok := m.files[name]; ok {
		if bytes.Equal(old, b) {
			st.Existed = true
			return st, nil
		}
		for i := 2; ; i++ { // a different file under the name: a suffix
			ref := suffixed(name, i)
			if _, taken := m.files[ref]; !taken {
				st.Ref = ref
				break
			}
		}
	}
	m.files[st.Ref] = b
	m.Puts++
	return st, nil
}

func suffixed(name string, n int) string {
	if i := strings.LastIndex(name, "."); i > strings.LastIndex(name, "/") {
		return fmt.Sprintf("%s (%d)%s", name[:i], n, name[i:])
	}
	return fmt.Sprintf("%s (%d)", name, n)
}

func (m *Memory) Stat(_ context.Context, ref string) (Info, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.files[ref]
	if !ok {
		return Info{}, ErrNotFound
	}
	sum := sha1.Sum(b)
	return Info{Size: int64(len(b)), Sum: hex.EncodeToString(sum[:]), Modified: time.Now()}, nil
}

func (m *Memory) Delete(_ context.Context, ref string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Fail != nil {
		return m.Fail
	}
	delete(m.files, ref)
	return nil
}

func (m *Memory) List(_ context.Context, prefix string) ([]Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Entry
	for ref, b := range m.files {
		if strings.HasPrefix(ref, prefix) {
			out = append(out, Entry{Ref: ref, Size: int64(len(b))})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref < out[j].Ref })
	return out, nil
}

func (m *Memory) Open(_ context.Context, ref string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.files[ref]
	if !ok {
		return nil, ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (m *Memory) Check(context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Fail != nil {
		return "", m.Fail
	}
	return "the memory destination answers", nil
}

// Refs lists what is stored, for a test to look at.
func (m *Memory) Refs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for ref := range m.files {
		out = append(out, ref)
	}
	sort.Strings(out)
	return out
}

// Content is what is stored under ref (nil when nothing is).
func (m *Memory) Content(ref string) []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.files[ref]
}
