package immich

import (
	"context"
	"strings"
	"time"
)

// PathSearch finds the photos whose original path contains the folder (a prefix ending in a slash, such as
// "/external/photos/"), every page. Immich matches the text anywhere in the path and is case-sensitive, so the caller still
// checks that each path starts with the folder (a "/photos/" fragment would also match "/mnt/photos/"). With createdAfter
// set, only photos that reached Immich after that moment are listed. It needs asset.read.
func (a *API) PathSearch(ctx context.Context, folder string, createdAfter time.Time) ([]AssetResponseDto, error) {
	dto := MetadataSearchDto{OriginalPath: &folder}
	if !createdAfter.IsZero() {
		dto.CreatedAfter = &createdAfter
	}
	return a.Search(ctx, dto)
}

// FolderOf is the folder part of an original path ("/a/b/c.jpg" is "/a/b").
func FolderOf(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[:i]
	}
	return ""
}

// StackSearch lists the photos (not videos) of this account that stacking looks at, with their EXIF, every page. folder, when not
// empty, is a folder with a trailing slash ("/external/photos/"); with createdAfter set, only photos that reached Immich after it.
// With stacked false a photo that is in a stack is left out, the cover too; with it true every photo is listed. A result never says
// which stack a photo is in (its stack field is empty in a search), so a caller that needs to know asks both ways and compares. It
// needs asset.read.
func (a *API) StackSearch(ctx context.Context, folder string, createdAfter time.Time, stacked bool) ([]AssetResponseDto, error) {
	yes := true
	img := IMAGE
	dto := MetadataSearchDto{WithExif: &yes, WithStacked: &stacked, Type: &img}
	if folder != "" {
		dto.OriginalPath = &folder
	}
	if !createdAfter.IsZero() {
		dto.CreatedAfter = &createdAfter
	}
	return a.Search(ctx, dto)
}
