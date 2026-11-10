package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
)

// RequiredPermissions is exactly what the key held for a server on the other side may do: say who it is (needed for
// the key to be checked at all), make a tag, put it on a photo and upload. Nothing reads, searches, lists or deletes.
var RequiredPermissions = []string{"user.read", "tag.create", "tag.asset", "asset.upload"}

// SupportedMajors are the Immich major versions the calls the bridge makes were tested with.
var SupportedMajors = []int{3}

// Why a check failed. Each is wrapped with the detail a person needs, so use errors.Is.
var (
	ErrNotImmich   = errors.New("that address does not answer like an Immich server")
	ErrVersion     = errors.New("that Immich version is not one this app was tested with")
	ErrKeyTooBroad = errors.New("this key can read your library; make a smaller one")
	ErrKeyTooSmall = errors.New("this key cannot do everything a hand-over needs")
	ErrRevoked     = errors.New("the server does not accept this key")
	ErrUnreachable = errors.New("cannot reach that server")
)

// Failure is a reason from the list above with the technical detail that goes with it (permission names, versions,
// an address). The reason alone is a sentence a page can translate; the detail is names and numbers, never prose.
type Failure struct {
	Kind   error
	Detail string
	Help   string // what to do, for the doctor and the log (English)
}

func (f *Failure) Error() string {
	s := f.Kind.Error()
	if f.Detail != "" {
		s += " (" + f.Detail + ")"
	}
	if f.Help != "" {
		s += ". " + f.Help
	}
	return s
}
func (f *Failure) Unwrap() error { return f.Kind }

// Info is what a successful check learned.
type Info struct {
	Version string // "v3.2.0"
	Major   int
}

// PermissionsHelp says in words which permissions to give the key, for a page or the doctor.
func PermissionsHelp() string {
	return "Give the key exactly these permissions and no others: " + strings.Join(RequiredPermissions, ", ") + "."
}

// Check says whether the app may hand photos over to the server at addr (already accepted by Policy.Address) with
// key. It asks the server for its version (no key needed) and the key for its own permissions (no permission
// needed), and nothing else: it never lists users and keeps nothing a server returns beyond the version.
// A server that redirects is an error (ErrRedirect, naming where it points), because the app does not follow.
func Check(ctx context.Context, hc *http.Client, addr, key string) (Info, error) {
	var v struct{ Major, Minor, Patch int }
	if err := getJSON(ctx, hc, addr+"/api/server/version", "", &v); err != nil {
		return Info{}, err
	}
	if v.Major == 0 && v.Minor == 0 && v.Patch == 0 {
		return Info{}, ErrNotImmich
	}
	info := Info{Version: fmt.Sprintf("v%d.%d.%d", v.Major, v.Minor, v.Patch), Major: v.Major}
	if !slices.Contains(SupportedMajors, v.Major) {
		return info, &Failure{Kind: ErrVersion, Detail: info.Version + ", tested with " + majorsText()}
	}
	var k struct {
		Permissions []string `json:"permissions"`
	}
	if err := getJSON(ctx, hc, addr+"/api/api-keys/me", key, &k); err != nil {
		return info, err
	}
	return info, judgeKey(k.Permissions)
}

func majorsText() string {
	var out []string
	for _, m := range SupportedMajors {
		out = append(out, fmt.Sprintf("%d", m))
	}
	return strings.Join(out, " or ")
}

// judgeKey accepts a key with exactly RequiredPermissions: more is refused as too broad, less as too small.
func judgeKey(have []string) error {
	var extra, missing []string
	for _, p := range have {
		if !slices.Contains(RequiredPermissions, p) {
			extra = append(extra, p)
		}
	}
	for _, p := range RequiredPermissions {
		if !slices.Contains(have, p) {
			missing = append(missing, p)
		}
	}
	sort.Strings(extra)
	switch {
	case len(extra) > 0:
		return &Failure{Kind: ErrKeyTooBroad, Detail: strings.Join(extra, ", "), Help: PermissionsHelp()}
	case len(missing) > 0:
		return &Failure{Kind: ErrKeyTooSmall, Detail: strings.Join(missing, ", "), Help: PermissionsHelp()}
	}
	return nil
}

func getJSON(ctx context.Context, hc *http.Client, target, key string, into any) error {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBadAddress, err)
	}
	req.Header.Set("Accept", "application/json")
	if key != "" {
		req.Header.Set("x-api-key", key)
	}
	resp, err := hc.Do(req)
	if err != nil {
		if errors.Is(err, ErrBadAddress) {
			return err
		}
		return fmt.Errorf("%w: %v", ErrUnreachable, trimErr(err))
	}
	defer resp.Body.Close()
	switch {
	case IsRedirect(resp):
		where := RedirectTarget(resp)
		if where == "" {
			where = "somewhere else"
		}
		return &Failure{Kind: ErrRedirect, Detail: where}
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		if key == "" {
			return fmt.Errorf("%w: it refused a question that needs no key", ErrNotImmich)
		}
		return ErrRevoked
	case resp.StatusCode >= 500:
		return fmt.Errorf("%w: it answered HTTP %d", ErrUnreachable, resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("%w (HTTP %d)", ErrNotImmich, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnreachable, trimErr(err))
	}
	if err := json.Unmarshal(b, into); err != nil {
		return ErrNotImmich
	}
	return nil
}

// trimErr drops the URL that net/http puts in its errors: it says nothing new and the address is not for logs.
func trimErr(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err
	}
	return err
}
