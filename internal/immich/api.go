package immich

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"
)

// API is a small, intention-revealing layer over the generated client, bound to one API key
// (or, for the status page, to one browser session cookie).
type API struct {
	c *ClientWithResponses
	// PageSize is the search page size (default 250); tests lower it to exercise pagination.
	PageSize int
}

type Auth struct {
	APIKey string // x-api-key
	Cookie string // forwarded browser cookie (status page)
}

func New(baseURL string, a Auth, hc *http.Client) (*API, error) {
	if hc == nil {
		hc = &http.Client{Timeout: 2 * time.Minute}
	}
	edit := func(_ context.Context, r *http.Request) error {
		if a.APIKey != "" {
			r.Header.Set("x-api-key", a.APIKey)
		}
		if a.Cookie != "" {
			r.Header.Set("Cookie", a.Cookie)
		}
		r.Header.Set("Accept", "application/json")
		return nil
	}
	c, err := NewClientWithResponses(strings.TrimRight(baseURL, "/")+"/api", WithHTTPClient(hc), WithRequestEditorFn(edit))
	if err != nil {
		return nil, err
	}
	return &API{c: c, PageSize: 250}, nil
}

// HTTPError is a refusal from Immich, so callers can tell a rejected key from a network fault.
type HTTPError struct {
	Op   string
	Code int
	Body string
}

func (e *HTTPError) Error() string { return fmt.Sprintf("%s: HTTP %d %s", e.Op, e.Code, e.Body) }

func statusErr(op string, code int, body []byte) error {
	b := string(body)
	if len(b) > 300 {
		b = b[:300]
	}
	return &HTTPError{Op: op, Code: code, Body: b}
}

// IsAuthError reports whether Immich rejected the credentials themselves (for example a deleted API key).
func IsAuthError(err error) bool {
	var he *HTTPError
	return errors.As(err, &he) && he.Code == http.StatusUnauthorized
}

type User struct {
	ID    openapi_types.UUID
	Name  string
	Email string
}

func (a *API) Me(ctx context.Context) (User, error) {
	r, err := a.c.GetMyUserWithResponse(ctx)
	if err != nil {
		return User{}, err
	}
	if r.JSON200 == nil {
		return User{}, statusErr("getMyUser", r.StatusCode(), r.Body)
	}
	return User{ID: r.JSON200.Id, Name: r.JSON200.Name, Email: string(r.JSON200.Email)}, nil
}

// TagsEnabled reports whether the person has Tags switched on in their Immich account. Immich has it off by
// default for each user, and while it is off the person can neither add tags in the app nor see them.
func (a *API) TagsEnabled(ctx context.Context) (bool, error) {
	r, err := a.c.GetMyPreferencesWithResponse(ctx)
	if err != nil {
		return false, err
	}
	if r.JSON200 == nil {
		return false, statusErr("getMyPreferences", r.StatusCode(), r.Body)
	}
	return r.JSON200.Tags.Enabled, nil
}

// EnableTags switches Tags on for the person (and shows them in the web sidebar), as the Tags switch in
// Immich's account settings does. It needs the person's own session; the person has to ask for it.
func (a *API) EnableTags(ctx context.Context) error {
	on := true
	r, err := a.c.UpdateMyPreferencesWithResponse(ctx, UpdateMyPreferencesJSONRequestBody{Tags: &TagsUpdate{Enabled: &on, SidebarWeb: &on}})
	if err != nil {
		return err
	}
	if r.JSON200 == nil {
		return statusErr("updateMyPreferences", r.StatusCode(), r.Body)
	}
	return nil
}

// ServerMajor returns the Immich major version.
func (a *API) ServerVersion(ctx context.Context) (major, minor int, err error) {
	r, err := a.c.GetServerVersionWithResponse(ctx)
	if err != nil {
		return 0, 0, err
	}
	if r.JSON200 == nil {
		return 0, 0, statusErr("getServerVersion", r.StatusCode(), r.Body)
	}
	return r.JSON200.Major, r.JSON200.Minor, nil
}

// --- tags ---

// FindTag returns the tag id, or nil if it does not exist (and create is false).
func (a *API) FindTag(ctx context.Context, name string, create bool) (*openapi_types.UUID, error) {
	r, err := a.c.GetAllTagsWithResponse(ctx)
	if err != nil {
		return nil, err
	}
	if r.JSON200 == nil {
		return nil, statusErr("getAllTags", r.StatusCode(), r.Body)
	}
	for _, t := range *r.JSON200 {
		if t.Name == name || t.Value == name {
			id := t.Id
			return &id, nil
		}
	}
	if !create {
		return nil, nil
	}
	u, err := a.c.UpsertTagsWithResponse(ctx, UpsertTagsJSONRequestBody{Tags: []string{name}})
	if err != nil {
		return nil, err
	}
	if u.JSON200 == nil || len(*u.JSON200) == 0 {
		return nil, statusErr("upsertTags", u.StatusCode(), u.Body)
	}
	id := (*u.JSON200)[0].Id
	return &id, nil
}

