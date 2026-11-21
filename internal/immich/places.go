package immich

import (
	"context"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"
)

// PlacePermissions is what the key for places needs: to search the person's photos (with their EXIF) and to write the place of one. The
// asset.update permission is broad (Immich lets it change a description, a rating, a date and more); the app only ever writes a place.
var PlacePermissions = perms("user.read", "asset.read", "asset.update")

// PlaceSearch lists the person's photos taken between two moments, with their EXIF (camera, time, place), at most as many as one search
// pages through, newest first, and at most max. A zero createdAfter lists them all.
func (a *API) PlaceSearch(ctx context.Context, from, to, createdAfter time.Time, max int) ([]AssetResponseDto, error) {
	yes := true
	img := IMAGE
	dto := MetadataSearchDto{WithExif: &yes, Type: &img}
	if !from.IsZero() {
		dto.TakenAfter = &from
	}
	if !to.IsZero() {
		dto.TakenBefore = &to
	}
	if !createdAfter.IsZero() {
		dto.CreatedAfter = &createdAfter
	}
	return a.SearchUpTo(ctx, dto, max)
}

// SetLocation writes the same place into photos. Immich finds the city and the country itself, a little later. It needs asset.update.
// (Clearing a place is not possible: on Immich 3.4.1 a null latitude or longitude is refused.)
func (a *API) SetLocation(ctx context.Context, ids []openapi_types.UUID, lat, lon float64) error {
	r, err := a.c.UpdateAssetsWithResponse(ctx, UpdateAssetsJSONRequestBody{Ids: ids, Latitude: &lat, Longitude: &lon})
	if err != nil {
		return err
	}
	if c := r.StatusCode(); c < 200 || c > 299 {
		return statusErr("updateAssets", c, r.Body)
	}
	return nil
}
