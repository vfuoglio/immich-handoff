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
	// bulk moves whole files. It has no limit on the total time of a request, because a large video can take
	// longer than any fixed limit to stream; Transfer gives up on a stalled transfer instead (see StallTimeout).
	bulk *ClientWithResponses
	// PageSize is the search page size (default 250); tests lower it to exercise pagination.
	PageSize int
}

type Auth struct {
	APIKey string // x-api-key
	Cookie string // forwarded browser cookie (status page)
}

// apiTimeout limits an ordinary call to Immich, from connecting to the last byte of the answer.
// It is a variable so tests can shorten it.
var apiTimeout = 2 * time.Minute

// StallTimeout is how long a file transfer may go without a single byte moving before it is given up.
// A transfer that keeps moving is never cut off, however long it takes. A variable so tests can shorten it.
var StallTimeout = 3 * time.Minute

// ErrStalled is why a transfer was given up: no data moved for StallTimeout.
var ErrStalled = errors.New("no data moved for too long")

func New(baseURL string, a Auth, hc *http.Client) (*API, error) {
	bulkHC := hc
	if hc == nil {
		hc = &http.Client{Timeout: apiTimeout}
		// no total limit for files: connecting, and Immich answering once it has the whole file (it checks
		// it first), are limited; the streaming in between is watched for stalls by Transfer
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.ResponseHeaderTimeout = 5 * time.Minute
		bulkHC = &http.Client{Transport: tr}
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
	root := strings.TrimRight(baseURL, "/") + "/api"
	c, err := NewClientWithResponses(root, WithHTTPClient(hc), WithRequestEditorFn(edit))
	if err != nil {
		return nil, err
	}
	bulk, err := NewClientWithResponses(root, WithHTTPClient(bulkHC), WithRequestEditorFn(edit))
	if err != nil {
		return nil, err
	}
	return &API{c: c, bulk: bulk, PageSize: 250}, nil
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
	ID      openapi_types.UUID
	Name    string
	Email   string
	IsAdmin bool // Immich says this person administers the server
}

func (a *API) Me(ctx context.Context) (User, error) {
	r, err := a.c.GetMyUserWithResponse(ctx)
	if err != nil {
		return User{}, err
	}
	if r.JSON200 == nil {
		return User{}, statusErr("getMyUser", r.StatusCode(), r.Body)
	}
	return User{ID: r.JSON200.Id, Name: r.JSON200.Name, Email: string(r.JSON200.Email), IsAdmin: r.JSON200.IsAdmin}, nil
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

// AddKeyPermission adds one permission to an API key, keeping the ones it has. It acts with the person's own
// session (a key cannot change itself), which is why linking an album needs no new key.
func (a *API) AddKeyPermission(ctx context.Context, key openapi_types.UUID, perm Permission) error {
	r, err := a.c.GetApiKeyWithResponse(ctx, key)
	if err != nil {
		return err
	}
	if r.JSON200 == nil {
		return statusErr("getApiKey", r.StatusCode(), r.Body)
	}
	have := r.JSON200.Permissions
	for _, p := range have {
		if p == perm || p == "all" {
			return nil
		}
	}
	all := append(append([]Permission{}, have...), perm)
	u, err := a.c.UpdateApiKeyWithResponse(ctx, key, UpdateApiKeyJSONRequestBody{Permissions: &all})
	if err != nil {
		return err
	}
	if u.JSON200 == nil {
		return statusErr("updateApiKey", u.StatusCode(), u.Body)
	}
	return nil
}

// --- albums ---

// FindAlbum returns an album this account owns with exactly this name, and how many photos it holds, or
// nil if there is none. Albums shared with the account are ignored: a handed-over photo must never land in
// somebody else's album. If several owned albums share the name, the first Immich lists is used.
func (a *API) FindAlbum(ctx context.Context, name string) (id *openapi_types.UUID, count int, err error) {
	owned := true
	r, err := a.c.GetAllAlbumsWithResponse(ctx, &GetAllAlbumsParams{IsOwned: &owned, Name: &name})
	if err != nil {
		return nil, 0, err
	}
	if r.JSON200 == nil {
		return nil, 0, statusErr("getAllAlbums", r.StatusCode(), r.Body)
	}
	for _, al := range *r.JSON200 {
		if al.AlbumName == name {
			i := al.Id
			return &i, al.AssetCount, nil
		}
	}
	return nil, 0, nil
}

// AlbumInfo is an album the account owns, as the page lists them.
type AlbumInfo struct {
	ID    openapi_types.UUID
	Name  string
	Count int
}

// OwnedAlbums lists the albums this account owns (not ones shared with it).
func (a *API) OwnedAlbums(ctx context.Context) ([]AlbumInfo, error) {
	owned := true
	r, err := a.c.GetAllAlbumsWithResponse(ctx, &GetAllAlbumsParams{IsOwned: &owned})
	if err != nil {
		return nil, err
	}
	if r.JSON200 == nil {
		return nil, statusErr("getAllAlbums", r.StatusCode(), r.Body)
	}
	out := make([]AlbumInfo, 0, len(*r.JSON200))
	for _, al := range *r.JSON200 {
		out = append(out, AlbumInfo{ID: al.Id, Name: al.AlbumName, Count: al.AssetCount})
	}
	return out, nil
}

// AlbumAssets lists the photos in an album (trashed ones are not included), all pages.
func (a *API) AlbumAssets(ctx context.Context, album openapi_types.UUID) ([]AssetResponseDto, error) {
	return a.Search(ctx, MetadataSearchDto{AlbumIds: &[]openapi_types.UUID{album}})
}

// CreateAlbum makes an empty album in this account.
func (a *API) CreateAlbum(ctx context.Context, name string) (openapi_types.UUID, error) {
	r, err := a.c.CreateAlbumWithResponse(ctx, CreateAlbumJSONRequestBody{AlbumName: name})
	if err != nil {
		return openapi_types.UUID{}, err
	}
	if r.JSON201 == nil {
		return openapi_types.UUID{}, statusErr("createAlbum", r.StatusCode(), r.Body)
	}
	return r.JSON201.Id, nil
}

// AddToAlbum puts photos in an album. Being in it already is not an error; any other refusal is.
func (a *API) AddToAlbum(ctx context.Context, album openapi_types.UUID, ids ...openapi_types.UUID) error {
	r, err := a.c.AddAssetsToAlbumWithResponse(ctx, album, AddAssetsToAlbumJSONRequestBody{Ids: ids})
	if err != nil {
		return err
	}
	if r.JSON200 == nil {
		return statusErr("addAssetsToAlbum", r.StatusCode(), r.Body)
	}
	for _, item := range *r.JSON200 {
		if !item.Success && (item.Error == nil || *item.Error != BulkIdErrorReasonDuplicate) {
			reason := "unknown"
			if item.Error != nil {
				reason = string(*item.Error)
			}
			return fmt.Errorf("could not add %s to the album: %s", item.Id, reason)
		}
	}
	return nil
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
	return a.DeleteAssets(ctx, []openapi_types.UUID{id}, force)
}

// DeleteAssets removes several assets in one request, so they go together or not at all.
func (a *API) DeleteAssets(ctx context.Context, ids []openapi_types.UUID, force bool) error {
	r, err := a.c.DeleteAssetsWithResponse(ctx, DeleteAssetsJSONRequestBody{Ids: ids, Force: &force})
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
	return TransferWith(ctx, src, dst, a, UploadOptions{})
}

// UploadOptions say how an upload differs from a plain photo.
type UploadOptions struct {
	// Hidden uploads the file with visibility "hidden": it is not shown in the timeline. A live photo's motion
	// clip is a hidden video.
	Hidden bool
	// MotionClip links the upload to this video of the same account as its motion (a live photo).
	MotionClip *openapi_types.UUID
}

// TransferWith is Transfer with options.
func TransferWith(ctx context.Context, src, dst *API, a AssetResponseDto, opts UploadOptions) (*AssetMediaResponseDto, hash.Hash, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	dl, err := src.bulk.DownloadAsset(ctx, a.Id, nil)
	if err != nil {
		return nil, nil, stalled(ctx, err)
	}
	defer dl.Body.Close()
	if dl.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(dl.Body, 300))
		return nil, nil, statusErr("downloadAsset", dl.StatusCode, b)
	}

	// The stream is watched, not timed: it may take as long as it likes while bytes keep moving. If the
	// upload side stops reading, the pipe blocks, the download stops being read, and this fires too.
	watch := time.AfterFunc(StallTimeout, func() { cancel(ErrStalled) })
	defer watch.Stop()
	body := &progressReader{r: dl.Body, onRead: func() { watch.Reset(StallTimeout) }, onEOF: func() { watch.Stop() }}

	h := sha1.New()
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		pw.CloseWithError(writeUpload(mw, io.TeeReader(body, h), a, opts))
	}()

	// No x-immich-checksum header: the hash is only known after the stream has been read.
	r, err := dst.bulk.UploadAssetWithBodyWithResponse(ctx, nil, mw.FormDataContentType(), pr)
	pr.CloseWithError(err) // unblock the writer goroutine if the request failed early
	if err != nil {
		return nil, nil, stalled(ctx, err)
	}
	switch {
	case r.JSON201 != nil:
		return r.JSON201, h, nil
	case r.JSON200 != nil:
		return r.JSON200, h, nil
	}
	return nil, nil, statusErr("uploadAsset", r.StatusCode(), r.Body)
}

func writeUpload(mw *multipart.Writer, file io.Reader, a AssetResponseDto, opts UploadOptions) error {
	fields := map[string]string{
		"fileCreatedAt":  a.FileCreatedAt.UTC().Format(time.RFC3339Nano),
		"fileModifiedAt": a.FileModifiedAt.UTC().Format(time.RFC3339Nano),
		"isFavorite":     strconv.FormatBool(a.IsFavorite),
	}
	if opts.Hidden {
		fields["visibility"] = string(Hidden)
	}
	if opts.MotionClip != nil {
		fields["livePhotoVideoId"] = opts.MotionClip.String()
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

// progressReader tells onRead that bytes moved, and onEOF when the stream ended.
type progressReader struct {
	r      io.Reader
	onRead func()
	onEOF  func()
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.onRead()
	}
	if err == io.EOF {
		p.onEOF()
	}
	return n, err
}

// stalled says so when a transfer was cut off because nothing moved, instead of the bare "context canceled".
func stalled(ctx context.Context, err error) error {
	if errors.Is(context.Cause(ctx), ErrStalled) {
		return fmt.Errorf("transfer given up: %w for %s", ErrStalled, StallTimeout)
	}
	return err
}
