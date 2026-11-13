package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/521studios/encounter-builder-api/internal/maps"
	"github.com/521studios/encounter-builder-api/internal/model"
)

// fakeMaps stands in for S3: objects maps key -> content type.
type fakeMaps struct {
	objects map[string]string
	heads   []string
	cleared []string
	// Set to make the matching S3 call fail.
	presignErr, headErr, clearErr error
}

func (f *fakeMaps) PresignUpload(_ context.Context, key, ct string) (string, map[string]string, error) {
	if f.presignErr != nil {
		return "", nil, f.presignErr
	}
	return "https://bucket.test", map[string]string{"key": key, "Content-Type": ct}, nil
}

func (f *fakeMaps) ContentType(_ context.Context, key string) (string, error) {
	f.heads = append(f.heads, key)
	if f.headErr != nil {
		return "", f.headErr
	}
	ct, ok := f.objects[key]
	if !ok {
		return "", maps.ErrNotFound
	}
	return ct, nil
}

func (f *fakeMaps) ClearPending(_ context.Context, key string) error {
	if f.clearErr != nil {
		return f.clearErr
	}
	f.cleared = append(f.cleared, key)
	return nil
}

// mapFixture creates chapter "ch" in campaign g1 with a fake map store holding one
// uploaded png at key.
func mapFixture(t *testing.T) (http.Handler, *fakeMaps, string, string) {
	t.Helper()
	h, _ := newHandler(t, true, 0)
	fm := &fakeMaps{objects: map[string]string{}}
	h.cfg.Maps = fm
	router := chapterRoutes(h)
	rec := do(t, router, http.MethodPost, chPath, `{"name":"Ch"}`)
	var ch model.Chapter
	if err := json.Unmarshal(rec.Body.Bytes(), &ch); err != nil || ch.ID == "" {
		t.Fatalf("create chapter: %d %s", rec.Code, rec.Body)
	}
	key := "maps/g1/" + ch.ID + "/a.png"
	fm.objects[key] = "image/png"
	return router, fm, chPath + "/" + ch.ID, key
}

func putChapter(t *testing.T, router http.Handler, path, mapImage string) (int, model.Chapter) {
	t.Helper()
	body := `{"name":"Ch"`
	if mapImage != "" {
		body += `,"map_image":` + mapImage
	}
	rec := do(t, router, http.MethodPut, path, body+`}`)
	var ch model.Chapter
	_ = json.Unmarshal(rec.Body.Bytes(), &ch)
	return rec.Code, ch
}

func TestMapImage_UploadURL(t *testing.T) {
	router, _, path, _ := mapFixture(t)
	rec := do(t, router, http.MethodPost, path+"/map-image/upload-url", `{"content_type":"image/webp"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload-url = %d, want 200; body=%s", rec.Code, rec.Body)
	}
	var out struct {
		Key    string            `json:"key"`
		URL    string            `json:"url"`
		Fields map[string]string `json:"fields"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	prefix := "maps/g1/" + strings.TrimPrefix(path, chPath+"/") + "/"
	if !strings.HasPrefix(out.Key, prefix) || !strings.HasSuffix(out.Key, ".webp") || out.Fields["Content-Type"] != "image/webp" || out.URL == "" {
		t.Fatalf("upload-url response wrong: %+v", out)
	}

	if rec := do(t, router, http.MethodPost, path+"/map-image/upload-url", `{"content_type":"image/gif"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("gif = %d, want 400", rec.Code)
	}
	if rec := do(t, router, http.MethodPost, chPath+"/nope/map-image/upload-url", `{"content_type":"image/png"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("missing chapter = %d, want 404", rec.Code)
	}
}

func TestMapImage_UnconfiguredIs503(t *testing.T) {
	h, _ := newHandler(t, true, 0)
	router := chapterRoutes(h)
	rec := do(t, router, http.MethodPost, chPath, `{"name":"Ch"}`)
	var ch model.Chapter
	_ = json.Unmarshal(rec.Body.Bytes(), &ch)
	if rec := do(t, router, http.MethodPost, chPath+"/"+ch.ID+"/map-image/upload-url", `{"content_type":"image/png"}`); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("upload-url without maps = %d, want 503", rec.Code)
	}
	code, _ := putChapter(t, router, chPath+"/"+ch.ID, `{"key":"maps/g1/`+ch.ID+`/a.png","width":1,"height":1}`)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("set image without maps = %d, want 503", code)
	}
}

