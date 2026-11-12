package api

import (
	"context"
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
	"github.com/521studios/encounter-builder-api/internal/partytreasure"
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
		ID:              newID(),
		CampaignID:      chi.URLParam(r, "campaignID"),
		Name:            in.Name,
		Status:          model.StatusPrepared, // new encounters always start prepared
		ChapterID:       in.ChapterID,
		Description:     in.Description,
		TextBlocks:      in.TextBlocks,
		ChallengeBlocks: in.ChallengeBlocks,
		Notes:           in.Notes,
		CloseoutNotes:   in.CloseoutNotes,
		Monsters:        in.Monsters,
		Hazards:         in.Hazards,
		Afflictions:     in.Afflictions,
		Treasure:        in.Treasure,
		TreasurePools:   in.TreasurePools,
		XPAwards:        in.XPAwards,
		RoomType:        in.RoomType,
		Rewards:         in.Rewards,
		SkillChecks:     in.SkillChecks,
		Challenges:      in.Challenges,
		Content:         in.Content,
		Exits:           in.Exits,
		Currency:        in.Currency,
		PartyLevel:      in.PartyLevel,
		PartySize:       in.PartySize,
		MapRegion:       in.MapRegion.Value,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	// An upload needs the encounter's ID, so a new encounter can't carry an image yet.
	if in.MapImage.Value != nil {
		writeJSON(w, http.StatusBadRequest, errBody("map_image can only be set on an existing encounter"))
		return
	}
	if err := h.cfg.Store.Put(r.Context(), enc); err != nil {
		writeJSON(w, http.StatusInternalServerError, errBody("could not save encounter"))
		return
	}
	writeJSON(w, http.StatusCreated, presentEncounter(enc))
}

