package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/521studios/encounter-builder-api/internal/model"
	"github.com/521studios/encounter-builder-api/internal/store"
	"github.com/go-chi/chi/v5"
)

// Chapters are campaign subdivisions that group encounters in the GM sidebar.
// Same requireGM gate as encounters — they live under the same campaign scope.

func (h *handler) createChapter(w http.ResponseWriter, r *http.Request) {
	var in model.ChapterInput
	if !decodeBody(w, r, &in) {
		return
	}
	if err := in.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
		return
	}
	now := time.Now().UTC()
	ch := model.Chapter{
		ID:         newID(),
		CampaignID: chi.URLParam(r, "campaignID"),
		Name:       in.Name,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if in.Order != nil {
		ch.Order = *in.Order
	}
	if err := h.cfg.Store.PutChapter(r.Context(), ch); err != nil {
		writeJSON(w, http.StatusInternalServerError, errBody("could not save chapter"))
		return
	}
	writeJSON(w, http.StatusCreated, ch)
}

func (h *handler) listChapters(w http.ResponseWriter, r *http.Request) {
	list, err := h.cfg.Store.ListChapters(r.Context(), chi.URLParam(r, "campaignID"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errBody("could not list chapters"))
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// updateChapter renames and/or reorders a chapter (both drive the sidebar).
// Order is only touched when supplied, so a rename can't accidentally reset it.
func (h *handler) updateChapter(w http.ResponseWriter, r *http.Request) {
	ch, ok := h.loadChapter(w, r)
	if !ok {
		return
	}
	var in model.ChapterInput
	if !decodeBody(w, r, &in) {
		return
	}
	if err := in.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
		return
	}
	ch.Name = in.Name
	if in.Order != nil {
		ch.Order = *in.Order
	}
	ch.UpdatedAt = time.Now().UTC()
	if err := h.cfg.Store.PutChapter(r.Context(), ch); err != nil {
		writeJSON(w, http.StatusInternalServerError, errBody("could not save chapter"))
		return
	}
	writeJSON(w, http.StatusOK, ch)
}

func (h *handler) deleteChapter(w http.ResponseWriter, r *http.Request) {
	if err := h.cfg.Store.DeleteChapter(r.Context(), chi.URLParam(r, "campaignID"), chi.URLParam(r, "chapterID")); err != nil {
		writeJSON(w, http.StatusInternalServerError, errBody("could not delete chapter"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// loadChapter fetches the path-addressed chapter, writing 404/500 and returning
// ok=false when it can't be served.
func (h *handler) loadChapter(w http.ResponseWriter, r *http.Request) (model.Chapter, bool) {
	ch, err := h.cfg.Store.GetChapter(r.Context(), chi.URLParam(r, "campaignID"), chi.URLParam(r, "chapterID"))
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, errBody("chapter not found"))
		return model.Chapter{}, false
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errBody("could not load chapter"))
		return model.Chapter{}, false
	}
	return ch, true
}
