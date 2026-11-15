package immich

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"
)

// What rules by people, places and dates ask of Immich (docs/rules-design.md).

// ErrNoSmartSearch is returned when the server cannot do a smart search (it runs without machine learning).
var ErrNoSmartSearch = errors.New("this Immich server cannot do a smart search: it runs without machine learning")

// PersonInfo is a named person Immich has found faces of.
type PersonInfo struct {
	ID   openapi_types.UUID
	Name string
}

// People lists the named, visible people of this account, by name. It needs person.read.
func (a *API) People(ctx context.Context) ([]PersonInfo, error) {
	var out []PersonInfo
	for page := 1; page <= 40; page++ {
		size := 500
		r, err := a.c.GetAllPeopleWithResponse(ctx, &GetAllPeopleParams{Page: &page, Size: &size})
		if err != nil {
			return nil, err
		}
		if r.JSON200 == nil {
			return nil, statusErr("getAllPeople", r.StatusCode(), r.Body)
		}
		for _, p := range r.JSON200.People {
			if p.Name != "" && !p.IsHidden {
				out = append(out, PersonInfo{ID: p.Id, Name: p.Name})
			}
		}
		if r.JSON200.HasNextPage == nil || !*r.JSON200.HasNextPage {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

// PersonThumbnail fetches a picture of a person's face. It needs person.read.
func (a *API) PersonThumbnail(ctx context.Context, id openapi_types.UUID) (data []byte, contentType string, err error) {
	resp, err := a.bulk.GetPersonThumbnail(ctx, id)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return nil, "", statusErr("getPersonThumbnail", resp.StatusCode, b)
	}
	data, err = io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	return data, resp.Header.Get("Content-Type"), err
}

// Places lists the countries (or, given a country, the cities) the library holds photos from: the only values a place search
// matches, because matching is exact. It needs asset.read.
func (a *API) Places(ctx context.Context, country string) ([]string, error) {
	p := &GetSearchSuggestionsParams{Type: SearchSuggestionType("country")}
	if country != "" {
		p.Type = SearchSuggestionType("city")
		p.Country = &country
	}
	r, err := a.c.GetSearchSuggestionsWithResponse(ctx, p)
	if err != nil {
		return nil, err
	}
	if r.JSON200 == nil {
		return nil, statusErr("getSearchSuggestions", r.StatusCode(), r.Body)
	}
	out := append([]string(nil), *r.JSON200...)
	sort.Strings(out)
	return out, nil
}

// SmartSearch asks Immich for the photos that look like a phrase, among those that reached it after a moment. A server without
// machine learning answers ErrNoSmartSearch.
func (a *API) SmartSearch(ctx context.Context, phrase string, after time.Time, size int) ([]AssetResponseDto, error) {
	body := SmartSearchDto{Query: &phrase, Size: &size}
	if !after.IsZero() {
		body.CreatedAfter = &after
	}
	r, err := a.c.SearchSmartWithResponse(ctx, body)
	if err != nil {
		return nil, err
	}
	if r.JSON200 == nil {
		if r.StatusCode() == http.StatusBadRequest && strings.Contains(string(r.Body), "not enabled") {
			return nil, ErrNoSmartSearch
		}
		return nil, statusErr("searchSmart", r.StatusCode(), r.Body)
	}
	return r.JSON200.Assets.Items, nil
}
