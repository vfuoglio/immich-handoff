package store

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestAPersonsMailCanBeTurnedOnChangedAndOff(t *testing.T) {
	st, err := open(t, t.TempDir(), "person-mail-secret-1234")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	u := uuid.New()
	if _, ok, err := st.PersonMail(ctx, u); ok || err != nil {
		t.Fatalf("nobody asked yet: %v %v", ok, err)
	}
	if err := st.SetPersonMail(ctx, PersonMail{UserID: u, Name: "Bob", Email: "bob@example.com", Lang: "de", Mode: MailAll}); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := st.MarkPersonMailSent(ctx, u, at); err != nil {
		t.Fatal(err)
	}
	if err := st.SetPersonMail(ctx, PersonMail{UserID: u, Name: "Robert", Email: "robert@example.com", Lang: "fr", Mode: MailNeeds}); err != nil {
		t.Fatal(err)
	}
	p, ok, err := st.PersonMail(ctx, u)
	if err != nil || !ok || p.Name != "Robert" || p.Email != "robert@example.com" || p.Lang != "fr" || p.Mode != MailNeeds {
		t.Fatalf("%+v %v %v", p, ok, err)
	}
	if !p.LastSent.Equal(at) || p.CreatedAt.IsZero() {
		t.Errorf("changing the settings keeps the dates: %+v", p)
	}
	other := uuid.New()
	st.SetPersonMail(ctx, PersonMail{UserID: other, Name: "Eve", Email: "eve@example.com", Lang: "en", Mode: MailAll})
	if l, _ := st.PersonMailList(ctx); len(l) != 2 {
		t.Errorf("list: %+v", l)
	}
	if err := st.DeletePersonMail(ctx, u); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.PersonMail(ctx, u); ok {
		t.Error("still there")
	}
	if _, ok, _ := st.PersonMail(ctx, other); !ok {
		t.Error("someone else's row went too")
	}
}
