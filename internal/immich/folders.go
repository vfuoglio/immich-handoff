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
