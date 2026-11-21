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

// What rules by people, places and dates ask of Immich (docs/contributing/design/rules-design.md).

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

// Cameras lists the camera makes the library holds photos from or, given a make, its models: the only values a camera search
// matches, because matching is exact. It needs asset.read.
func (a *API) Cameras(ctx context.Context, make string) ([]string, error) {
	p := &GetSearchSuggestionsParams{Type: CameraMake}
	if make != "" {
		p.Type = CameraModel
		p.Make = &make
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

// RuleFilter is what a rule asks Immich to find: every set field must hold.
type RuleFilter struct {
	PersonIDs     []openapi_types.UUID
	TakenAfter    time.Time
	TakenBefore   time.Time
	Country, City string
	AlbumID       openapi_types.UUID // the zero value: any album
	CreatedAfter  time.Time
	Phrase        string
	Make, Model   string
	TagIDs        []openapi_types.UUID // every one of them must be on the photo
}

// RuleSearch finds the photos a rule matches in Immich's own terms: one search with everything Immich can filter. With a phrase it
// is a smart search (the closest 100), otherwise a metadata search through every page. The people each result carries and its
// place come with it. A server without machine learning answers ErrNoSmartSearch for a phrase.
func (a *API) RuleSearch(ctx context.Context, f RuleFilter) ([]AssetResponseDto, error) {
	if f.Phrase == "" {
		dto := MetadataSearchDto{}
		yes := true
		dto.WithPeople, dto.WithExif = &yes, &yes
		if len(f.PersonIDs) > 0 {
			dto.PersonIds = &f.PersonIDs
		}
		if !f.TakenAfter.IsZero() {
			dto.TakenAfter = &f.TakenAfter
		}
		if !f.TakenBefore.IsZero() {
			dto.TakenBefore = &f.TakenBefore
		}
		if f.Country != "" {
			dto.Country = &f.Country
		}
		if f.City != "" {
			dto.City = &f.City
		}
		if f.AlbumID != (openapi_types.UUID{}) {
			dto.AlbumIds = &[]openapi_types.UUID{f.AlbumID}
		}
		if !f.CreatedAfter.IsZero() {
			dto.CreatedAfter = &f.CreatedAfter
		}
		if f.Make != "" {
			dto.Make = &f.Make
		}
		if f.Model != "" {
			dto.Model = &f.Model
		}
		if len(f.TagIDs) > 0 {
			dto.TagIds = &f.TagIDs
		}
		return a.Search(ctx, dto)
	}
	size := 100
	dto := SmartSearchDto{Query: &f.Phrase, Size: &size}
	if len(f.PersonIDs) > 0 {
		dto.PersonIds = &f.PersonIDs
	}
	if !f.TakenAfter.IsZero() {
		dto.TakenAfter = &f.TakenAfter
	}
	if !f.TakenBefore.IsZero() {
		dto.TakenBefore = &f.TakenBefore
	}
	if f.Country != "" {
		dto.Country = &f.Country
	}
	if f.City != "" {
		dto.City = &f.City
	}
	if f.AlbumID != (openapi_types.UUID{}) {
		dto.AlbumIds = &[]openapi_types.UUID{f.AlbumID}
	}
	if !f.CreatedAfter.IsZero() {
		dto.CreatedAfter = &f.CreatedAfter
	}
	if f.Make != "" {
		dto.Make = &f.Make
	}
	if f.Model != "" {
		dto.Model = &f.Model
	}
	if len(f.TagIDs) > 0 {
		dto.TagIds = &f.TagIDs
	}
	r, err := a.c.SearchSmartWithResponse(ctx, dto)
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

// SimilarTo asks Immich for the photos that look like one of the person's own, nearest first: Immich's smart search with
// queryAssetId ranks the whole library and gives no score, so the position is all there is. A server without machine learning answers
// ErrNoSmartSearch.
func (a *API) SimilarTo(ctx context.Context, asset openapi_types.UUID, size int) ([]AssetResponseDto, error) {
	dto := SmartSearchDto{QueryAssetId: &asset, Size: &size}
	r, err := a.c.SearchSmartWithResponse(ctx, dto)
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

// Recent lists the newest photos (not videos) of the person's own library, one page, for them to pick examples from.
func (a *API) Recent(ctx context.Context, n int) ([]AssetResponseDto, error) {
	order, typ, page := AssetOrderDesc, IMAGE, 1
	dto := MetadataSearchDto{Size: &n, Page: &page, Order: &order, Type: &typ}
	r, err := a.c.SearchAssetsWithResponse(ctx, nil, dto)
	if err != nil {
		return nil, err
	}
	if r.JSON200 == nil {
		return nil, statusErr("searchAssets", r.StatusCode(), r.Body)
	}
	return r.JSON200.Assets.Items, nil
}
