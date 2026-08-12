package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/521studios/encounter-builder-api/internal/auth"
	"github.com/521studios/encounter-builder-api/internal/letsroll"
	"github.com/521studios/encounter-builder-api/internal/model"
	"github.com/521studios/encounter-builder-api/internal/store"
	"github.com/go-chi/chi/v5"
)

// requireGM gates the campaign routes: it asks lets-roll — as the caller,
// forwarding their bearer — whether they run this game. Non-GMs and non-members
// get 403; a lets-roll outage is a 502, never a silent allow.
func (h *handler) requireGM(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		campaignID := chi.URLParam(r, "campaignID")
		if campaignID == "" {
			writeJSON(w, http.StatusBadRequest, errBody("campaign id is required"))
			return
		}
		bearer := auth.RawToken(r.Context())
		if bearer == "" { // shouldn't happen behind the auth middleware
			writeJSON(w, http.StatusUnauthorized, errBody("missing token"))
			return
		}
		game, err := h.cfg.LetsRoll.FetchGame(r.Context(), bearer, campaignID)
		if errors.Is(err, letsroll.ErrForbidden) {
			writeJSON(w, http.StatusForbidden, errBody("not a member of this campaign"))
			return
		}
		if err != nil {
			// Log the real cause — a 502 here is otherwise a black box (e.g. a
			// decode mismatch or a lets-roll outage looks identical to the client).
			slog.ErrorContext(r.Context(), "requireGM: lets-roll membership check failed",
				"campaign", campaignID, "error", err)
			writeJSON(w, http.StatusBadGateway, errBody("could not verify campaign membership"))
			return
		}
		if !game.AmGM {
			writeJSON(w, http.StatusForbidden, errBody("only the GM can manage encounters"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *handler) createEncounter(w http.ResponseWriter, r *http.Request) {
	var in model.EncounterInput
	if !decodeBody(w, r, &in) {
		return
	}
	if err := in.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
		return
	}
	now := time.Now().UTC()
	enc := model.Encounter{
		ID:         newID(),
		CampaignID: chi.URLParam(r, "campaignID"),
		Name:       in.Name,
		Status:     model.StatusDraft, // new encounters always start in draft
		Notes:      in.Notes,
		Monsters:   in.Monsters,
		Treasure:   in.Treasure,
		Currency:   in.Currency,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := h.cfg.Store.Put(r.Context(), enc); err != nil {
		writeJSON(w, http.StatusInternalServerError, errBody("could not save encounter"))
		return
	}
	writeJSON(w, http.StatusCreated, enc)
}

func (h *handler) listEncounters(w http.ResponseWriter, r *http.Request) {
	list, err := h.cfg.Store.List(r.Context(), chi.URLParam(r, "campaignID"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errBody("could not list encounters"))
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *handler) getEncounter(w http.ResponseWriter, r *http.Request) {
	enc, ok := h.loadEncounter(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, enc)
}

func (h *handler) updateEncounter(w http.ResponseWriter, r *http.Request) {
	existing, ok := h.loadEncounter(w, r)
	if !ok {
		return
	}
	if existing.Status == model.StatusReleased {
		writeJSON(w, http.StatusConflict, errBody("encounter is released and can no longer be edited"))
		return
	}
	var in model.EncounterInput
	if !decodeBody(w, r, &in) {
		return
	}
	if err := in.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
		return
	}
	// Status transitions are limited: update may move draft<->run, never to
	// released (that's the /release endpoint). Empty means "leave as-is".
	if in.Status != "" && in.Status != model.StatusDraft && in.Status != model.StatusRun {
		writeJSON(w, http.StatusBadRequest, errBody("status must be draft or run"))
		return
	}
	existing.Name = in.Name
	existing.Notes = in.Notes
	existing.Monsters = in.Monsters
	existing.Treasure = in.Treasure
	existing.Currency = in.Currency
	if in.Status != "" {
		existing.Status = in.Status
	}
	existing.UpdatedAt = time.Now().UTC()

	if err := h.cfg.Store.Put(r.Context(), existing); err != nil {
		writeJSON(w, http.StatusInternalServerError, errBody("could not save encounter"))
		return
	}
	writeJSON(w, http.StatusOK, existing)
}

func (h *handler) deleteEncounter(w http.ResponseWriter, r *http.Request) {
	if err := h.cfg.Store.Delete(r.Context(), chi.URLParam(r, "campaignID"), chi.URLParam(r, "encounterID")); err != nil {
		writeJSON(w, http.StatusInternalServerError, errBody("could not delete encounter"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// releaseEncounter marks loot as handed to the party. The browser-mediated
// handshake to party-treasure-api comes later (§5); this records the state.
func (h *handler) releaseEncounter(w http.ResponseWriter, r *http.Request) {
	enc, ok := h.loadEncounter(w, r)
	if !ok {
		return
	}
	if enc.Status == model.StatusReleased {
		writeJSON(w, http.StatusConflict, errBody("encounter already released"))
		return
	}
	now := time.Now().UTC()
	enc.Status = model.StatusReleased
	enc.ReleasedAt = &now
	enc.UpdatedAt = now
	if err := h.cfg.Store.Put(r.Context(), enc); err != nil {
		writeJSON(w, http.StatusInternalServerError, errBody("could not release encounter"))
		return
	}
	writeJSON(w, http.StatusOK, enc)
}

// loadEncounter fetches the path-addressed encounter, writing 404/500 and
// returning ok=false when it can't be served.
func (h *handler) loadEncounter(w http.ResponseWriter, r *http.Request) (model.Encounter, bool) {
	enc, err := h.cfg.Store.Get(r.Context(), chi.URLParam(r, "campaignID"), chi.URLParam(r, "encounterID"))
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, errBody("encounter not found"))
		return model.Encounter{}, false
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errBody("could not load encounter"))
		return model.Encounter{}, false
	}
	return enc, true
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)) // 1 MiB cap
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid JSON body: "+err.Error()))
		return false
	}
	return true
}

func errBody(msg string) map[string]string { return map[string]string{"error": msg} }

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand never fails on Linux; ignore per stdlib norm
	return hex.EncodeToString(b[:])
}