func TestMapImage_SetKeepTransformRemove(t *testing.T) {
	router, fm, path, key := mapFixture(t)

	code, ch := putChapter(t, router, path, `{"key":"`+key+`","width":800,"height":600}`)
	if code != http.StatusOK {
		t.Fatalf("set = %d", code)
	}
	if ch.MapImage == nil || ch.MapImage.URL != "/"+key || ch.MapImage.Scale != 1 || ch.MapImage.Rotation != 0 || ch.MapImage.Width != 800 {
		t.Fatalf("set response wrong: %+v", ch.MapImage)
	}
	if len(fm.heads) != 1 || len(fm.cleared) != 1 || fm.cleared[0] != key {
		t.Fatalf("new key must be checked and kept: heads=%v cleared=%v", fm.heads, fm.cleared)
	}

	// Omitting map_image keeps it, and the URL is presented on list too.
	if code, ch = putChapter(t, router, path, ""); code != http.StatusOK || ch.MapImage == nil {
		t.Fatalf("omit = %d %+v, want image kept", code, ch.MapImage)
	}
	rec := do(t, router, http.MethodGet, chPath, "")
	var list []model.Chapter
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 1 || list[0].MapImage == nil || list[0].MapImage.URL != "/"+key {
		t.Fatalf("list must present url: %s", rec.Body)
	}

	// A transform-only change on the same key doesn't touch S3.
	code, ch = putChapter(t, router, path, `{"key":"`+key+`","width":800,"height":600,"rotation":270,"scale":0.5}`)
	if code != http.StatusOK || ch.MapImage.Rotation != 270 || ch.MapImage.Scale != 0.5 {
		t.Fatalf("transform = %d %+v", code, ch.MapImage)
	}
	if len(fm.heads) != 1 || len(fm.cleared) != 1 {
		t.Fatalf("same-key save hit S3: heads=%v cleared=%v", fm.heads, fm.cleared)
	}

	if code, ch = putChapter(t, router, path, `null`); code != http.StatusOK || ch.MapImage != nil {
		t.Fatalf("null = %d %+v, want removed", code, ch.MapImage)
	}
}

func TestMapImage_StoresNoURL(t *testing.T) {
	h, _ := newHandler(t, true, 0)
	fm := &fakeMaps{objects: map[string]string{}}
	h.cfg.Maps = fm
	router := chapterRoutes(h)
	rec := do(t, router, http.MethodPost, chPath, `{"name":"Ch"}`)
	var ch model.Chapter
	_ = json.Unmarshal(rec.Body.Bytes(), &ch)
	key := "maps/g1/" + ch.ID + "/a.png"
	fm.objects[key] = "image/png"
	if code, _ := putChapter(t, router, chPath+"/"+ch.ID, `{"key":"`+key+`","width":1,"height":1}`); code != http.StatusOK {
		t.Fatalf("set = %d", code)
	}
	stored, err := h.cfg.Store.GetChapter(context.Background(), "g1", ch.ID)
	if err != nil || stored.MapImage == nil || stored.MapImage.URL != "" {
		t.Fatalf("stored map image must have no url: %+v err=%v", stored.MapImage, err)
	}
}

func TestMapImage_Rejects(t *testing.T) {
	router, fm, path, key := mapFixture(t)
	other := strings.Replace(key, "a.png", "b.png", 1)
	fm.objects[strings.Replace(key, "a.png", "c.gif", 1)] = "image/gif"
	cases := map[string]string{
		"rotation 45":         `{"key":"` + key + `","width":1,"height":1,"rotation":45}`,
		"scale 0":             `{"key":"` + key + `","width":1,"height":1,"scale":0}`,
		"scale 4.5":           `{"key":"` + key + `","width":1,"height":1,"scale":4.5}`,
		"zero width":          `{"key":"` + key + `","width":0,"height":1}`,
		"huge height":         `{"key":"` + key + `","width":1,"height":32769}`,
		"url is GET-only":     `{"key":"` + key + `","url":"/x","width":1,"height":1}`,
		"other chapter's key": `{"key":"maps/g1/other/a.png","width":1,"height":1}`,
		"nested key":          `{"key":"` + strings.Replace(key, "a.png", "x/a.png", 1) + `","width":1,"height":1}`,
		"not uploaded":        `{"key":"` + other + `","width":1,"height":1}`,
		"not an image":        `{"key":"` + strings.Replace(key, "a.png", "c.gif", 1) + `","width":1,"height":1}`,
	}
	for name, img := range cases {
		if code, _ := putChapter(t, router, path, img); code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", name, code)
		}
	}
	if len(fm.cleared) != 0 {
		t.Fatalf("rejected saves must not clear any tag: %v", fm.cleared)
	}
}

