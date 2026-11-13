package api

import (
	"net/http"
	"time"

	"github.com/521studios/encounter-builder-api/internal/model"
)

// markKill is the GM marking one creature killed or alive in EB (we5k.2.4).
// The server stamps the change, so it wins over any older lets-roll report.
func (h *handler) markKill(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Key    string `json:"eb_monster_key"`
		Killed bool   `json:"killed"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	enc, ok := h.loadEncounter(w, r)
	if !ok {
		return
	}
	if enc.Status == model.StatusReleased {
		writeJSON(w, http.StatusConflict, errBody("this encounter's treasure is already released"))
		return
	}
	if !enc.HasCreature(in.Key) {
		writeJSON(w, http.StatusUnprocessableEntity, errBody("no such creature in this encounter"))
		return
	}
	now := time.Now().UTC()
	enc.MarkKill(in.Key, model.Kill{Killed: in.Killed, At: now, Source: model.KillSourceEB})
	enc.UpdatedAt = now
	if err := h.cfg.Store.Put(r.Context(), enc); err != nil {
		writeJSON(w, http.StatusInternalServerError, errBody("could not save encounter"))
		return
	}
	writeJSON(w, http.StatusOK, presentEncounter(enc))
}

// killReport is one creature's change as lets-roll saw it: killed when the GM
// took it to 0 HP, alive again when it went back above 0.
type killReport struct {
	Key    string    `json:"eb_monster_key"`
	Killed bool      `json:"killed"`
	At     time.Time `json:"at"`
}

// killReports takes kills reported by lets-roll (we5k.2.4). Only lets-roll's
// service identity reaches it (lrident middleware), never a user token. Each
// report applies only if newer than what EB holds (an equal at is stale), and
// only while the encounter is executing: once it's closed out the treasure
// package is built from its kills, so a late report is a 409.
func (h *handler) killReports(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Kills []killReport `json:"kills"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	for _, k := range in.Kills {
		if k.Key == "" || k.At.IsZero() {
			writeJSON(w, http.StatusBadRequest, errBody("each kill needs eb_monster_key and at"))
			return
		}
	}
	enc, ok := h.loadEncounter(w, r)
	if !ok {
		return
	}
	if enc.Status.Canonical() != model.StatusExecuting {
		writeJSON(w, http.StatusConflict, errBody("kills are reported only while the encounter is executing"))
		return
	}
	results := make(map[string]string, len(in.Kills))
	changed := false
	for _, k := range in.Kills {
		switch {
		case !enc.HasCreature(k.Key):
			results[k.Key] = "unknown"
		case enc.MarkKill(k.Key, model.Kill{Killed: k.Killed, At: k.At.UTC(), Source: model.KillSourceLetsRoll}):
			results[k.Key], changed = "applied", true
		default:
			results[k.Key] = "stale"
		}
	}
	if changed {
		enc.UpdatedAt = time.Now().UTC()
		if err := h.cfg.Store.Put(r.Context(), enc); err != nil {
			writeJSON(w, http.StatusInternalServerError, errBody("could not save encounter"))
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}
