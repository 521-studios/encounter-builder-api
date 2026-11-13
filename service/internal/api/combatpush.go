package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/521studios/encounter-builder-api/internal/auth"
	"github.com/521studios/encounter-builder-api/internal/combatpush"
	"github.com/521studios/encounter-builder-api/internal/model"
	"github.com/go-chi/chi/v5"
)

// pushCombat sends the encounter's monsters into the campaign's lets-roll game
// (we5k.2.3) and moves a prepared encounter to executing. lets-roll upserts on
// the encounter id, so a re-push syncs later EB edits instead of duplicating.
// Allowed only from prepared or executing (Devon, 2026-11-13).
func (h *handler) pushCombat(w http.ResponseWriter, r *http.Request) {
	if h.cfg.CombatPush == nil {
		writeJSON(w, http.StatusServiceUnavailable, errBody("lets-roll combat push is not configured"))
		return
	}
	enc, ok := h.loadEncounter(w, r)
	if !ok {
		return
	}
	switch enc.Status.Canonical() {
	case model.StatusPrepared, model.StatusExecuting:
	default:
		writeJSON(w, http.StatusConflict, errBody("only a prepared or executing encounter can be pushed to lets-roll"))
		return
	}
	monsters, legacy := combatpush.MonstersFrom(enc)
	if len(legacy) > 0 {
		// Pre-template elite/weak can't travel in content_ref; the GM re-applies it.
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error":      "re-apply elite/weak as a template on these monsters before pushing",
			"incomplete": legacy,
		})
		return
	}
	if len(monsters) == 0 {
		writeJSON(w, http.StatusUnprocessableEntity, errBody("this encounter has no monsters to push"))
		return
	}

	// Like the release push: once lets-roll has accepted, a client hang-up must
	// not lose the receipt, so detach from the request but bound the whole thing.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 15*time.Second)
	defer cancel()
	rcpt, err := h.cfg.CombatPush.Push(ctx, auth.RawToken(r.Context()), chi.URLParam(r, "campaignID"), combatpush.Input{
		EncounterID: enc.ID,
		Name:        enc.Name,
		Monsters:    monsters,
	})
	if err != nil {
		slog.WarnContext(ctx, "lets-roll combat push failed", "encounter", enc.ID, "campaign", enc.CampaignID, "error", err)
		writeJSON(w, http.StatusBadGateway, errBody("lets-roll did not accept the push; try again"))
		return
	}

	now := time.Now().UTC()
	push := model.CombatPush{At: now, LetsRollEncounterID: rcpt.LetsRollEncounterID}
	for _, k := range rcpt.Kept {
		push.Kept = append(push.Kept, model.KeptMonster{Key: k.Key, Reason: k.Reason})
	}
	enc.CombatPush = &push
	enc.Status = model.StatusExecuting
	enc.UpdatedAt = now
	if err := h.cfg.Store.Put(ctx, enc); err != nil {
		// lets-roll has the encounter; a retry replays it and saves the receipt.
		slog.ErrorContext(ctx, "could not save combat push result", "encounter", enc.ID, "error", err)
		writeJSON(w, http.StatusInternalServerError, errBody("pushed to lets-roll, but could not save the result; push again"))
		return
	}
	writeJSON(w, http.StatusOK, presentEncounter(enc))
}
