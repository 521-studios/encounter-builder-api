package api

import (
	"context"
	"encoding/json"
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
}

func (f *fakeMaps) PresignUpload(_ context.Context, key, ct string) (string, map[string]string, error) {
	return "https://bucket.test", map[string]string{"key": key, "Content-Type": ct}, nil
}

func (f *fakeMaps) ContentType(_ context.Context, key string) (string, error) {
	f.heads = append(f.heads, key)
	ct, ok := f.objects[key]
	if !ok {
		return "", maps.ErrNotFound
	}
	return ct, nil
}

func (f *fakeMaps) ClearPending(_ context.Context, key string) error {
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
