package syncer

import "github.com/vfuoglio/immich-handoff/internal/immich"

// Account is one Immich account the app can act for, through one API key.
type Account struct {
	Name string
	API  *immich.API
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
