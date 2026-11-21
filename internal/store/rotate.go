package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/vfuoglio/immich-handoff/internal/vault"
)

// kdfSalt is the key in meta that holds the salt the store key is stretched with. The salt is not secret.
const kdfSalt = "kdf-salt"

// useSalt makes the vault seal with the salted key: it reads the database's salt, or makes one the first time. What
// was sealed before stays readable (the vault opens both forms) and is rewritten as it is changed, or all at once by
// Rotate.
func (s *Store) useSalt() error {
	if s.vault == nil {
		return nil
	}
	var salt []byte
	err := s.db.QueryRow(`SELECT v FROM meta WHERE k = ?`, kdfSalt).Scan(&salt)
	if errors.Is(err, sql.ErrNoRows) {
		if salt, err = vault.NewSalt(); err != nil {
			return err
		}
		if _, err = s.db.Exec(`INSERT INTO meta (k, v) VALUES (?, ?)`, kdfSalt, salt); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	v, err := s.vault.WithSalt(salt)
	if err != nil {
		return err
	}
	s.vault = v
	return nil
}

// sealedColumns lists every place the store keeps a sealed value: the table, the column, the column that identifies
// the row, and the text the value is bound to (what Seal was given as its context).
var sealedColumns = []struct {
	table, col, id string
	context        func(id string) string
}{
	{"links", "key_enc", "id", same},
	{"services", "key_enc", "id", same},
	{"dropboxes", "key_enc", "id", same},
	{"rooms", "key_enc", "id", same},
	{"frames", "key_enc", "id", same},
	{"peers", "secret_enc", "id", same},
	{"shared_albums", "key_enc", "id", same},
	{"bridges", "key_enc", "service", prefix("bridge:")},
	{"push_subs", "sealed", "id", prefix("push:")},
	{"library_keys", "key_enc", "user_id", prefix("library:")},
	{"memory_prefs", "key_enc", "user_id", prefix("memories:")},
	{"folder_keys", "key_enc", "user_id", prefix("folders:")},
	{"stack_keys", "key_enc", "user_id", prefix("stacks:")},
	{"album_keys", "key_enc", "user_id", prefix("albums:")},
	{"circle_keys", "key_enc", "user_id", prefix("circles:")},
	{"cast_keys", "key_enc", "user_id", prefix("cast:")},
	{"stories", "key_enc", "id", same},
	{"destinations", "secret_enc", "id", prefix("destination:")},
	{"meta", "v", "k", metaContext},
}

func same(id string) string { return id }

func prefix(p string) func(string) string { return func(id string) string { return p + id } }

// metaContext: the sealed values in meta are bound to their own name; the salt is not sealed.
func metaContext(k string) string { return k }

// Rotate seals everything again with next: another store key, a new salt, the current form. It is all or nothing.
// Rows that were never sealed (empty) are left alone. It returns how many values were rewritten.
func (s *Store) Rotate(ctx context.Context, next *vault.Vault) (int, error) {
	if s.vault == nil {
		return 0, errors.New("store: there is no store key, so nothing is sealed")
	}
	salt, err := vault.NewSalt()
	if err != nil {
		return 0, err
	}
	nv, err := next.WithSalt(salt)
	if err != nil {
		return 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	n := 0
	for _, c := range sealedColumns {
		type row struct {
			id  string
			enc []byte
		}
		var rows []row
		where := ""
		if c.table == "meta" {
			where = ` AND k <> '` + kdfSalt + `'`
		}
		q, err := tx.QueryContext(ctx, fmt.Sprintf(`SELECT %s, %s FROM %s WHERE %s IS NOT NULL%s`, c.id, c.col, c.table, c.col, where))
		if err != nil {
			return 0, fmt.Errorf("store: rotate %s: %w", c.table, err)
		}
		for q.Next() {
			var r row
			if err := q.Scan(&r.id, &r.enc); err != nil {
				q.Close()
				return 0, err
			}
			rows = append(rows, r)
		}
		q.Close()
		for _, r := range rows {
			if len(r.enc) == 0 {
				continue
			}
			plain, err := s.vault.Open(r.enc, c.context(r.id))
			if err != nil {
				return 0, fmt.Errorf("store: rotate %s %s: %w", c.table, r.id, err)
			}
			enc, err := nv.Seal(plain, c.context(r.id))
			if err != nil {
				return 0, err
			}
			if _, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET %s = ? WHERE %s = ?`, c.table, c.col, c.id), enc, r.id); err != nil {
				return 0, err
			}
			n++
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE meta SET v = ? WHERE k = ?`, salt, kdfSalt); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	s.vault = nv
	return n, nil
}
