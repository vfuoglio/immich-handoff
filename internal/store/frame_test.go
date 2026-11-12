package store

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestAFrameIsStoredWithItsKeySealedAndFoundByItsLinkWhileItIsActive(t *testing.T) {
	dir, ctx := t.TempDir(), context.Background()
	st, err := open(t, dir, "frame-secret-key-12345")
	if err != nil {
		t.Fatal(err)
	}
	owner, other := uuid.New(), uuid.New()
	f, err := st.AddFrame(ctx, Frame{Token: "tok-1", OwnerID: owner, OwnerName: "Ann", Name: "Living room", AlbumID: uuid.New(), AlbumName: "Holiday", KeyID: uuid.New(), Key: "SECRET-FRAME-KEY", Hold: 30})
	if err != nil || f.ID == "" || f.State != FrameActive {
		t.Fatalf("%+v %v", f, err)
	}
	got, err := st.FrameByToken(ctx, "tok-1")
	if err != nil || got.Key != "SECRET-FRAME-KEY" || got.Hold != 30 || got.AlbumName != "Holiday" || !got.EndsAt.IsZero() {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := st.FrameByToken(ctx, "nope"); err != ErrNotFound {
		t.Errorf("%v", err)
	}
	if mine, _ := st.FramesOf(ctx, owner); len(mine) != 1 {
		t.Errorf("%+v", mine)
	}
	if theirs, _ := st.FramesOf(ctx, other); len(theirs) != 0 {
		t.Errorf("%+v", theirs)
	}
	if _, err := st.FrameByID(ctx, other, f.ID); err != ErrNotFound {
		t.Errorf("another person found it by id: %v", err)
	}
	if n, _ := st.CountFrames(ctx, owner); n != 1 {
		t.Errorf("%d", n)
	}
	st.Close()
	raw, _ := os.ReadFile(filepath.Join(dir, "handoff.db"))
	wal, _ := os.ReadFile(filepath.Join(dir, "handoff.db-wal"))
	if bytes.Contains(raw, []byte("SECRET-FRAME-KEY")) || bytes.Contains(wal, []byte("SECRET-FRAME-KEY")) {
		t.Error("the frame's key is in the file in the clear")
	}
}

func TestFramesPastTheirDateEndAndAnEndedOneIsNotFoundByItsLink(t *testing.T) {
	st, err := open(t, t.TempDir(), "frame-secret-key-12345")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx, owner := context.Background(), uuid.New()
	now := time.Now()
	mk := func(token string, ends time.Time) Frame {
		f, err := st.AddFrame(ctx, Frame{Token: token, OwnerID: owner, Name: token, AlbumID: uuid.New(), KeyID: uuid.New(), Key: "k", Hold: 15, EndsAt: ends})
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	mk("forever", time.Time{})
	mk("later", now.Add(time.Hour))
	past := mk("past", now.Add(-time.Hour))
	if n, err := st.EndFramesDue(ctx, now); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	if n, _ := st.EndFramesDue(ctx, now); n != 0 {
		t.Errorf("ended twice: %d", n)
	}
	if _, err := st.FrameByToken(ctx, "past"); err != ErrNotFound {
		t.Errorf("an ended frame's link: %v", err)
	}
	for _, token := range []string{"forever", "later"} {
		if _, err := st.FrameByToken(ctx, token); err != nil {
			t.Errorf("%s: %v", token, err)
		}
	}
	list, _ := st.FramesOf(ctx, owner)
	ended := 0
	for _, f := range list {
		if f.State == FrameEnded {
			ended++
		}
	}
	if len(list) != 3 || ended != 1 {
		t.Errorf("the owner still lists it, ended: %+v", list)
	}
	if err := st.DeleteFrame(ctx, past.ID); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.CountFrames(ctx, owner); n != 2 {
		t.Errorf("%d", n)
	}
}
