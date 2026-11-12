package store

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestADigestRoundTripsAndOnlyEnabledOnesAreListed(t *testing.T) {
	st, err := open(t, t.TempDir(), "digest-secret-key-1234")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if _, ok, _ := st.DigestFor(ctx, "feed"); ok {
		t.Fatal("a digest nobody made")
	}
	made := time.Now().Truncate(time.Second)
	d := Digest{Service: "feed", Enabled: true, Added: "asset.view,sharedLink.create", Week: "2026-11-09", MadeAt: made, Album: "feed week of 2026-11-02",
		LinkID: "l1", LinkKey: "k1", LinkEnds: made.Add(30 * 24 * time.Hour), Photos: 12, People: 3, LeftOut: 5, LeftJSON: `[{"n":"x.jpg","r":"dark"}]`, Note: "n"}
	if err := st.SetDigest(ctx, d); err != nil {
		t.Fatal(err)
	}
	got, ok, err := st.DigestFor(ctx, "feed")
	if err != nil || !ok || got.Added != d.Added || got.Photos != 12 || got.People != 3 || got.LeftOut != 5 || got.LeftJSON != d.LeftJSON || !got.MadeAt.Equal(made) || !got.LinkEnds.Equal(d.LinkEnds) || !got.Enabled {
		t.Fatalf("%+v %v %v", got, ok, err)
	}
	st.SetDigest(ctx, Digest{Service: "club", Enabled: false})
	d.Photos = 7
	st.SetDigest(ctx, d) // the same service again replaces it
	on, _ := st.DigestsEnabled(ctx)
	if len(on) != 1 || on[0].Service != "feed" || on[0].Photos != 7 {
		t.Errorf("%+v", on)
	}
	st.DeleteDigest(ctx, "feed")
	if _, ok, _ := st.DigestFor(ctx, "feed"); ok {
		t.Error("still there")
	}
}

func TestTheLedgerListsWhatArrivedInTheWeekWithoutClipsRemoteCopiesOrTakenBackOnes(t *testing.T) {
	st, err := open(t, t.TempDir(), "digest-secret-key-1234")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	a, b := uuid.New(), uuid.New()
	rcv := []uuid.UUID{uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()}
	st.AddHandoverItem(ctx, "link-a", "b1", uuid.New(), rcv[0], false, false)
	st.AddHandoverItem(ctx, "link-a", "b1", uuid.New(), rcv[1], true, false)  // a motion clip
	st.AddHandoverItem(ctx, "link-b", "b2", uuid.New(), rcv[2], false, true)  // on another server
	st.AddHandoverItem(ctx, "link-b", "b2", uuid.New(), rcv[3], false, false) // taken back below
	st.AddHandoverItem(ctx, "link-b", "b3", uuid.New(), rcv[4], false, false)
	st.db.Exec(`UPDATE handover_items SET taken_back = 1 WHERE receiver_asset = ?`, rcv[3].String())
	_, _ = a, b
	got, err := st.HandoverItemsSince(ctx, []string{"link-a", "link-b"}, time.Now().Add(-time.Hour))
	if err != nil || len(got) != 2 || got[0].ReceiverAsset != rcv[0].String() || got[1].ReceiverAsset != rcv[4].String() || got[1].LinkID != "link-b" {
		t.Fatalf("%+v %v", got, err)
	}
	if got, _ := st.HandoverItemsSince(ctx, []string{"link-a"}, time.Now().Add(time.Hour)); len(got) != 0 {
		t.Errorf("nothing is newer than the future: %+v", got)
	}
	if got, _ := st.HandoverItemsSince(ctx, []string{"link-c"}, time.Now().Add(-time.Hour)); len(got) != 0 {
		t.Errorf("another link: %+v", got)
	}
}