func (h *handler) listEncounters(w http.ResponseWriter, r *http.Request) {
	list, err := h.cfg.Store.List(r.Context(), chi.URLParam(r, "campaignID"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errBody("could not list encounters"))
		return
	}
	for i := range list {
		list[i] = presentEncounter(list[i])
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *handler) getEncounter(w http.ResponseWriter, r *http.Request) {
	enc, ok := h.loadEncounter(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, presentEncounter(enc))
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
	// Update may move freely among prepared/executing/complete (latest change
	// wins), never to released (that's the /release endpoint). Empty = as-is.
	switch in.Status.Canonical() {
	case "", model.StatusPrepared, model.StatusExecuting, model.StatusComplete:
	default:
		writeJSON(w, http.StatusBadRequest, errBody("status must be prepared, executing or complete"))
		return
	}
	existing.Name = in.Name
	existing.ChapterID = in.ChapterID
	existing.Description = in.Description
	existing.TextBlocks = in.TextBlocks
	existing.ChallengeBlocks = in.ChallengeBlocks
	existing.Notes = in.Notes
	existing.CloseoutNotes = in.CloseoutNotes
	existing.Monsters = in.Monsters
	existing.Hazards = in.Hazards
	existing.Afflictions = in.Afflictions
	existing.Treasure = in.Treasure
	existing.TreasurePools = in.TreasurePools
	existing.XPAwards = in.XPAwards
	existing.RoomType = in.RoomType
	existing.Rewards = in.Rewards
	existing.SkillChecks = in.SkillChecks
	existing.Challenges = in.Challenges
	existing.Content = in.Content
	existing.Exits = in.Exits
	existing.Currency = in.Currency
	existing.PartyLevel = in.PartyLevel
	existing.PartySize = in.PartySize
	now := time.Now().UTC()
	if in.Status != "" {
		was := existing.Status.Canonical()
		existing.Status = in.Status.Canonical()
		if existing.Status != model.StatusComplete {
			existing.CompletedAt = nil
		} else if was != model.StatusComplete {
			existing.CompletedAt = &now
		}
	}
	if in.MapRegion.Set {
		existing.MapRegion = in.MapRegion.Value
	}
	if !h.applyMapImage(w, r, &existing.MapImage, encounterMapKeyPrefix(existing.CampaignID, existing.ID), in.MapImage) {
		return
	}
	existing.UpdatedAt = now

	if err := h.cfg.Store.Put(r.Context(), existing); err != nil {
		writeJSON(w, http.StatusInternalServerError, errBody("could not save encounter"))
		return
	}
	writeJSON(w, http.StatusOK, presentEncounter(existing))
}

func (h *handler) deleteEncounter(w http.ResponseWriter, r *http.Request) {
	if err := h.cfg.Store.Delete(r.Context(), chi.URLParam(r, "campaignID"), chi.URLParam(r, "encounterID")); err != nil {
		writeJSON(w, http.StatusInternalServerError, errBody("could not delete encounter"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// releaseEncounter marks loot as handed to the party and, when party-treasure is
// configured, pushes it into the party pool.
func (h *handler) releaseEncounter(w http.ResponseWriter, r *http.Request) {
	enc, ok := h.loadEncounter(w, r)
	if !ok {
		return
	}
	if enc.Status == model.StatusReleased {
		writeJSON(w, http.StatusConflict, errBody("encounter already released"))
		return
	}
	// Draft→done completeness gate (rvd4): releasing is where half-filled content
	// rows must be finished (save-time validation is only structural). Block the
	// release with the list of gaps so the client can point the GM at each, unless
	// ?force=true — an explicit "release anyway" for intentionally-sparse rooms.
	if r.URL.Query().Get("force") != "true" {
		if gaps := model.IncompleteContent(enc.Content); len(gaps) > 0 {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
				"error":      "encounter has unfinished content; finish it or release with ?force=true",
				"incomplete": gaps,
			})
			return
		}
	}
	now := time.Now().UTC()
	enc.Status = model.StatusReleased
	enc.ReleasedAt = &now
	enc.UpdatedAt = now
	if err := h.cfg.Store.Put(r.Context(), enc); err != nil {
		writeJSON(w, http.StatusInternalServerError, errBody("could not release encounter"))
		return
	}
	// Release is committed. Best-effort push the loot to party-treasure (§5b) and
	// record the outcome on the encounter; a failed push does NOT fail the release —
	// the GM sees the error and can Retry.
	if h.cfg.PartyTreasure != nil {
		h.pushRelease(r, &enc)
	}
	writeJSON(w, http.StatusOK, presentEncounter(enc))
}

// retryReleasePush re-pushes a released encounter's loot after a failed (or
// unknown) push. Same client_ulid (the encounter id), so party-treasure replays
// rather than duplicates a push that actually landed.
func (h *handler) retryReleasePush(w http.ResponseWriter, r *http.Request) {
	if h.cfg.PartyTreasure == nil {
		writeJSON(w, http.StatusServiceUnavailable, errBody("party-treasure is not configured"))
		return
	}
	enc, ok := h.loadEncounter(w, r)
	if !ok {
		return
	}
	if enc.Status != model.StatusReleased {
		writeJSON(w, http.StatusConflict, errBody("encounter is not released"))
		return
	}
	h.pushRelease(r, &enc)
	writeJSON(w, http.StatusOK, presentEncounter(enc))
}

// pushRelease sends enc's loot to party-treasure and stores the receipt or error
// on enc.ReleasePush. A failure to persist that record is logged, not returned:
// the response still carries it, and a Retry rewrites it.
func (h *handler) pushRelease(r *http.Request, enc *model.Encounter) {
	// A client that hangs up mid-push must not lose the receipt of a push that
	// landed, so detach from the request, but still bound the push + Put.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 15*time.Second)
	defer cancel()
	items, coins := partytreasure.ReleasePayloadFromEncounter(*enc)
	rcpt, err := h.cfg.PartyTreasure.PushRelease(ctx, enc.CampaignID, partytreasure.ReleaseInput{
		ClientULID: enc.ID,
		Items:      items,
		Coins:      coins,
	})
	push := model.ReleasePush{At: time.Now().UTC(), LedgerID: rcpt.LedgerID, ItemCount: rcpt.ItemCount}
	if err != nil {
		slog.WarnContext(ctx, "party-treasure push failed",
			"encounter", enc.ID, "campaign", enc.CampaignID, "error", err)
		push.Error = err.Error() // rcpt is zero on error
	}
	enc.ReleasePush = &push
	if err := h.cfg.Store.Put(ctx, *enc); err != nil {
		slog.ErrorContext(ctx, "could not save release push result",
			"encounter", enc.ID, "campaign", enc.CampaignID, "error", err)
	}
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
