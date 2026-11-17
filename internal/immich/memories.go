package immich

import (
	"context"
	"sort"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"
)

// MemoriesPermissions is what a person's key for "On this day" messages is given: reading their memories and looking at
// the pictures in them, and nothing else. A key limited to these two can list memories and fetch thumbnails.
var MemoriesPermissions = perms("memory.read", "asset.view")

// Memory is one of the person's memories for a day, as the "On this day" message needs it.
type Memory struct {
	ID     openapi_types.UUID
	Year   int       // the year the photos are from
	Seen   bool      // the person already watched it in Immich
	At     time.Time // the memory's date
	Photos []openapi_types.UUID
}

// MemoriesFor lists the "on this day" memories Immich has for a day (YYYY-MM-DD), the oldest year first. It needs memory.read.
func (a *API) MemoriesFor(ctx context.Context, day time.Time) ([]Memory, error) {
	d := openapi_types.Date{Time: time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)}
	typ := OnThisDay
	r, err := a.c.SearchMemoriesWithResponse(ctx, &SearchMemoriesParams{For: &d, Type: &typ})
	if err != nil {
		return nil, err
	}
	if r.JSON200 == nil {
		return nil, statusErr("searchMemories", r.StatusCode(), r.Body)
	}
	var out []Memory
	for _, m := range *r.JSON200 {
		mm := Memory{ID: m.Id, Year: m.Data.Year, Seen: m.SeenAt != nil, At: m.MemoryAt}
		for _, as := range m.Assets {
			mm.Photos = append(mm.Photos, as.Id)
		}
		out = append(out, mm)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Year < out[j].Year })
	return out, nil
}

// CreateMemory makes an "on this day" memory (the end-to-end tests do, to have one on any day).
func (a *API) CreateMemory(ctx context.Context, at time.Time, year int, photos ...openapi_types.UUID) (openapi_types.UUID, error) {
	body := CreateMemoryJSONRequestBody{
		Type: OnThisDay, MemoryAt: at, Data: OnThisDayDto{Year: year}, AssetIds: &photos,
	}
	r, err := a.c.CreateMemoryWithResponse(ctx, body)
	if err != nil {
		return openapi_types.UUID{}, err
	}
	if r.JSON201 == nil {
		return openapi_types.UUID{}, statusErr("createMemory", r.StatusCode(), r.Body)
	}
	return r.JSON201.Id, nil
}
