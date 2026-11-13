package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/521studios/encounter-builder-api/internal/combatpush"
	"github.com/521studios/encounter-builder-api/internal/model"
)

type stubPusher struct {
	bearer, game string
	in           combatpush.Input
	err          error
}

func (s *stubPusher) Push(_ context.Context, bearer, game string, in combatpush.Input) (combatpush.Receipt, error) {
	s.bearer, s.game, s.in = bearer, game, in
	if s.err != nil {
		return combatpush.Receipt{}, s.err
	}
	return combatpush.Receipt{LetsRollEncounterID: 9, Kept: []combatpush.KeptMonster{{Key: "m1:2", Reason: "damaged"}}}, nil
}

const goblins = `{"name":"Goblins","content":[{"id":"m1","type":"monster","monster":{"ref":{"game_id":"gob"},"count":2,"adjustment":"none"}}]}`

func TestPushCombat(t *testing.T) {
	h, _ := newHandler(t, true, 0)
	pusher := &stubPusher{}
	h.cfg.CombatPush = pusher
	router := campaignRoutes(h)
	create := func(body string) string {
		rec := do(t, router, http.MethodPost, encPath, body)
		var e model.Encounter
		_ = json.Unmarshal(rec.Body.Bytes(), &e)
		return encPath + "/" + e.ID
	}

	base := create(goblins)
	rec := do(t, router, http.MethodPost, base+"/push-combat", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("push = %d %s", rec.Code, rec.Body)
	}
	var got model.Encounter
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Status != model.StatusExecuting || got.CombatPush == nil || got.CombatPush.LetsRollEncounterID != 9 || len(got.CombatPush.Kept) != 1 {
		t.Fatalf("after push: %+v", got)
	}
	if pusher.bearer != "user-bearer" || pusher.game != "g1" || pusher.in.EncounterID != got.ID || len(pusher.in.Monsters) != 2 {
		t.Fatalf("pushed %q %q %+v", pusher.bearer, pusher.game, pusher.in)
	}
	// A re-push from executing is allowed (lets-roll syncs).
	if rec := do(t, router, http.MethodPost, base+"/push-combat", ""); rec.Code != http.StatusOK {
		t.Fatalf("re-push = %d", rec.Code)
	}

	// Closed-out encounters can't be pushed.
	do(t, router, http.MethodPut, base, `{"name":"Goblins","status":"complete"}`)
	if rec := do(t, router, http.MethodPost, base+"/push-combat", ""); rec.Code != http.StatusConflict {
		t.Fatalf("push complete = %d, want 409", rec.Code)
	}
	// Legacy elite/weak and empty rooms are refused before calling lets-roll.
	legacy := create(`{"name":"Old","content":[{"id":"x","type":"monster","monster":{"ref":{"game_id":"ogre"},"count":1,"adjustment":"elite"}}]}`)
	if rec := do(t, router, http.MethodPost, legacy+"/push-combat", ""); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("legacy = %d, want 422", rec.Code)
	}
	if rec := do(t, router, http.MethodPost, create(`{"name":"Empty"}`)+"/push-combat", ""); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("empty = %d, want 422", rec.Code)
	}
	// A lets-roll failure is a 502 and leaves the encounter prepared.
	pusher.err = errors.New("boom")
	failed := create(goblins)
	if rec := do(t, router, http.MethodPost, failed+"/push-combat", ""); rec.Code != http.StatusBadGateway {
		t.Fatalf("failed push = %d, want 502", rec.Code)
	}
	rec = do(t, router, http.MethodGet, failed, "")
	var after model.Encounter
	_ = json.Unmarshal(rec.Body.Bytes(), &after)
	if after.Status != model.StatusPrepared || after.CombatPush != nil {
		t.Fatalf("after failed push: %+v", after)
	}
}

func TestPushCombat_UnconfiguredIs503(t *testing.T) {
	h, _ := newHandler(t, true, 0)
	router := campaignRoutes(h)
	rec := do(t, router, http.MethodPost, encPath, goblins)
	var e model.Encounter
	_ = json.Unmarshal(rec.Body.Bytes(), &e)
	if rec := do(t, router, http.MethodPost, encPath+"/"+e.ID+"/push-combat", ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured = %d, want 503", rec.Code)
	}
}
