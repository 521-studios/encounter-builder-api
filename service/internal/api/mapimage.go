package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/521studios/encounter-builder-api/internal/maps"
	"github.com/521studios/encounter-builder-api/internal/model"
)

// MapStore is the chapter map image store (muz0.1). The handlers depend on this
// interface so tests can stub S3. maps.Store is the real one.
type MapStore interface {
	PresignUpload(ctx context.Context, key, contentType string) (url string, fields map[string]string, err error)
	ContentType(ctx context.Context, key string) (string, error)
	ClearPending(ctx context.Context, key string) error
}

var mapImageExt = map[string]string{"image/png": "png", "image/jpeg": "jpg", "image/webp": "webp"}

func mapKeyPrefix(campaignID, chapterID string) string {
	return "maps/" + campaignID + "/" + chapterID + "/"
}

// mapImageUploadURL presigns a browser POST of one map image for an existing chapter.
func (h *handler) mapImageUploadURL(w http.ResponseWriter, r *http.Request) {
	if h.cfg.Maps == nil {
		writeJSON(w, http.StatusServiceUnavailable, errBody("map uploads are not configured"))
		return
	}
	ch, ok := h.loadChapter(w, r)
	if !ok {
		return
	}
	var in struct {
		ContentType string `json:"content_type"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	ext, ok := mapImageExt[in.ContentType]
	if !ok {
		writeJSON(w, http.StatusBadRequest, errBody("content_type must be image/png, image/jpeg or image/webp"))
		return
	}
	key := mapKeyPrefix(ch.CampaignID, ch.ID) + newID() + "." + ext
	url, fields, err := h.cfg.Maps.PresignUpload(r.Context(), key, in.ContentType)
	if err != nil {
		slog.Error("presign map upload", "key", key, "err", err)
		writeJSON(w, http.StatusInternalServerError, errBody("could not create upload"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"key": key, "url": url, "fields": fields})
}

// applyMapImage applies a map_image patch to ch: omit keeps it, null removes it, and
// an object sets it. Only a new key is checked against S3 (it must exist with an
// image content type) and has its pending tag cleared. A transform-only change is a
// plain write. On failure it writes the error response and returns false.
func (h *handler) applyMapImage(w http.ResponseWriter, r *http.Request, ch *model.Chapter, patch model.MapImagePatch) bool {
	if !patch.Set {
		return true
	}
	if patch.Value == nil {
		ch.MapImage = nil
		return true
	}
	img, err := patch.Value.Resolve(mapKeyPrefix(ch.CampaignID, ch.ID))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
		return false
	}
	if ch.MapImage == nil || ch.MapImage.Key != img.Key {
		if h.cfg.Maps == nil {
			writeJSON(w, http.StatusServiceUnavailable, errBody("map uploads are not configured"))
			return false
		}
		ct, err := h.cfg.Maps.ContentType(r.Context(), img.Key)
		if errors.Is(err, maps.ErrNotFound) {
			writeJSON(w, http.StatusBadRequest, errBody("map_image.key has not been uploaded"))
			return false
		}
		if err != nil {
			slog.Error("head map image", "key", img.Key, "err", err)
			writeJSON(w, http.StatusBadGateway, errBody("could not check map image"))
			return false
		}
		if _, ok := mapImageExt[ct]; !ok {
			writeJSON(w, http.StatusBadRequest, errBody("map_image.key is not a png, jpeg or webp image"))
			return false
		}
		if err := h.cfg.Maps.ClearPending(r.Context(), img.Key); err != nil {
			slog.Error("clear map image pending tag", "key", img.Key, "err", err)
			writeJSON(w, http.StatusBadGateway, errBody("could not keep map image"))
			return false
		}
	}
	// ponytail: a replaced or removed image's old object stays in the bucket (and so
	// does a deleted chapter's). Add a DeleteObject here and in deleteChapter if
	// storage cost ever matters.
	ch.MapImage = &img
	return true
}

// presentChapter adds the read-only map image URL. CloudFront serves /maps/* from
// the bucket on the SPA's own origin, so the URL is the key's path.
func presentChapter(ch model.Chapter) model.Chapter {
	if ch.MapImage != nil {
		img := *ch.MapImage
		img.URL = "/" + img.Key
		ch.MapImage = &img
	}
	return ch
}

// Compile-time check that the real store satisfies the interface.
var _ MapStore = (*maps.Store)(nil)
