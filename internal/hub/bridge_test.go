package hub_test

import (
	"context"
	"testing"

	"github.com/vfuoglio/immich-handoff/internal/bridge"
	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

func TestAServiceWithABridgeGetsItsRemoteReceiver(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	if err := e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: "holiday", Tag: "hol", Policy: "approval"}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetBridge(ctx, store.Bridge{Service: "holiday", Address: "https://photos.example.org", Key: "REMOTE", DisplayName: "Aunt Lia"}); err != nil {
		t.Fatal(err)
	}

	// off (the default): the service must not run at all, because the owner promised a copy on the other server too
	e.h.Bridge = bridge.Settings{}
	if err := e.h.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if svc := service(e, "holiday"); svc.Remote != nil || svc.Broken == "" || len(svc.Receivers) != 0 {
		t.Fatalf("bridges off: %+v", svc)
	}

	e.h.Bridge = bridge.Settings{Enabled: true}
	if err := e.h.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	svc := service(e, "holiday")
	if svc.Remote == nil || svc.Remote.Label() != "Aunt Lia" || svc.Broken != "" || len(svc.Receivers) != 1 {
		t.Fatalf("bridges on: %+v", svc)
	}
	// the same receiver is kept across reloads (its connections are reused), and replaced with a new key
	first := svc.Remote
	e.h.Reload(ctx)
	if service(e, "holiday").Remote != first {
		t.Error("a reload made a new receiver")
	}
	e.st.SetBridge(ctx, store.Bridge{Service: "holiday", Address: "https://photos.example.org", Key: "NEW", DisplayName: "Aunt Lia"})
	e.h.Reload(ctx)
	if service(e, "holiday").Remote == first {
		t.Error("a new key must make a new receiver")
	}

	// a service without a bridge is untouched, and removing the bridge removes the receiver
	e.st.RemoveBridge(ctx, "holiday")
	e.h.Reload(ctx)
	if service(e, "holiday").Remote != nil || service(e, "family").Remote != nil {
		t.Error("no bridge, no remote")
	}
}
