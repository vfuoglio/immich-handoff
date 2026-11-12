package store

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestABoxIsFoundByItsMailCodeUntilItIsRemovedOrRevoked(t *testing.T) {
	st, err := open(t, t.TempDir(), "mailcode-secret-1234")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	mk := func(token string) DropBox {
		b, err := st.AddDropBox(ctx, DropBox{Token: token, Name: token, OwnerID: uuid.New(), KeyID: uuid.New(), Key: "k", InboxTag: "inbox/" + token})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	a, b := mk("alpha-token-0001"), mk("beta-token-00002")
	if _, err := st.DropBoxByMailCode(ctx, "abcdefghij"); err != ErrDropBoxGone {
		t.Errorf("a code nobody has: %v", err)
	}
	if _, err := st.DropBoxByMailCode(ctx, ""); err != ErrDropBoxGone {
		t.Errorf("an empty code finds nothing, however many boxes have none: %v", err)
	}
	if err := st.SetDropMailCode(ctx, a.ID, "abcdefghij"); err != nil {
		t.Fatal(err)
	}
	got, err := st.DropBoxByMailCode(ctx, "abcdefghij")
	if err != nil || got.ID != a.ID || got.MailCode != "abcdefghij" {
		t.Fatalf("%+v %v", got, err)
	}
	if err := st.SetDropMailCode(ctx, b.ID, "abcdefghij"); err != ErrExists {
		t.Errorf("two boxes with one code: %v", err)
	}
	if err := st.SetDropMailCode(ctx, b.ID, ""); err != nil { // many boxes may have no code at all
		t.Fatal(err)
	}
	if _, err := st.SetDropBoxState(ctx, a.ID, DropRevoked, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DropBoxByMailCode(ctx, "abcdefghij"); err != ErrDropBoxGone {
		t.Errorf("a revoked box: %v", err)
	}
}