// TagAssets returns an error unless every asset ended up tagged.
func (a *API) TagAssets(ctx context.Context, tag openapi_types.UUID, ids ...openapi_types.UUID) error {
	r, err := a.c.TagAssetsWithResponse(ctx, tag, TagAssetsJSONRequestBody{Ids: ids})
	if err != nil {
		return err
	}
	if r.JSON200 == nil {
		return statusErr("tagAssets", r.StatusCode(), r.Body)
	}
	for _, item := range *r.JSON200 {
		if !item.Success && (item.Error == nil || string(*item.Error) != "duplicate") {
			return fmt.Errorf("tagAssets: %s not tagged (%v)", item.Id, item.Error)
		}
	}
	return nil
}

func (a *API) UntagAssets(ctx context.Context, tag openapi_types.UUID, ids []openapi_types.UUID) error {
	for len(ids) > 0 {
		n := min(len(ids), 200)
		r, err := a.c.UntagAssetsWithResponse(ctx, tag, UntagAssetsJSONRequestBody{Ids: ids[:n]})
		if err != nil {
			return err
		}
		if r.JSON200 == nil {
			return statusErr("untagAssets", r.StatusCode(), r.Body)
		}
		ids = ids[n:]
	}
	return nil
}

// --- search ---

// Search follows pagination and returns every matching asset.
func (a *API) Search(ctx context.Context, f MetadataSearchDto) ([]AssetResponseDto, error) {
	var out []AssetResponseDto
	size := a.PageSize
	if size <= 0 {
		size = 250
	}
	f.Size = &size
	page := 1
	for {
		f.Page = &page
		r, err := a.c.SearchAssetsWithResponse(ctx, nil, f)
		if err != nil {
			return nil, err
		}
		if r.JSON200 == nil {
			return nil, statusErr("searchAssets", r.StatusCode(), r.Body)
		}
		out = append(out, r.JSON200.Assets.Items...)
		// Immich 3.2 still pages with `page`/`nextPage`; the newer `cursor` is accepted but never returned.
		// If `page` is removed upstream, this stops compiling against a newer generated client.
		next := r.JSON200.Assets.NextPage
		if next == nil || *next == "" {
			return out, nil
		}
		n, err := strconv.Atoi(*next)
		if err != nil || n <= page {
			return nil, fmt.Errorf("searchAssets: unexpected nextPage %q after page %d", *next, page)
		}
		page = n
	}
}

func (a *API) Asset(ctx context.Context, id openapi_types.UUID) (*AssetResponseDto, error) {
	r, err := a.c.GetAssetInfoWithResponse(ctx, id, nil)
	if err != nil {
		return nil, err
	}
	if r.JSON200 == nil {
		return nil, statusErr("getAssetInfo", r.StatusCode(), r.Body)
	}
	return r.JSON200, nil
}

func (a *API) DeleteAsset(ctx context.Context, id openapi_types.UUID, force bool) error {
	r, err := a.c.DeleteAssetsWithResponse(ctx, DeleteAssetsJSONRequestBody{Ids: []openapi_types.UUID{id}, Force: &force})
	if err != nil {
		return err
	}
	if c := r.StatusCode(); c < 200 || c > 299 {
		return statusErr("deleteAssets", c, r.Body)
	}
	return nil
}

// --- transfer ---

// Transfer streams the original of srcID from src into dst as a new upload, without touching
// disk or buffering the file. It returns the uploaded asset and the SHA-1 of the bytes that
// actually flowed through, so the caller can verify the stored copy before deleting anything.
func Transfer(ctx context.Context, src, dst *API, a AssetResponseDto) (*AssetMediaResponseDto, hash.Hash, error) {
	dl, err := src.c.DownloadAsset(ctx, a.Id, nil)
	if err != nil {
		return nil, nil, err
	}
	defer dl.Body.Close()
	if dl.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(dl.Body, 300))
		return nil, nil, statusErr("downloadAsset", dl.StatusCode, b)
	}

	h := sha1.New()
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		pw.CloseWithError(writeUpload(mw, io.TeeReader(dl.Body, h), a))
	}()

	// No x-immich-checksum header: the hash is only known after the stream has been read.
	r, err := dst.c.UploadAssetWithBodyWithResponse(ctx, nil, mw.FormDataContentType(), pr)
	pr.CloseWithError(err) // unblock the writer goroutine if the request failed early
	if err != nil {
		return nil, nil, err
	}
	switch {
	case r.JSON201 != nil:
		return r.JSON201, h, nil
	case r.JSON200 != nil:
		return r.JSON200, h, nil
	}
	return nil, nil, statusErr("uploadAsset", r.StatusCode(), r.Body)
}

func writeUpload(mw *multipart.Writer, file io.Reader, a AssetResponseDto) error {
	fields := map[string]string{
		"fileCreatedAt":  a.FileCreatedAt.UTC().Format(time.RFC3339Nano),
		"fileModifiedAt": a.FileModifiedAt.UTC().Format(time.RFC3339Nano),
		"isFavorite":     strconv.FormatBool(a.IsFavorite),
	}
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			return err
		}
	}
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="assetData"; filename=%q`, a.OriginalFileName))
	h.Set("Content-Type", "application/octet-stream")
	part, err := mw.CreatePart(h)
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, file); err != nil {
		return err
	}
	return mw.Close()
}

// ChecksumMatches compares Immich's stored checksum (believed base64 SHA-1; hex also accepted).
func ChecksumMatches(stored string, h hash.Hash) bool {
	sum := h.Sum(nil)
	return stored == base64.StdEncoding.EncodeToString(sum) || strings.EqualFold(stored, hex.EncodeToString(sum))
}
