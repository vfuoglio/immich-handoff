package store

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestAPersonsChoiceOfWhereCopiesGoIsKeptAndChanged(t *testing.T) {
	st, err := open(t, t.TempDir(), "library-prefs-secret-1234")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	u, other := uuid.New(), uuid.New()
	if _, ok, err := st.LibraryPref(ctx, u); ok || err != nil {
		t.Fatalf("nothing chosen yet: %v %v", ok, err)
	}
	if err := st.SetLibraryPref(ctx, u, LibraryPref{Dest: "album", Album: "From family"}); err != nil {
		t.Fatal(err)
	}
	if p, ok, err := st.LibraryPref(ctx, u); err != nil || !ok || p.Dest != "album" || p.Album != "From family" {
		t.Fatalf("saved: %+v %v %v", p, ok, err)
	}
	if err := st.SetLibraryPref(ctx, u, LibraryPref{Dest: "plain"}); err != nil {
		t.Fatal(err)
	}
	if p, _, _ := st.LibraryPref(ctx, u); p.Dest != "plain" || p.Album != "" {
		t.Errorf("changed: %+v", p)
	}
	if _, ok, _ := st.LibraryPref(ctx, other); ok {
		t.Error("one person's choice is not another's")
	}
}
