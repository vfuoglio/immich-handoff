//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// What a hand-over carries over. For each kind of photo this builds the photo in the sender's Immich, hands it
// over, and compares the receiver's copy with the original. It prints a report (run with -v) that the
// documentation page "What is carried over" is written from. It asserts only what the app promises (the file,
// its name, its dates, the favourite mark); everything else is reported, not asserted, because it describes
// Immich as much as this app, and a change there should be noticed and written down, not fail a release.

type fullAsset struct {
	ID               string  `json:"id"`
	Type             string  `json:"type"`
	OriginalFileName string  `json:"originalFileName"`
	IsFavorite       bool    `json:"isFavorite"`
	IsTrashed        bool    `json:"isTrashed"`
	IsEdited         bool    `json:"isEdited"`
	Visibility       string  `json:"visibility"`
	FileCreatedAt    string  `json:"fileCreatedAt"`
	Duration         any     `json:"duration"`
	LivePhotoVideoID *string `json:"livePhotoVideoId"`
	Stack            *struct {
		ID           string `json:"id"`
		AssetCount   int    `json:"assetCount"`
		PrimaryAsset string `json:"primaryAssetId"`
	} `json:"stack"`
	Tags     []struct{ Name string } `json:"tags"`
	ExifInfo *struct {
		Description      string   `json:"description"`
		Rating           *int     `json:"rating"`
		Latitude         *float64 `json:"latitude"`
		Longitude        *float64 `json:"longitude"`
		DateTimeOriginal string   `json:"dateTimeOriginal"`
	} `json:"exifInfo"`
	HasMetadata bool `json:"hasMetadata"`
	Width       *int `json:"width"`
}

func getFull(t *testing.T, u user, id string) fullAsset {
	t.Helper()
	var a fullAsset
	call(t, "GET", "/assets/"+id, keyAuth(u), nil, &a)
	return a
}

func (a fullAsset) exif() (desc string, rating string, gps string, taken string) {
	if a.ExifInfo == nil {
		return "-", "-", "-", "-"
	}
	e := a.ExifInfo
	desc, rating, gps, taken = e.Description, "-", "-", e.DateTimeOriginal
	if desc == "" {
		desc = "-"
	}
	if e.Rating != nil {
		rating = fmt.Sprint(*e.Rating)
	}
	if e.Latitude != nil && e.Longitude != nil {
		gps = fmt.Sprintf("%.4f,%.4f", *e.Latitude, *e.Longitude)
	}
	if taken == "" {
		taken = "-"
	}
	return
}

func tagNames(a fullAsset) string {
	var n []string
	for _, tg := range a.Tags {
		n = append(n, tg.Name)
	}
	if len(n) == 0 {
		return "-"
	}
	return strings.Join(n, ",")
}

