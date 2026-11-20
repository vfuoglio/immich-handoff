package backup

import (
	"bufio"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/vfuoglio/immich-handoff/internal/vault"
)

// An encrypted backup is the data file sealed as a whole, so names, email addresses and settings are as private as the
// keys in it. The file is: a magic line, a random salt, then the data in chunks of 64 KiB, each sealed with AES-256-GCM
// under a key that Argon2id makes from the store key and the salt. A chunk's nonce is its number, and the last chunk is
// marked, so a file cannot be cut short, reordered or have chunks taken out without Decrypt noticing.
const (
	magic     = "IMMICH-HANDOFF-BACKUP-1\n"
	saltSize  = 16
	chunkSize = 64 << 10
	purpose   = "backup file"
)

// ErrNotEncrypted: the file is a plain data file (Decrypt was asked for one that is not encrypted).
var ErrNotEncrypted = errors.New("backup: the file is not encrypted")

// IsEncrypted reports whether the start of a file is the magic line of an encrypted backup.
func IsEncrypted(head []byte) bool { return bytes.HasPrefix(head, []byte(magic)) }

func chunkNonce(n uint64, last bool) []byte {
	nonce := make([]byte, 12)
	binary.BigEndian.PutUint64(nonce[3:], n)
	if last {
		nonce[0] = 1
	}
	return nonce
}

func aeadFor(v *vault.Vault, salt []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(v.DeriveKey(purpose, salt))
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Encrypt writes src to dst, sealed with the store key.
func Encrypt(dst io.Writer, src io.Reader, v *vault.Vault) error {
	salt := make([]byte, saltSize)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	aead, err := aeadFor(v, salt)
	if err != nil {
		return err
	}
	header := append([]byte(magic), salt...)
	if _, err := dst.Write(header); err != nil {
		return err
	}
	br, buf := bufio.NewReaderSize(src, chunkSize+1), make([]byte, chunkSize)
	for n := uint64(0); ; n++ {
		have, err := io.ReadFull(br, buf)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return err
		}
		last, err := lastChunk(br, err)
		if err != nil {
			return err
		}
		if _, err := dst.Write(aead.Seal(nil, chunkNonce(n, last), buf[:have], header)); err != nil {
			return err
		}
		if last {
			return nil
		}
	}
}

// lastChunk: after a read of a chunk, is it the last? It is when the read came up short, or nothing follows it.
func lastChunk(br *bufio.Reader, readErr error) (bool, error) {
	if readErr != nil {
		return true, nil
	}
	if _, err := br.Peek(1); err == io.EOF {
		return true, nil
	} else if err != nil {
		return false, err
	}
	return false, nil
}

// Decrypt writes the data of an encrypted backup to dst. It fails when the store key is not the one it was sealed
// with, and when the file was changed or is incomplete.
func Decrypt(dst io.Writer, src io.Reader, v *vault.Vault) error {
	header := make([]byte, len(magic)+saltSize)
	if _, err := io.ReadFull(src, header); err != nil || !IsEncrypted(header) {
		return ErrNotEncrypted
	}
	aead, err := aeadFor(v, header[len(magic):])
	if err != nil {
		return err
	}
	sealed := chunkSize + aead.Overhead()
	br, buf := bufio.NewReaderSize(src, sealed+1), make([]byte, sealed)
	for n := uint64(0); ; n++ {
		have, err := io.ReadFull(br, buf)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return err
		}
		last, err := lastChunk(br, err)
		if err != nil {
			return err
		}
		plain, oerr := aead.Open(nil, chunkNonce(n, last), buf[:have], header)
		if oerr != nil {
			return errors.New("backup: cannot decrypt (wrong store key, or the file was changed or cut short)")
		}
		if _, err := dst.Write(plain); err != nil {
			return err
		}
		if last {
			return nil
		}
	}
}

// Encrypted wraps a Source: it makes the plain copy beside the destination, seals it into the destination and removes
// the plain one.
type Encrypted struct {
	Source Source
	Vault  *vault.Vault
}

func (e Encrypted) Backup(ctx context.Context, dest string) error {
	plain := dest + ".plain"
	defer os.Remove(plain)
	if err := e.Source.Backup(ctx, plain); err != nil {
		return err
	}
	in, err := os.Open(plain)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := Encrypt(out, in, e.Vault); err != nil {
		out.Close()
		os.Remove(dest)
		return fmt.Errorf("backup: sealing the copy: %w", err)
	}
	return out.Close()
}