func TestMapImage_S3FailuresAreServerErrors(t *testing.T) {
	boom := errors.New("s3 down")
	img := func(key string) string { return `{"key":"` + key + `","width":1,"height":1}` }

	router, fm, path, key := mapFixture(t)
	fm.presignErr = boom
	if rec := do(t, router, http.MethodPost, path+"/map-image/upload-url", `{"content_type":"image/png"}`); rec.Code != http.StatusInternalServerError {
		t.Fatalf("presign failure = %d, want 500", rec.Code)
	}
	fm.headErr = boom
	if code, _ := putChapter(t, router, path, img(key)); code != http.StatusBadGateway {
		t.Fatalf("HeadObject failure = %d, want 502", code)
	}
	fm.headErr = nil
	fm.clearErr = boom
	code, _ := putChapter(t, router, path, img(key))
	if code != http.StatusBadGateway {
		t.Fatalf("PutObjectTagging failure = %d, want 502", code)
	}
	// A failed save must not have stored the image.
	rec := do(t, router, http.MethodGet, chPath, "")
	if strings.Contains(rec.Body.String(), "map_image") {
		t.Fatalf("failed save stored the image: %s", rec.Body)
	}
}

func TestMapImage_CreateRejectsAnImage(t *testing.T) {
	h, _ := newHandler(t, true, 0)
	h.cfg.Maps = &fakeMaps{objects: map[string]string{}}
	router := chapterRoutes(h)
	if rec := do(t, router, http.MethodPost, chPath, `{"name":"Ch","map_image":{"key":"maps/g1/x/a.png","width":1,"height":1}}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("create with image = %d, want 400", rec.Code)
	}
	if rec := do(t, router, http.MethodPost, chPath, `{"name":"Ch","map_image":null}`); rec.Code != http.StatusCreated {
		t.Fatalf("create with null image = %d, want 201", rec.Code)
	}
}

// encMapFixture creates encounter "e" in campaign g1 with a fake map store holding one
// uploaded png under the encounter's own prefix.
func encMapFixture(t *testing.T) (http.Handler, *fakeMaps, string, string) {
	t.Helper()
	h, _ := newHandler(t, true, 0)
	fm := &fakeMaps{objects: map[string]string{}}
	h.cfg.Maps = fm
	router := campaignRoutes(h)
	rec := do(t, router, http.MethodPost, encPath, `{"name":"E","map_region":{"x":-10,"y":20,"w":300,"h":200}}`)
	var enc model.Encounter
	if err := json.Unmarshal(rec.Body.Bytes(), &enc); err != nil || enc.ID == "" {
		t.Fatalf("create encounter: %d %s", rec.Code, rec.Body)
	}
	if enc.MapRegion == nil || *enc.MapRegion != (model.MapRegion{X: -10, Y: 20, W: 300, H: 200}) {
		t.Fatalf("create must store map_region: %+v", enc.MapRegion)
	}
	key := "maps/g1/encounters/" + enc.ID + "/a.png"
	fm.objects[key] = "image/png"
	return router, fm, encPath + "/" + enc.ID, key
}

func putEncounter(t *testing.T, router http.Handler, path, extra string) (int, model.Encounter) {
	t.Helper()
	rec := do(t, router, http.MethodPut, path, `{"name":"E"`+extra+`}`)
	var enc model.Encounter
	_ = json.Unmarshal(rec.Body.Bytes(), &enc)
	return rec.Code, enc
}

func TestEncounterMapRegion_KeepReplaceClearReject(t *testing.T) {
	router, _, path, _ := encMapFixture(t)
	// A save that doesn't send map_region (an older client) must not wipe it.
	if code, enc := putEncounter(t, router, path, ""); code != http.StatusOK || enc.MapRegion == nil || enc.MapRegion.W != 300 {
		t.Fatalf("omit = %d %+v, want region kept", code, enc.MapRegion)
	}
	if code, enc := putEncounter(t, router, path, `,"map_region":{"x":1,"y":2,"w":3,"h":4}`); code != http.StatusOK || *enc.MapRegion != (model.MapRegion{X: 1, Y: 2, W: 3, H: 4}) {
		t.Fatalf("replace = %d %+v", code, enc.MapRegion)
	}
	if code, enc := putEncounter(t, router, path, `,"map_region":null`); code != http.StatusOK || enc.MapRegion != nil {
		t.Fatalf("null = %d %+v, want cleared", code, enc.MapRegion)
	}
	for _, bad := range []string{
		`{"x":0,"y":0,"w":0,"h":4}`,
		`{"x":0,"y":0,"w":4,"h":-1}`,
		`{"x":0,"y":0,"w":131073,"h":4}`,
		`{"x":-131073,"y":0,"w":4,"h":4}`,
		`{"x":0,"y":0,"w":4,"h":4,"z":1}`,
	} {
		if code, _ := putEncounter(t, router, path, `,"map_region":`+bad); code != http.StatusBadRequest {
			t.Errorf("map_region %s = %d, want 400", bad, code)
		}
	}
}

func TestEncounterMapImage_UploadSetPresentRemove(t *testing.T) {
	router, fm, path, key := encMapFixture(t)
	rec := do(t, router, http.MethodPost, path+"/map-image/upload-url", `{"content_type":"image/png"}`)
	var up struct {
		Key string `json:"key"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &up)
	if rec.Code != http.StatusOK || !strings.HasPrefix(up.Key, "maps/g1/encounters/"+strings.TrimPrefix(path, encPath+"/")+"/") {
		t.Fatalf("upload-url = %d key=%q", rec.Code, up.Key)
	}
	if rec := do(t, router, http.MethodPost, encPath+"/nope/map-image/upload-url", `{"content_type":"image/png"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("missing encounter = %d, want 404", rec.Code)
	}

	code, enc := putEncounter(t, router, path, `,"map_image":{"key":"`+key+`","width":640,"height":480}`)
	if code != http.StatusOK || enc.MapImage == nil || enc.MapImage.URL != "/"+key || len(fm.cleared) != 1 {
		t.Fatalf("set = %d %+v cleared=%v", code, enc.MapImage, fm.cleared)
	}
	// Omit keeps it, and get + list present the URL.
	if code, enc = putEncounter(t, router, path, ""); code != http.StatusOK || enc.MapImage == nil {
		t.Fatalf("omit = %d %+v, want kept", code, enc.MapImage)
	}
	var got model.Encounter
	_ = json.Unmarshal(do(t, router, http.MethodGet, path, "").Body.Bytes(), &got)
	var list []model.Encounter
	_ = json.Unmarshal(do(t, router, http.MethodGet, encPath, "").Body.Bytes(), &list)
	if got.MapImage == nil || got.MapImage.URL != "/"+key || len(list) != 1 || list[0].MapImage == nil || list[0].MapImage.URL != "/"+key {
		t.Fatalf("get/list must present url: get=%+v list=%+v", got.MapImage, list)
	}
	if code, enc = putEncounter(t, router, path, `,"map_image":null`); code != http.StatusOK || enc.MapImage != nil {
		t.Fatalf("null = %d %+v, want removed", code, enc.MapImage)
	}
}

func TestEncounterMapImage_ReleasePresentsURL(t *testing.T) {
	router, _, path, key := encMapFixture(t)
	if code, _ := putEncounter(t, router, path, `,"map_image":{"key":"`+key+`","width":1,"height":1},"status":"complete"`); code != http.StatusOK {
		t.Fatalf("set = %d", code)
	}
	rec := do(t, router, http.MethodPost, path+"/release", "")
	var enc model.Encounter
	_ = json.Unmarshal(rec.Body.Bytes(), &enc)
	if rec.Code != http.StatusOK || enc.MapImage == nil || enc.MapImage.URL != "/"+key {
		t.Fatalf("release = %d %+v, want url presented", rec.Code, enc.MapImage)
	}
}

func TestEncounterMapImage_UnconfiguredIs503(t *testing.T) {
	h, _ := newHandler(t, true, 0)
	router := campaignRoutes(h)
	rec := do(t, router, http.MethodPost, encPath, `{"name":"E"}`)
	var enc model.Encounter
	_ = json.Unmarshal(rec.Body.Bytes(), &enc)
	if rec := do(t, router, http.MethodPost, encPath+"/"+enc.ID+"/map-image/upload-url", `{"content_type":"image/png"}`); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("upload-url without maps = %d, want 503", rec.Code)
	}
}

func TestMapRegion_ValidateBounds(t *testing.T) {
	ok := []model.MapRegion{{W: 1, H: 1}, {X: -131072, Y: 131072, W: 131072, H: 131072}}
	bad := []model.MapRegion{{W: 0, H: 1}, {W: 1, H: 131073}, {X: 131073, W: 1, H: 1}, {Y: -131073, W: 1, H: 1}}
	for _, m := range ok {
		if err := m.Validate(); err != nil {
			t.Errorf("%+v: %v, want ok", m, err)
		}
	}
	for _, m := range bad {
		if m.Validate() == nil {
			t.Errorf("%+v: ok, want error", m)
		}
	}
}

func TestEncounterMapImage_Rejects(t *testing.T) {
	router, fm, path, _ := encMapFixture(t)
	// A chapter-map key or another encounter's key can't be claimed by this encounter.
	for _, foreign := range []string{"maps/g1/ch1/a.png", "maps/g1/encounters/other/a.png"} {
		fm.objects[foreign] = "image/png"
		if code, _ := putEncounter(t, router, path, `,"map_image":{"key":"`+foreign+`","width":1,"height":1}`); code != http.StatusBadRequest {
			t.Errorf("foreign key %s = %d, want 400", foreign, code)
		}
	}
	if rec := do(t, router, http.MethodPost, encPath, `{"name":"N","map_image":{"key":"x","width":1,"height":1}}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("create with image = %d, want 400", rec.Code)
	}
}
