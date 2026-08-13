package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/521studios/encounter-builder-api/internal/model"
	"github.com/521studios/encounter-builder-api/internal/store"
	"github.com/go-chi/chi/v5"
)

// Campaign settings hold the base of the expected-party inheritance chain
// (campaign -> chapter -> encounter). Same requireGM gate as encounters/chapters.

// getSettings returns the campaign's expected-party defaults. When none are
// saved yet it returns an empty settings object (200) rather than 404, so the
// client always has a shape to read and resolve inheritance against.
func (h *handler) getSettings(w http.ResponseWriter, r *http.Request) {
	campaignID := chi.URLParam(r, "campaignID")
	cs, err := h.cfg.Store.GetSettings(r.Context(), campaignID)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusOK, model.CampaignSettings{CampaignID: campaignID})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errBody("could not load settings"))
		return
	}
	writeJSON(w, http.StatusOK, cs)
}

// putSettings replaces the campaign's expected-party defaults (PUT = full
// replace; a nil field clears that default back to unset).
func (h *handler) putSettings(w http.ResponseWriter, r *http.Request) {
	var in model.CampaignSettingsInput
	if !decodeBody(w, r, &in) {
		return
	}
	if err := in.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
		return
	}
	now := time.Now().UTC()
	cs := model.CampaignSettings{
		CampaignID: chi.URLParam(r, "campaignID"),
		PartyLevel: in.PartyLevel,
		PartySize:  in.PartySize,
		UpdatedAt:  &now,
	}
	if err := h.cfg.Store.PutSettings(r.Context(), cs); err != nil {
		writeJSON(w, http.StatusInternalServerError, errBody("could not save settings"))
		return
	}
	writeJSON(w, http.StatusOK, cs)
}