// uploadFile uploads any file, with extra form fields (for example visibility).
func uploadFile(t *testing.T, u user, name string, data []byte, mime string, extra map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	now := time.Now().UTC().Format(time.RFC3339)
	mw.WriteField("fileCreatedAt", now)
	mw.WriteField("fileModifiedAt", now)
	for k, v := range extra {
		mw.WriteField(k, v)
	}
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="assetData"; filename=%q`, name))
	h.Set("Content-Type", mime)
	p, _ := mw.CreatePart(h)
	p.Write(data)
	mw.Close()
	req, _ := http.NewRequest("POST", baseURL+"/api/assets", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("x-api-key", u.key)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out struct {
		ID string `json:"id"`
	}
	if res.StatusCode >= 300 {
		t.Fatalf("upload %s: HTTP %d", name, res.StatusCode)
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil || out.ID == "" {
		t.Fatalf("upload %s: no id (%v)", name, err)
	}
	return out.ID
}

// makeVideo makes a tiny real MP4 (ffmpeg), or returns nil when ffmpeg is not installed.
func makeVideo(t *testing.T, seconds int) []byte {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return nil
	}
	out := filepath.Join(t.TempDir(), "clip.mp4")
	cmd := exec.Command("ffmpeg", "-loglevel", "error", "-f", "lavfi", "-i", fmt.Sprintf("testsrc=duration=%d:size=96x96:rate=10", seconds),
		"-pix_fmt", "yuv420p", "-y", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Logf("ffmpeg failed: %v %s", err, b)
		return nil
	}
	b, _ := os.ReadFile(out)
	return b
}

func TestWhatIsCarriedOver(t *testing.T) {
	alice, bob, family, _ := bootstrap(t)
	bin := buildApp(t)
	a := startApp(t, bin, alice, bob, family) // fixed accounts, originals moved to the trash
	_ = a

	// The copies this test makes sit in the receiver's inbox. Other tests count that inbox, so take the tag
	// off them when this test is done: the photos stay, the shared state is left as it was found.
	var copies []string
	t.Cleanup(func() {
		if len(copies) == 0 {
			return
		}
		var tags []struct{ ID, Name string }
		call(t, "GET", "/tags", keyAuth(family), nil, &tags)
		for _, tg := range tags {
			if tg.Name == "inbox" {
				call(t, "DELETE", "/tags/"+tg.ID+"/assets", keyAuth(family), map[string]any{"ids": copies}, nil)
			}
		}
	})

	var report []string
	row := func(item, sender, receiver string) {
		report = append(report, fmt.Sprintf("  %-34s sender: %-28s receiver's copy: %s", item, sender, receiver))
	}
	t.Cleanup(func() { t.Logf("WHAT IS CARRIED OVER\n%s", strings.Join(report, "\n")) })

	// the sender's workflow must exist before the first photo is tagged, or no event reaches the app
	eventually(t, "alice's workflow exists", 4*time.Minute, func() bool {
		var wfs []struct {
			Name    string `json:"name"`
			Enabled bool   `json:"enabled"`
		}
		call(t, "GET", "/workflows", keyAuth(alice), nil, &wfs)
		for _, w := range wfs {
			if w.Name == "immich-handoff" && w.Enabled {
				return true
			}
		}
		return false
	})
	waitProcessed := func(u user, id string) {
		eventually(t, "Immich has read the file", 90*time.Second, func() bool {
			x := getFull(t, u, id)
			return x.HasMetadata && x.ExifInfo != nil && x.Width != nil
		})
	}
	// handOver tags a photo for the service and returns the receiver's new copy, or "" if none arrives.
	handOver := func(id string) string {
		before := map[string]bool{}
		for _, x := range searchTag(t, family, "inbox", false) {
			before[x] = true
		}
		tagAsset(t, alice, "share", id)
		copyID := ""
		end := time.Now().Add(120 * time.Second)
		for time.Now().Before(end) && copyID == "" {
			for _, x := range searchTag(t, family, "inbox", false) {
				if !before[x] {
					copyID = x
				}
			}
			time.Sleep(2 * time.Second)
		}
		if copyID != "" {
			copies = append(copies, copyID)
		}
		return copyID
	}
	albumsOf := func(u user, id string) int {
		var al []struct{ ID string }
		call(t, "GET", "/albums?assetId="+id, keyAuth(u), nil, &al)
		return len(al)
	}

	t.Run("a photo with the details a person adds in Immich", func(t *testing.T) {
		id := uploadFile(t, alice, "pier.jpg", jpegBytes(300), "image/jpeg", nil)
		waitProcessed(alice, id)
		mustOK(t, call(t, "PUT", "/assets/"+id, keyAuth(alice), map[string]any{
			"isFavorite": true, "description": "Sunset at the pier", "rating": 4,
			"latitude": 48.8584, "longitude": 2.2945, "dateTimeOriginal": "2020-06-15T18:30:00.000Z"}, nil), "edit details")
		mustOK(t, call(t, "POST", "/albums", keyAuth(alice), map[string]any{"albumName": "Trip", "assetIds": []string{id}}, nil), "album")
		tagAsset(t, alice, "keepsake", id)
		cp := handOver(id)
		if cp == "" {
			t.Fatal("the photo was not handed over")
		}
		src := getFull(t, alice, id) // read afterwards: Immich settles a changed date a moment after the edit
		dst := getFull(t, family, cp)
		sd, sr, sg, st := src.exif()
		dd, dr, dg, dt := dst.exif()
		row("favourite mark", fmt.Sprint(src.IsFavorite), fmt.Sprint(dst.IsFavorite))
		row("description", sd, dd)
		row("rating", sr, dr)
		row("location", sg, dg)
		row("date taken (EXIF)", st, dt)
		row("date of the file", src.FileCreatedAt, dst.FileCreatedAt)
		row("file name", src.OriginalFileName, dst.OriginalFileName)
		row("other tags", tagNames(src), tagNames(dst))
		row("albums", fmt.Sprint(albumsOf(alice, id)), fmt.Sprint(albumsOf(family, cp)))
		if !dst.IsFavorite || dst.OriginalFileName != "pier.jpg" || dst.FileCreatedAt != src.FileCreatedAt {
			t.Errorf("the app promises the file, its name, its dates and the favourite mark: %+v", dst)
		}
	})

	t.Run("a photo whose details are written inside the file", func(t *testing.T) {
		if _, err := exec.LookPath("exiftool"); err != nil {
			row("details inside the file", "not tested", "exiftool is not installed here")
			return
		}
		path := filepath.Join(t.TempDir(), "beach.jpg")
		if err := os.WriteFile(path, jpegBytes(306), 0o600); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("exiftool", "-overwrite_original", "-ImageDescription=Beach day", "-Rating=3",
			"-DateTimeOriginal=2019:07:04 10:00:00", "-GPSLatitude=41.3851", "-GPSLatitudeRef=N",
			"-GPSLongitude=2.1734", "-GPSLongitudeRef=E", path).CombinedOutput(); err != nil {
			t.Fatalf("exiftool: %v %s", err, out)
		}
		data, _ := os.ReadFile(path)
		id := uploadFile(t, alice, "beach.jpg", data, "image/jpeg", nil)
		waitProcessed(alice, id)
		cp := handOver(id)
		if cp == "" {
			row("details inside the file", "uploaded", "NOT HANDED OVER")
			return
		}
		src, dst := getFull(t, alice, id), getFull(t, family, cp)
		sd, sr, sg, st := src.exif()
		dd, dr, dg, dt := dst.exif()
		row("inside the file: description", sd, dd)
		row("inside the file: rating", sr, dr)
		row("inside the file: location", sg, dg)
		row("inside the file: date taken", st, dt)
	})

	t.Run("an archived photo", func(t *testing.T) {
		id := uploadFile(t, alice, "archived.jpg", jpegBytes(301), "image/jpeg", nil)
		waitProcessed(alice, id)
		mustOK(t, call(t, "PUT", "/assets/"+id, keyAuth(alice), map[string]any{"visibility": "archive"}, nil), "archive")
		cp := handOver(id)
		if cp == "" {
			row("archived photo", "archive", "NOT HANDED OVER (the app did not find it)")
			return
		}
		row("archived photo", "archive", "visibility "+getFull(t, family, cp).Visibility)
	})

	t.Run("a photo that was edited (rotated) in Immich", func(t *testing.T) {
		id := uploadFile(t, alice, "rotated.jpg", jpegBytes(302), "image/jpeg", nil)
		waitProcessed(alice, id)
		code := call(t, "PUT", "/assets/"+id+"/edits", keyAuth(alice), map[string]any{
			"edits": []map[string]any{{"action": "rotate", "parameters": map[string]any{"angle": 90}}}}, nil)
		if code >= 300 {
			row("edited photo", fmt.Sprintf("edit refused (HTTP %d)", code), "not tested")
			return
		}
		eventually(t, "the edit is recorded", 30*time.Second, func() bool { return getFull(t, alice, id).IsEdited })
		cp := handOver(id)
		if cp == "" {
			row("edited photo", "edited", "NOT HANDED OVER")
			return
		}
		row("edit (rotation)", fmt.Sprintf("edited=%v", getFull(t, alice, id).IsEdited), fmt.Sprintf("edited=%v", getFull(t, family, cp).IsEdited))
	})

	t.Run("a live photo (a still with a separate motion clip)", func(t *testing.T) {
		clip := makeVideo(t, 1)
		if clip == nil {
			row("live photo", "not tested", "ffmpeg is not installed here")
			return
		}
		video := uploadFile(t, alice, "motion.mp4", clip, "video/mp4", map[string]string{"visibility": "hidden"})
		still := uploadFile(t, alice, "live.jpg", jpegBytes(303), "image/jpeg", nil)
		waitProcessed(alice, still)
		mustOK(t, call(t, "PUT", "/assets/"+still, keyAuth(alice), map[string]any{"livePhotoVideoId": video}, nil), "pair the clip with the still")
		if x := getFull(t, alice, still); x.LivePhotoVideoID == nil {
			row("live photo", "could not be paired here", "not tested")
			return
		}
		cp := handOver(still)
		if cp == "" {
			row("live photo", "paired", "NOT HANDED OVER")
			return
		}
		dst := getFull(t, family, cp)
		motion := "none"
		if dst.LivePhotoVideoID != nil {
			motion = "has a motion clip"
			// the app promises this: the clip arrives hidden, as a video, and the still is linked to it
			clipCopy := getFull(t, family, *dst.LivePhotoVideoID)
			if clipCopy.Type != "VIDEO" || clipCopy.Visibility != "hidden" {
				t.Errorf("the receiver's motion clip is type %q, visibility %q; want a hidden VIDEO", clipCopy.Type, clipCopy.Visibility)
			}
		} else {
			t.Error("the receiver's copy of a live photo has no motion clip")
		}
		sv := getFull(t, alice, video)
		row("live photo: motion clip", "paired with the still", motion)
		row("live photo: sender's clip afterwards", fmt.Sprintf("trashed=%v visibility=%s", sv.IsTrashed, sv.Visibility), "-")
	})

	t.Run("photos in a stack", func(t *testing.T) {
		first := uploadFile(t, alice, "stack-1.jpg", jpegBytes(304), "image/jpeg", nil)
		second := uploadFile(t, alice, "stack-2.jpg", jpegBytes(305), "image/jpeg", nil)
		waitProcessed(alice, first)
		waitProcessed(alice, second)
		code := call(t, "POST", "/stacks", keyAuth(alice), map[string]any{"assetIds": []string{first, second}}, nil)
		if code >= 300 {
			row("stack", fmt.Sprintf("could not be made (HTTP %d)", code), "not tested")
			return
		}
		defer func() {
			if t.Failed() { // what the app says is the quickest way to see why
				t.Logf("app log:\n%s", a.log.String())
			}
		}()
		cp := handOver(first) // only the cover is tagged
		if cp == "" {
			row("stack (cover tagged)", "stacked", "NOT HANDED OVER")
			return
		}
		// the app promises the whole stack: both photos arrive, as a stack again, and (this sender moves) both go
		var recv *struct {
			ID           string `json:"id"`
			AssetCount   int    `json:"assetCount"`
			PrimaryAsset string `json:"primaryAssetId"`
		}
		eventually(t, "the receiver's copy to be in a stack of 2", 60*time.Second, func() bool {
			recv = getFull(t, family, cp).Stack
			return recv != nil && recv.AssetCount == 2
		})
		if recv.PrimaryAsset != cp {
			t.Errorf("the cover of the tagged photo must stay the cover at the receiver: primary %s, copy %s", recv.PrimaryAsset, cp)
		}
		for _, id := range []string{first, second} {
			if !getFull(t, alice, id).IsTrashed {
				t.Errorf("the sender's photo %s of the stack was not removed after the hand-over", id)
			}
		}
		row("stack: the tagged cover", "stack of 2", fmt.Sprintf("in a stack of %d, cover kept", recv.AssetCount))
		row("stack: the other photo", "in the same stack", "handed over with it")
	})

	t.Run("a video", func(t *testing.T) {
		clip := makeVideo(t, 2)
		if clip == nil {
			row("video", "not tested", "ffmpeg is not installed here")
			return
		}
		id := uploadFile(t, alice, "clip.mp4", clip, "video/mp4", nil)
		eventually(t, "Immich has read the video", 90*time.Second, func() bool { return getFull(t, alice, id).HasMetadata })
		src := getFull(t, alice, id)
		cp := handOver(id)
		if cp == "" {
			row("video", "uploaded", "NOT HANDED OVER")
			return
		}
		dst := getFull(t, family, cp)
		row("video: type and duration", fmt.Sprint(src.Type, " ", src.Duration), fmt.Sprint(dst.Type, " ", dst.Duration))
	})
}
