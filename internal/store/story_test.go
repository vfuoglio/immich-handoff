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

func TestAStoryIsKeptWithItsKeySealedAndFoundByTokenOnlyWhenReady(t *testing.T) {
	dir := t.TempDir()
	st, err := open(t, dir, "story-secret-1234567890")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	owner := uuid.New()
	s, err := st.AddStory(ctx, Story{Token: "tok-1", OwnerID: owner, OwnerName: "Ana", Title: "Summer", Kind: StoryAlbum, SourceID: uuid.NewString(), SourceName: "Beach",
		ShowDates: true, ShowPlaces: true, KeyID: uuid.New(), Key: "a-very-secret-story-key"})
	if err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "handoff.db")); err == nil && bytes.Contains(raw, []byte("a-very-secret-story-key")) {
		t.Error("the key must not be in the database file in the clear")
	}
	if _, err := st.StoryByToken(ctx, "tok-1"); err != ErrNotFound {
		t.Fatalf("a story still being built has no public page: %v", err)
	}
	picks := []StoryPick{{Asset: "a1", Chapter: 0, Date: "2026-07-01", Place: "Lisbon, Portugal", People: []string{"Nora"}}, {Asset: "a2", Chapter: 1, Date: "2026-08-02"}}
	chapters := []StoryChapter{{Title: "July", From: "2026-07-01", To: "2026-07-31"}, {Title: "August"}}
	if err := st.SetStoryBuilt(ctx, s.ID, StoryReady, "", picks, chapters); err != nil {
		t.Fatal(err)
	}
	got, err := st.StoryByToken(ctx, "tok-1")
	if err != nil || got.Key != "a-very-secret-story-key" || len(got.Picks) != 2 || got.Picks[0].Place != "Lisbon, Portugal" || got.Picks[0].People[0] != "Nora" || got.Chapters[1].Title != "August" || !got.ShowDates || got.ShowPeople {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	if _, err := st.StoryByID(ctx, uuid.New(), s.ID); err != ErrNotFound {
		t.Fatalf("somebody else's story: %v", err)
	}
	got.Title, got.Preview, got.ShowPeople = "Our summer", true, true
	got.Picks = got.Picks[:1]
	if err := st.UpdateStory(ctx, got); err != nil {
		t.Fatal(err)
	}
	if again, _ := st.StoryByID(ctx, owner, s.ID); again.Title != "Our summer" || !again.Preview || !again.ShowPeople || len(again.Picks) != 1 {
		t.Fatalf("an edit is kept: %+v", again)
	}
	if n, _ := st.CountStories(ctx, owner); n != 1 {
		t.Fatalf("count %d", n)
	}
	got.EndsAt = time.Now().Add(-time.Minute)
	st.UpdateStory(ctx, got)
	if n, err := st.EndStoriesDue(ctx, time.Now()); err != nil || n != 1 {
		t.Fatalf("one story is due: %d %v", n, err)
	}
	if _, err := st.StoryByToken(ctx, "tok-1"); err != ErrNotFound {
		t.Fatalf("an ended story has no public page: %v", err)
	}
	if err := st.DeleteStory(ctx, owner, s.ID); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.CountStories(ctx, owner); n != 0 {
		t.Fatal("deleted")
	}
}

func TestTheEndOfYearOfferIsRememberedPerPersonAndYear(t *testing.T) {
	st, err := open(t, t.TempDir(), "story-offer-secret-12345")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	u := uuid.New()
	if st.StoryOffer(ctx, u, 2026) != "" {
		t.Fatal("not offered yet")
	}
	st.SetStoryOffer(ctx, u, 2026, "dismissed")
	if st.StoryOffer(ctx, u, 2026) != "dismissed" || st.StoryOffer(ctx, u, 2025) != "" || st.StoryOffer(ctx, uuid.New(), 2026) != "" {
		t.Fatal("per person and per year")
	}
}
