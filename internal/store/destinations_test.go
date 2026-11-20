package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestOwnDestinationsKeepTheirSecretsSealedAndServicesBindDestinations(t *testing.T) {
	st, err := open(t, t.TempDir(), "dest-secret-0123456789")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx, owner := context.Background(), uuid.New()
	d, err := st.AddOwnDestination(ctx, OwnDestination{OwnerID: owner, Name: "Nextcloud", Kind: "webdav", Settings: map[string]string{"url": "https://cloud.example/dav"}, Secrets: map[string]string{"password": "hunter2"}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.OwnDestination(ctx, d.ID)
	if err != nil || got.Secrets["password"] != "hunter2" || got.Settings["url"] != "https://cloud.example/dav" || got.OwnerID != owner {
		t.Fatalf("%+v %v", got, err)
	}
	if list, _ := st.OwnDestinationsOf(ctx, uuid.New()); len(list) != 0 {
		t.Error("another person's list is empty")
	}
	b, err := st.AddServiceDestination(ctx, ServiceDestination{Service: "family", Ref: "own:" + d.ID, Template: "{yyyy}/{name}", CapPhotos: 50})
	if err != nil || b.State != DestOK {
		t.Fatalf("%+v %v", b, err)
	}
	if _, err := st.AddServiceDestination(ctx, ServiceDestination{Service: "family", Ref: "own:" + d.ID}); !errors.Is(err, ErrExists) {
		t.Errorf("a service uses a destination once: %v", err)
	}
	st.AddServiceDestination(ctx, ServiceDestination{Service: "family", Ref: "admin:nas"})
	if list, _ := st.ServiceDestinations(ctx, "family"); len(list) != 2 || list[0].ID != b.ID {
		t.Errorf("in the order added: %+v", list)
	}
	st.SetServiceDestinationPaused(ctx, b.ID, true)
	st.SetServiceDestinationCap(ctx, b.ID, 10, 200)
	st.SetServiceDestinationTemplate(ctx, b.ID, "x")
	since := time.Now().Add(-time.Hour)
	st.SetServiceDestinationState(ctx, b.ID, DestFailing, "disk full", since)
	if g, _ := st.ServiceDestination(ctx, b.ID); !g.Paused || g.CapPhotos != 10 || g.CapMB != 200 || g.Template != "x" || g.State != DestFailing || g.Note != "disk full" || g.Since.IsZero() {
		t.Errorf("%+v", g)
	}
	st.LogDestinationUse(ctx, b.ID, 1000)
	st.LogDestinationUse(ctx, b.ID, 500)
	if n, bytes, err := st.DestinationUse(ctx, b.ID, time.Now().Add(-time.Hour)); err != nil || n != 2 || bytes != 1500 {
		t.Errorf("%d %d %v", n, bytes, err)
	}
	if n, _, _ := st.DestinationUse(ctx, b.ID, time.Now().Add(time.Hour)); n != 0 {
		t.Error("only what is since the time")
	}
	// removing the service takes its bindings and logs with it, and a person's destination can be forgotten
	if err := st.RemoveService(ctx, "family"); err != nil {
		t.Fatal(err)
	}
	if list, _ := st.ServiceDestinations(ctx, "family"); len(list) != 0 {
		t.Error("bindings go with the service")
	}
	if n, _, _ := st.DestinationUse(ctx, b.ID, time.Now().Add(-time.Hour)); n != 0 {
		t.Error("and their logs")
	}
	st.RemoveOwnDestination(ctx, d.ID)
	if _, err := st.OwnDestination(ctx, d.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("%v", err)
	}
}
