package immich

import (
	"context"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"
)

// StoryAssets lists the photos a story is made from, newest first, at most max, with the place and the people each carries (a story
// shows the city and the names, never the coordinates). album and person may be zero values; from and to may be zero times.
func (a *API) StoryAssets(ctx context.Context, album, person openapi_types.UUID, from, to time.Time, max int) ([]AssetResponseDto, error) {
	yes := true
	f := MetadataSearchDto{WithExif: &yes, WithPeople: &yes}
	if album != (openapi_types.UUID{}) {
		f.AlbumIds = &[]openapi_types.UUID{album}
	}
	if person != (openapi_types.UUID{}) {
		f.PersonIds = &[]openapi_types.UUID{person}
	}
	if !from.IsZero() {
		f.TakenAfter = &from
	}
	if !to.IsZero() {
		f.TakenBefore = &to
	}
	return a.SearchUpTo(ctx, f, max)
}
