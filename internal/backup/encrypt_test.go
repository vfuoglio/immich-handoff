package backup

import (
	"bytes"
	"crypto/rand"
	"strings"
	"testing"

	"github.com/vfuoglio/immich-handoff/internal/vault"
)

func TestEncryptedBackupRoundTripsAndNoticesDamage(t *testing.T) {
	v, _ := vault.New("backup-test-store-key-1")
	for _, size := range []int{0, 1, chunkSize - 1, chunkSize, chunkSize + 1, 3*chunkSize + 17} {
		plain := make([]byte, size)
		rand.Read(plain)
		var enc, back bytes.Buffer
		if err := Encrypt(&enc, bytes.NewReader(plain), v); err != nil {
			t.Fatalf("%d: %v", size, err)
		}
		if size > 16 && bytes.Contains(enc.Bytes(), plain[:16]) {
			t.Fatalf("%d: the data is in the clear", size)
		}
		if err := Decrypt(&back, bytes.NewReader(enc.Bytes()), v); err != nil || !bytes.Equal(back.Bytes(), plain) {
			t.Fatalf("%d: round trip: %v", size, err)
		}
		if size > 2*chunkSize { // the end cut off, and a flipped byte
			if err := Decrypt(&bytes.Buffer{}, bytes.NewReader(enc.Bytes()[:enc.Len()-chunkSize/2]), v); err == nil {
				t.Errorf("%d: a cut file was accepted", size)
			}
			bad := append([]byte(nil), enc.Bytes()...)
			bad[len(bad)/2] ^= 1
			if err := Decrypt(&bytes.Buffer{}, bytes.NewReader(bad), v); err == nil {
				t.Errorf("%d: a changed file was accepted", size)
			}
		}
	}
	var enc bytes.Buffer
	Encrypt(&enc, strings.NewReader("hello"), v)
	other, _ := vault.New("another-store-key-0123456")
	if err := Decrypt(&bytes.Buffer{}, bytes.NewReader(enc.Bytes()), other); err == nil {
		t.Error("another store key opened the file")
	}
	if err := Decrypt(&bytes.Buffer{}, strings.NewReader("SQLite format 3\x00 ......................"), v); err != ErrNotEncrypted {
		t.Errorf("a plain file = %v, want ErrNotEncrypted", err)
	}
}
