package syncer

import (
	"context"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/immich"
)

// Account is one Immich account the app can act for, through one API key.
type Account struct {
	Name   string
	API    *immich.API
	UserID uuid.UUID // the Immich user behind the key; set by ResolveUsers or when a person joins
}

// Service says: photos that a sender tags with Tag are handed to every receiver.
// The original is removed only after all receivers hold a verified copy.
type Service struct {
	Name       string // "" is the single default service of a plain .env install
	Tag        string // tag a sender adds
	InboxTag   string // tag put on new copies in each receiver
	RefusedTag string // replaces Tag on an original that a receiver refused
	ExpiryDays int    // unfiled copies lose InboxTag after this many days
	Senders    []*Account
	Receivers  []*Account
	AllowJoin  bool // people may add themselves as senders from the web page
}

// Label is the name used in logs and on the status page.
func (s *Service) Label() string {
	if s.Name == "" {
		return "default"
	}
	return s.Name
}

// WorkflowName is the name of the Immich workflow this app keeps in each sender's account.
// The default service keeps the original name so existing installs are not duplicated on upgrade.
func (s *Service) WorkflowName() string {
	if s.Name == "" {
		return "immich-handoff"
	}
	return "immich-handoff: " + s.Name
}

// Provider supplies the current services when they can change while the app runs.
type Provider interface {
	Services() []*Service
}

// Current returns the services to work on right now.
func (s *Syncer) Current() []*Service {
	if s.Provider != nil {
		return s.Provider.Services()
	}
	return s.Services
}

// ResolveUsers looks up which Immich user is behind each account's key. Accounts whose key is
// rejected are returned so the caller can report them; they simply never match a signed-in person.
func ResolveUsers(ctx context.Context, services []*Service) (failed []*Account) {
	seen := map[*Account]bool{}
	for _, svc := range services {
		for _, group := range [][]*Account{svc.Senders, svc.Receivers} {
			for _, a := range group {
				if seen[a] {
					continue
				}
				seen[a] = true
				if u, err := a.API.Me(ctx); err == nil {
					a.UserID = u.ID
				} else {
					failed = append(failed, a)
				}
			}
		}
	}
	return failed
}
