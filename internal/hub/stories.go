package hub

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// Stories (#272): a short animated recap with a public link. The owner chooses a source; the photos are chosen once, in the
// background, with the owner's session (storybuild.go); the public page only shows what was chosen, by opaque ids, with a key of the
// story's own that can look at pictures. See docs/design/stories-design.md.

const (
	maxStories  = 10
	storyYearOK = 30 // photos in a year for the end-of-year suggestion
)

var (
	ErrBadStoryTitle  = errors.New("give the story a title of one to sixty characters")
	ErrBadStorySource = errors.New("choose an album, a person, a range of dates or a drop box for the story")
	ErrBadStoryDates  = errors.New("the dates must be real dates, and the first must not be after the last")
	ErrBadStoryEnd    = errors.New("a story's link can last 7, 30 or 90 days, or never")
	ErrTooManyStories = errors.New("you already have 10 stories; remove one first")
	ErrNoStory        = errors.New("that is not one of your stories")
	ErrStoryNoPhotos  = errors.New("there are no photos to make a story of in that")
	ErrStoryBuilding  = errors.New("the story is still being made")
	ErrBadStoryPhoto  = errors.New("that photo is not in the story")
	ErrStoryPreview   = errors.New("a preview in chats needs the address of this app (PUBLIC_APP_URL) to be set")
	ErrStoryLast      = errors.New("a story needs at least one photo: pick it again instead")
)

// StoryParams is what a person chooses when making a story.
type StoryParams struct {
	Title      string
	Kind       string    // store.StoryAlbum, StoryPerson, StoryRange or StoryBox
	Source     uuid.UUID // the album or person
	Box        string    // the drop box's id, for a story of a drop box
	From, To   string    // YYYY-MM-DD, optional
	EndsInDays int       // 7, 30 or 90; 0 is never (the default offered is 30)
}

var storyRun struct {
	mu  sync.Mutex
	ids map[string]bool
}

func storyEndOK(days int) bool { return days == 0 || days == 7 || days == 30 || days == 90 }

// CreateStory makes a story and starts choosing its photos in the background. It needs the owner's own session: to check the source,
// to make the story's key, and to look at the photos.
func (h *Hub) CreateStory(ctx context.Context, sess *immich.API, owner immich.User, p StoryParams) (store.Story, error) {
	if !h.SelfService() {
		return store.Story{}, ErrNotJoinable
	}
	me := uuid.UUID(owner.ID)
	if n, err := h.Store.CountStories(ctx, me); err != nil {
		return store.Story{}, err
	} else if n >= maxStories {
		return store.Story{}, ErrTooManyStories
	}
	if !storyEndOK(p.EndsInDays) {
		return store.Story{}, ErrBadStoryEnd
	}
	if (p.From != "" && !validDay(p.From)) || (p.To != "" && !validDay(p.To)) || (p.From != "" && p.To != "" && p.From > p.To) {
		return store.Story{}, ErrBadStoryDates
	}
	s := store.Story{OwnerID: me, OwnerName: owner.Name, Kind: p.Kind, From: p.From, To: p.To, ShowDates: true, ShowPlaces: true}
	switch p.Kind {
	case store.StoryAlbum:
		albums, err := sess.OwnedAlbums(ctx)
		if err != nil {
			return store.Story{}, err
		}
		for _, a := range albums {
			if uuid.UUID(a.ID) == p.Source {
				s.SourceID, s.SourceName = p.Source.String(), a.Name
			}
		}
		if s.SourceID == "" {
			if shared, err := sess.AlbumsSharedWithMe(ctx); err == nil {
				for _, a := range shared {
					if uuid.UUID(a.ID) == p.Source {
						s.SourceID, s.SourceName = p.Source.String(), a.Name
					}
				}
			}
		}
		if s.SourceID == "" {
			return store.Story{}, ErrNoAlbum
		}
	case store.StoryPerson:
		people, err := sess.People(ctx)
		if err != nil {
			return store.Story{}, err
		}
		for _, x := range people {
			if uuid.UUID(x.ID) == p.Source {
				s.SourceID, s.SourceName = p.Source.String(), x.Name
			}
		}
		if s.SourceID == "" {
			return store.Story{}, ErrRulePerson
		}
	case store.StoryRange:
		if p.From == "" || p.To == "" {
			return store.Story{}, ErrBadStoryDates
		}
	case store.StoryBox:
		b, _, err := h.dropBoxFor(ctx, me, p.Box, "owner")
		if err != nil {
			return store.Story{}, err
		}
		s.SourceID, s.SourceName = b.ID, b.Name
	default:
		return store.Story{}, ErrBadStorySource
	}
	title := strings.TrimSpace(p.Title)
	if title == "" {
		title = defaultStoryTitle(s)
	}
	if n := utf8.RuneCountInString(title); n < 1 || n > 60 || strings.ContainsAny(title, "\r\n\x00") {
		return store.Story{}, ErrBadStoryTitle
	}
	s.Title = title
	if p.EndsInDays > 0 {
		s.EndsAt = h.now().Add(time.Duration(p.EndsInDays) * 24 * time.Hour)
	}
	keyID, secret, err := sess.CreateAPIKey(ctx, keyName("story "+title), immich.FramePermissions)
	if err != nil {
		return store.Story{}, err
	}
	s.KeyID, s.Key, s.Token = keyID, secret, newToken()+newToken()
	s, err = h.Store.AddStory(ctx, s)
	if err != nil {
		if derr := sess.DeleteAPIKey(context.WithoutCancel(ctx), keyID); derr != nil {
			h.Log.Error("could not remove the key made for a failed story", "err", derr)
		}
		return store.Story{}, err
	}
	if p.Kind == store.StoryRange && p.From != "" && p.To != "" && strings.HasSuffix(p.From, "-01-01") && strings.HasSuffix(p.To, "-12-31") && p.From[:4] == p.To[:4] {
		if y := atoiYear(p.From[:4]); y > 0 {
			_ = h.Store.SetStoryOffer(ctx, me, y, "made")
		}
	}
	h.startStoryBuild(sess, s)
	h.event(ctx, "", me, "story", i18n.Pack("{name} is making the story {title}", "name", owner.Name, "title", s.Title))
	return s, nil
}

