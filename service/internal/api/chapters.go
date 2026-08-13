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
	ch.PartyLevel = in.PartyLevel
	ch.PartySize = in.PartySize
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

// updateChapter renames and/or reorders a chapter (both drive the sidebar), and
// sets its expected-party defaults. Per-field update semantics differ by design:
// Order is touch-only-when-supplied (omit = unchanged) for rename back-compat,
// while PartyLevel/PartySize are full-replaced (omit = clear-to-inherit) to match
// the encounter + settings writes — so partial callers MUST round-trip the party
// fields (see the block comment below).
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
	// Party defaults are full-replaced (like the encounter's), so a nil field
	// clears the chapter's override back to inherit from campaign settings — the
	// clear-to-inherit path the detail page needs. Callers that send partial
	// updates (the sidebar rename) MUST round-trip party_level/party_size, or a
	// rename would wipe them. Order stays touch-only-when-supplied for back-compat
	// with the existing rename; the frontend round-trips both.
	ch.PartyLevel = in.PartyLevel
	ch.PartySize = in.PartySize
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
