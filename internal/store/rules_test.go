package store

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestARuleThatFollowsAnAlbumKeepsItsKindAndWhereCopiesGo(t *testing.T) {
	st, err := open(t, t.TempDir(), "library-rule-secret-1234")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	owner := uuid.New()
	plain, err := st.AddRule(ctx, Rule{OwnerID: owner, Service: "family", Cond: RuleCond{Phrase: "dog"}})
	if err != nil || plain.Kind != RuleKindSend || plain.Dest != "" {
		t.Fatalf("a rule that sends stays what it was: %+v %v", plain, err)
	}
	follow, err := st.AddRule(ctx, Rule{OwnerID: owner, Kind: RuleKindLibrary, Dest: "album", DestAlbum: "From Anna",
		Cond: RuleCond{Album: &RuleRef{ID: uuid.NewString(), Name: "Trip"}, AlbumOwner: "Anna"}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.RuleByID(ctx, follow.ID)
	if err != nil || got.Kind != RuleKindLibrary || got.Dest != "album" || got.DestAlbum != "From Anna" || got.Cond.AlbumOwner != "Anna" || got.Cond.Album.Name != "Trip" || got.Service != "" {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	all, _ := st.RulesOf(ctx, owner)
	if len(all) != 2 || all[0].Kind != RuleKindSend || all[1].Kind != RuleKindLibrary {
		t.Errorf("both kinds are listed, oldest first: %+v", all)
	}
	if err := st.PutRulePhoto(ctx, follow.ID, "a", RuleSeen, "in the album when the rule began"); err != nil {
		t.Fatal(err)
	}
	if states, _ := st.RulePhotoStates(ctx, follow.ID); states["a"] != RuleSeen {
		t.Errorf("a photo that was there already is remembered, not offered: %v", states)
	}
}