func atoiYear(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func defaultStoryTitle(s store.Story) string {
	switch s.Kind {
	case store.StoryAlbum, store.StoryBox:
		return s.SourceName
	case store.StoryPerson:
		return s.SourceName
	}
	if s.From != "" && len(s.From) >= 4 && s.From[:4] == s.To[:4] && strings.HasSuffix(s.From, "-01-01") && strings.HasSuffix(s.To, "-12-31") {
		return "My " + s.From[:4]
	}
	return "Our story"
}

// startStoryBuild chooses the photos in the background with the owner's session; a story is shown only when it is ready.
func (h *Hub) startStoryBuild(sess *immich.API, s store.Story) {
	storyRun.mu.Lock()
	if storyRun.ids == nil {
		storyRun.ids = map[string]bool{}
	}
	if storyRun.ids[s.ID] {
		storyRun.mu.Unlock()
		return
	}
	storyRun.ids[s.ID] = true
	storyRun.mu.Unlock()
	go func() {
		defer func() {
			storyRun.mu.Lock()
			delete(storyRun.ids, s.ID)
			storyRun.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
		defer cancel()
		if err := h.buildStory(ctx, sess, s); err != nil {
			h.Log.Warn("story failed", "title", s.Title, "err", err)
			problem := trimNet(err)
			if errors.Is(err, ErrStoryNoPhotos) {
				problem = err.Error()
			}
			_ = h.Store.SetStoryBuilt(context.WithoutCancel(ctx), s.ID, store.StoryFailed, problem, nil, nil)
		}
	}()
}

// RebuildStory chooses the photos again (the library may have changed), keeping the link, the title and what the page shows.
func (h *Hub) RebuildStory(ctx context.Context, sess *immich.API, owner immich.User, id string) error {
	s, err := h.Store.StoryByID(ctx, uuid.UUID(owner.ID), id)
	if err != nil {
		return ErrNoStory
	}
	if s.State == store.StoryBuilding {
		return ErrStoryBuilding
	}
	if err := h.Store.SetStoryBuilding(ctx, s.ID); err != nil {
		return err
	}
	h.startStoryBuild(sess, s)
	return nil
}

// StoryEdit is what the owner can change about a story.
type StoryEdit struct {
	Title                             string
	ShowDates, ShowPlaces, ShowPeople bool
	Preview                           bool
	ChapterTitles                     map[int]string // by chapter number
	EndsInDays                        int            // -1: leave as it is
}

// EditStory changes a story's title, chapter titles, what the page shows, the preview switch and its end date.
func (h *Hub) EditStory(ctx context.Context, owner immich.User, id string, e StoryEdit) error {
	s, err := h.Store.StoryByID(ctx, uuid.UUID(owner.ID), id)
	if err != nil {
		return ErrNoStory
	}
	title := strings.TrimSpace(e.Title)
	if n := utf8.RuneCountInString(title); n < 1 || n > 60 || strings.ContainsAny(title, "\r\n\x00") {
		return ErrBadStoryTitle
	}
	s.Title = title
	s.ShowDates, s.ShowPlaces, s.ShowPeople = e.ShowDates, e.ShowPlaces, e.ShowPeople
	if e.Preview && h.AppURL == "" {
		return ErrStoryPreview
	}
	s.Preview = e.Preview
	for i, t := range e.ChapterTitles {
		if i < 0 || i >= len(s.Chapters) {
			continue
		}
		t = strings.TrimSpace(t)
		if utf8.RuneCountInString(t) > 60 || strings.ContainsAny(t, "\r\n\x00") {
			return ErrBadStoryTitle
		}
		s.Chapters[i].Title = t
	}
	if e.EndsInDays >= 0 {
		if !storyEndOK(e.EndsInDays) {
			return ErrBadStoryEnd
		}
		s.EndsAt = time.Time{}
		if e.EndsInDays > 0 {
			s.EndsAt = h.now().Add(time.Duration(e.EndsInDays) * 24 * time.Hour)
		}
		if s.State == store.StoryEnded && (s.EndsAt.IsZero() || s.EndsAt.After(h.now())) { // a longer life brings an ended story back
			if err := h.Store.SetStoryBuilt(ctx, s.ID, store.StoryReady, "", s.Picks, s.Chapters); err != nil {
				return err
			}
		}
	}
	return h.Store.UpdateStory(ctx, s)
}

// RemoveStoryPhoto takes one photo out of a story (never the last one).
func (h *Hub) RemoveStoryPhoto(ctx context.Context, owner immich.User, id, asset string) error {
	s, err := h.Store.StoryByID(ctx, uuid.UUID(owner.ID), id)
	if err != nil {
		return ErrNoStory
	}
	if len(s.Picks) <= 1 {
		return ErrStoryLast
	}
	var keep []store.StoryPick
	found := false
	for _, p := range s.Picks {
		if p.Asset == asset {
			found = true
			continue
		}
		keep = append(keep, p)
	}
	if !found {
		return ErrBadStoryPhoto
	}
	s.Picks = keep
	// a chapter left with no photo goes, and the others are numbered again
	used := map[int]bool{}
	for _, p := range s.Picks {
		used[p.Chapter] = true
	}
	remap := map[int]int{}
	var chapters []store.StoryChapter
	for i, c := range s.Chapters {
		if used[i] {
			remap[i] = len(chapters)
			chapters = append(chapters, c)
		}
	}
	for i := range s.Picks {
		s.Picks[i].Chapter = remap[s.Picks[i].Chapter]
	}
	s.Chapters = chapters
	return h.Store.UpdateStory(ctx, s)
}

// EndStory removes a story: its link stops at once and its key is deleted from the owner's account.
func (h *Hub) EndStory(ctx context.Context, sess *immich.API, owner immich.User, id string) error {
	s, err := h.Store.StoryByID(ctx, uuid.UUID(owner.ID), id)
	if err != nil {
		return ErrNoStory
	}
	if err := h.Store.DeleteStory(ctx, uuid.UUID(owner.ID), s.ID); err != nil {
		return err
	}
	if sess != nil && s.KeyID != uuid.Nil {
		if err := sess.DeleteAPIKey(ctx, s.KeyID); err != nil && !immich.IsNotFound(err) {
			h.Log.Warn("could not delete the key of a story", "err", err)
		}
	}
	h.event(ctx, "", uuid.UUID(owner.ID), "story", i18n.Pack("{name} removed the story {title}", "name", owner.Name, "title", s.Title))
	return nil
}

// StoriesOf lists a person's stories, newest first.
func (h *Hub) StoriesOf(ctx context.Context, user uuid.UUID) ([]store.Story, error) {
	if h.Store == nil {
		return nil, nil
	}
	return h.Store.StoriesOf(ctx, user)
}

// StoryOfOwner finds one of a person's stories.
func (h *Hub) StoryOfOwner(ctx context.Context, user uuid.UUID, id string) (store.Story, error) {
	s, err := h.Store.StoryByID(ctx, user, id)
	if err != nil {
		return store.Story{}, ErrNoStory
	}
	return s, nil
}

// StoryFor finds the story a public link opens. Unknown, still being made, ended and expired all answer alike.
func (h *Hub) StoryFor(ctx context.Context, token string) (store.Story, error) {
	if len(token) < 32 || len(token) > 128 || h.Store == nil {
		return store.Story{}, ErrDropGone
	}
	s, err := h.Store.StoryByToken(ctx, token)
	if err != nil || (!s.EndsAt.IsZero() && !s.EndsAt.After(h.now())) {
		return store.Story{}, ErrDropGone
	}
	return s, nil
}

func storyID(token string, asset string) string {
	m := hmac.New(sha256.New, []byte(token))
	m.Write([]byte(asset))
	return hex.EncodeToString(m.Sum(nil))[:16]
}

// StoryDoc is what the public page is given: the title, the chapters and the scenes, with opaque photo ids and only the words the
// story's switches allow. Coordinates and Immich ids are never in it.
type StoryDoc struct {
	Title    string         `json:"title"`
	Chapters []StoryDocPart `json:"chapters"`
	Scenes   []StoryDocShot `json:"scenes"`
	Dates    bool           `json:"dates"`
	Places   bool           `json:"places"`
}

// StoryDocPart is a chapter card.
type StoryDocPart struct {
	Title string `json:"title,omitempty"`
	From  string `json:"from,omitempty"`
	To    string `json:"to,omitempty"`
}

// StoryDocShot is a photo.
type StoryDocShot struct {
	ID      string   `json:"id"`
	Chapter int      `json:"chapter"`
	Date    string   `json:"date,omitempty"`
	Place   string   `json:"place,omitempty"`
	People  []string `json:"people,omitempty"`
}

// StoryDocOf makes the public document of a story.
func StoryDocOf(s store.Story) StoryDoc {
	d := StoryDoc{Title: s.Title, Dates: s.ShowDates, Places: s.ShowPlaces, Chapters: []StoryDocPart{}, Scenes: []StoryDocShot{}}
	for _, c := range s.Chapters {
		part := StoryDocPart{Title: c.Title}
		if s.ShowDates {
			part.From, part.To = c.From, c.To
		}
		d.Chapters = append(d.Chapters, part)
	}
	for _, p := range s.Picks {
		sc := StoryDocShot{ID: storyID(s.Token, p.Asset), Chapter: p.Chapter}
		if s.ShowDates {
			sc.Date = p.Date
		}
		if s.ShowPlaces {
			sc.Place = p.Place
		}
		if s.ShowPeople {
			sc.People = p.People
		}
		d.Scenes = append(d.Scenes, sc)
	}
	return d
}

func (h *Hub) storyAsset(s store.Story, id string) (openapi_types.UUID, bool) {
	for _, p := range s.Picks {
		if storyID(s.Token, p.Asset) == id {
			u, err := uuid.Parse(p.Asset)
			return u, err == nil
		}
	}
	return openapi_types.UUID{}, false
}

// StoryImage fetches the picture of one photo of a story, large enough for a screen, with the story's key. Only a photo that is in the
// story can be asked for, by its opaque id.
func (h *Hub) StoryImage(ctx context.Context, token, id string) ([]byte, string, error) {
	s, err := h.StoryFor(ctx, token)
	if err != nil {
		return nil, "", err
	}
	asset, ok := h.storyAsset(s, id)
	if !ok {
		return nil, "", ErrDropGone
	}
	return h.storyPicture(ctx, s, asset)
}

func (h *Hub) storyPicture(ctx context.Context, s store.Story, asset openapi_types.UUID) ([]byte, string, error) {
	key := "story/" + s.ID + "/" + asset.String()
	if p, ok := h.wall.get(key); ok {
		return p.data, p.kind, nil
	}
	api, err := h.Connect(s.Key)
	if err != nil {
		return nil, "", err
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	data, kind, err := api.Thumbnail(cctx, asset, true)
	if err != nil {
		if immich.IsNotFound(err) {
			return nil, "", ErrDropGone
		}
		return nil, "", err
	}
	h.wall.put(key, wallPic{data, kind})
	return data, kind, nil
}

// StoryCover is the first photo of a story whose owner allowed a preview in chats; any other story has none.
func (h *Hub) StoryCover(ctx context.Context, token string) ([]byte, string, error) {
	s, err := h.StoryFor(ctx, token)
	if err != nil || !s.Preview || len(s.Picks) == 0 {
		return nil, "", ErrDropGone
	}
	asset, err := uuid.Parse(s.Picks[0].Asset)
	if err != nil {
		return nil, "", ErrDropGone
	}
	return h.storyPicture(ctx, s, asset)
}

// StoryThumb is a photo of a story for the owner's own editor, through their own session. Only a photo in the story is served.
func (h *Hub) StoryThumb(ctx context.Context, sess *immich.API, owner uuid.UUID, id, asset string) ([]byte, string, error) {
	s, err := h.Store.StoryByID(ctx, owner, id)
	if err != nil {
		return nil, "", ErrNoStory
	}
	for _, p := range s.Picks {
		if p.Asset == asset {
			u, err := uuid.Parse(asset)
			if err != nil {
				return nil, "", ErrBadStoryPhoto
			}
			return sess.Thumbnail(ctx, u, false)
		}
	}
	return nil, "", ErrBadStoryPhoto
}

// StoryMaintenance ends stories whose end date has come: their link stops at once. Their keys stay in the owner's Immich until the
// owner removes the story, because only the owner's own session can delete a key.
func (h *Hub) StoryMaintenance(ctx context.Context) {
	if h.Store == nil || !h.SelfService() {
		return
	}
	if n, err := h.Store.EndStoriesDue(ctx, h.now()); err != nil {
		h.Log.Debug("could not end stories", "err", err)
	} else if n > 0 {
		h.Log.Info("stories ended", "count", n)
	}
}

// StoryCaption says in a few words what a photo of a story shows, for a TV: the chapter, the date and the place the story allows.
func StoryCaption(s store.Story, p store.StoryPick) string {
	var parts []string
	if p.Chapter >= 0 && p.Chapter < len(s.Chapters) && s.Chapters[p.Chapter].Title != "" {
		parts = append(parts, s.Chapters[p.Chapter].Title)
	}
	if s.ShowDates && p.Date != "" {
		parts = append(parts, p.Date)
	}
	if s.ShowPlaces && p.Place != "" {
		parts = append(parts, p.Place)
	}
	return strings.Join(parts, " · ")
}

// storyYear says which year the end-of-year suggestion is for at a moment: this year from December, the year before until the middle of
// January, otherwise none.
func storyYear(now time.Time) int {
	switch {
	case now.Month() == time.December:
		return now.Year()
	case now.Month() == time.January && now.Day() <= 15:
		return now.Year() - 1
	}
	return 0
}

var storyYearCache struct {
	mu sync.Mutex
	m  map[uuid.UUID]struct {
		year int
		ok   bool
		at   time.Time
	}
}

// StoryYearOffer says whether to suggest a story of the year: it is the season, the person has not answered for that year, and they
// have at least 30 photos from it. It looks with the person's own session and remembers the answer for an hour.
func (h *Hub) StoryYearOffer(ctx context.Context, sess *immich.API, user immich.User) (int, bool) {
	if h.Store == nil || !h.SelfService() {
		return 0, false
	}
	year := storyYear(h.now())
	me := uuid.UUID(user.ID)
	if year == 0 || h.Store.StoryOffer(ctx, me, year) != "" {
		return 0, false
	}
	storyYearCache.mu.Lock()
	if c, hit := storyYearCache.m[me]; hit && c.year == year && h.now().Sub(c.at) < time.Hour {
		storyYearCache.mu.Unlock()
		return year, c.ok
	}
	storyYearCache.mu.Unlock()
	from := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(year, 12, 31, 23, 59, 59, 0, time.UTC)
	list, err := sess.StoryAssets(ctx, openapi_types.UUID{}, openapi_types.UUID{}, from, to, storyYearOK)
	ok := err == nil && len(storyFilter(list, "", "")) >= storyYearOK
	storyYearCache.mu.Lock()
	if storyYearCache.m == nil {
		storyYearCache.m = map[uuid.UUID]struct {
			year int
			ok   bool
			at   time.Time
		}{}
	}
	storyYearCache.m[me] = struct {
		year int
		ok   bool
		at   time.Time
	}{year, ok, h.now()}
	storyYearCache.mu.Unlock()
	return year, ok
}

// DismissStoryYear remembers that the person does not want the suggestion for a year.
func (h *Hub) DismissStoryYear(ctx context.Context, user uuid.UUID, year int) error {
	if h.Store == nil || year < 2000 || year > 2200 {
		return ErrNoStory
	}
	return h.Store.SetStoryOffer(ctx, user, year, "dismissed")
}

// MakeStoryOfYear makes the story of a year: every photo from that year, with the person's own session.
func (h *Hub) MakeStoryOfYear(ctx context.Context, sess *immich.API, owner immich.User, year int) (store.Story, error) {
	if year < 2000 || year > 2200 {
		return store.Story{}, ErrBadStoryDates
	}
	return h.CreateStory(ctx, sess, owner, StoryParams{Kind: store.StoryRange, From: fmt.Sprintf("%d-01-01", year), To: fmt.Sprintf("%d-12-31", year), EndsInDays: 30})
}
