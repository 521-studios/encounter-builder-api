package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/521studios/encounter-builder-api/internal/auth"
	"github.com/521studios/encounter-builder-api/internal/letsroll"
	"github.com/521studios/encounter-builder-api/internal/model"
	"github.com/521studios/encounter-builder-api/internal/store"
	"github.com/go-chi/chi/v5"
)

// settingsRoutes mounts the campaign-settings subtree with requireGM, seeding a
// verified-token context (mirrors chapterRoutes).
func settingsRoutes(h *handler) http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(auth.WithRawToken(req.Context(), "user-bearer")))
		})
	})
	r.Route("/api/app/campaigns/{campaignID}/settings", func(r chi.Router) {
		r.Use(h.requireGM)
		r.Get("/", h.getSettings)
		r.Put("/", h.putSettings)
	})
	return r
}

const setPath = "/api/app/campaigns/g1/settings"

func TestSettings_RequireGMRejectsNonGM(t *testing.T) {
	h, _ := newHandler(t, false, 0)
	rec := do(t, settingsRoutes(h), http.MethodPut, setPath, `{"party_level":3}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-GM put = %d, want 403", rec.Code)
	}
}

// Unset settings read back as an empty object (200) so the client always has a
// shape to resolve inheritance against — no 404 special-casing.
func TestSettings_GetDefaultsWhenUnset(t *testing.T) {
	h, _ := newHandler(t, true, 0)
	rec := do(t, settingsRoutes(h), http.MethodGet, setPath, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get unset = %d, want 200; body=%s", rec.Code, rec.Body)
	}
	var cs model.CampaignSettings
	_ = json.Unmarshal(rec.Body.Bytes(), &cs)
	if cs.CampaignID != "g1" || cs.PartyLevel != nil || cs.PartySize != nil {
		t.Fatalf("unset settings = %+v, want {g1, nil, nil}", cs)
	}
}

func TestSettings_PutThenGet(t *testing.T) {
	h, _ := newHandler(t, true, 0)
	router := settingsRoutes(h)

	rec := do(t, router, http.MethodPut, setPath, `{"party_level":5,"party_size":4}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put = %d, want 200; body=%s", rec.Code, rec.Body)
	}
	rec = do(t, router, http.MethodGet, setPath, "")
	var cs model.CampaignSettings
	_ = json.Unmarshal(rec.Body.Bytes(), &cs)
	if cs.PartyLevel == nil || *cs.PartyLevel != 5 || cs.PartySize == nil || *cs.PartySize != 4 {
		t.Fatalf("settings after put = %+v, want level 5 size 4", cs)
	}
	if cs.UpdatedAt == nil {
		t.Fatalf("put did not stamp UpdatedAt")
	}
}

func TestSettings_PutValidationRejects(t *testing.T) {
	h, _ := newHandler(t, true, 0)
	router := settingsRoutes(h)
	for _, body := range []string{`{"party_level":0}`, `{"party_level":21}`, `{"party_size":0}`} {
		if rec := do(t, router, http.MethodPut, setPath, body); rec.Code != http.StatusBadRequest {
			t.Fatalf("put %s = %d, want 400", body, rec.Code)
		}
	}
}

func TestSettingsHandlers_StoreErrorsAre500(t *testing.T) {
	srv := gmServer(t, true, 0)
	t.Cleanup(srv.Close)
	h := &handler{cfg: Config{Env: "test", Store: store.New(errDynamo{}, "t"), LetsRoll: letsroll.New(srv.URL)}}
	router := settingsRoutes(h)

	cases := map[string]struct{ method, body string }{
		"get": {http.MethodGet, ""},
		"put": {http.MethodPut, `{"party_level":3}`},
	}
	for name, tc := range cases {
		if rec := do(t, router, tc.method, setPath, tc.body); rec.Code != http.StatusInternalServerError {
			t.Fatalf("%s under store error = %d, want 500", name, rec.Code)
		}
	}
}

// Party overrides on an encounter round-trip, and an omitted override persists
// as nil (inherit) rather than 0 — the whole point of the pointer field.
func TestEncounter_PartyOverridePersistsAndOmittedIsNil(t *testing.T) {
	h, _ := newHandler(t, true, 0)
	router := campaignRoutes(h)

	rec := do(t, router, http.MethodPost, encPath, `{"name":"With override","party_level":3,"party_size":5}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201; body=%s", rec.Code, rec.Body)
	}
	var withOverride model.Encounter
	_ = json.Unmarshal(rec.Body.Bytes(), &withOverride)
	if withOverride.PartyLevel == nil || *withOverride.PartyLevel != 3 || withOverride.PartySize == nil || *withOverride.PartySize != 5 {
		t.Fatalf("override not persisted: %+v", withOverride)
	}

	rec = do(t, router, http.MethodPost, encPath, `{"name":"Inherits"}`)
	var inherits model.Encounter
	_ = json.Unmarshal(rec.Body.Bytes(), &inherits)
	if inherits.PartyLevel != nil || inherits.PartySize != nil {
		t.Fatalf("omitted party fields should be nil (inherit), got %+v", inherits)
	}

	// Clearing an override: the encounter PUT sends full state, so a nil field
	// clears it back to inherit.
	rec = do(t, router, http.MethodPut, encPath+"/"+withOverride.ID, `{"name":"With override"}`)
	var cleared model.Encounter
	_ = json.Unmarshal(rec.Body.Bytes(), &cleared)
	if cleared.PartyLevel != nil {
		t.Fatalf("PUT without party_level should clear the override, got %+v", cleared)
	}
}

func TestEncounter_PartyValidationRejects(t *testing.T) {
	h, _ := newHandler(t, true, 0)
	rec := do(t, campaignRoutes(h), http.MethodPost, encPath, `{"name":"bad","party_level":99}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("out-of-range party_level = %d, want 400", rec.Code)
	}
}

// Chapter party defaults are full-replaced (like the encounter's): a
// round-tripping update preserves them, and omitting them clears back to inherit.
func TestChapter_PartyDefaultsFullReplace(t *testing.T) {
	h, _ := newHandler(t, true, 0)
	router := chapterRoutes(h)

	rec := do(t, router, http.MethodPost, chPath, `{"name":"Ch","party_level":4,"party_size":4}`)
	var ch model.Chapter
	_ = json.Unmarshal(rec.Body.Bytes(), &ch)
	if ch.PartyLevel == nil || *ch.PartyLevel != 4 {
		t.Fatalf("chapter party_level not persisted: %+v", ch)
	}

	// Round-trip (the frontend rename sends the current party fields) preserves.
	rec = do(t, router, http.MethodPut, chPath+"/"+ch.ID, `{"name":"Ch renamed","party_level":4,"party_size":4}`)
	var renamed model.Chapter
	_ = json.Unmarshal(rec.Body.Bytes(), &renamed)
	if renamed.Name != "Ch renamed" || renamed.PartyLevel == nil || *renamed.PartyLevel != 4 {
		t.Fatalf("round-trip update lost party defaults: %+v", renamed)
	}

	// Omitting the party fields clears the override back to inherit (full-replace).
	rec = do(t, router, http.MethodPut, chPath+"/"+ch.ID, `{"name":"Ch"}`)
	var cleared model.Chapter
	_ = json.Unmarshal(rec.Body.Bytes(), &cleared)
	if cleared.PartyLevel != nil || cleared.PartySize != nil {
		t.Fatalf("omitting party fields should clear to inherit, got %+v", cleared)
	}
}

// Chapter party validation is enforced at the HTTP boundary too (ChapterInput
// routes through validateParty), not just in the unit test.
func TestChapter_PartyValidationRejects(t *testing.T) {
	h, _ := newHandler(t, true, 0)
	rec := do(t, chapterRoutes(h), http.MethodPost, chPath, `{"name":"bad","party_level":99}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("out-of-range chapter party_level = %d, want 400", rec.Code)
	}
}

// The set-from-nil branch of updateChapter: supplying party fields on a PUT to a
// fresh chapter applies them (complements TestChapter_PartyDefaultsFullReplace's
// round-trip-preserve and omit-clear paths).
func TestChapter_UpdateSetsPartyDefaults(t *testing.T) {
	h, _ := newHandler(t, true, 0)
	router := chapterRoutes(h)
	rec := do(t, router, http.MethodPost, chPath, `{"name":"Ch"}`)
	var ch model.Chapter
	_ = json.Unmarshal(rec.Body.Bytes(), &ch)
	if ch.PartyLevel != nil {
		t.Fatalf("fresh chapter should have nil party_level, got %+v", ch)
	}
	rec = do(t, router, http.MethodPut, chPath+"/"+ch.ID, `{"name":"Ch","party_level":6,"party_size":3}`)
	var up model.Chapter
	_ = json.Unmarshal(rec.Body.Bytes(), &up)
	if up.PartyLevel == nil || *up.PartyLevel != 6 || up.PartySize == nil || *up.PartySize != 3 {
		t.Fatalf("update did not set party defaults: %+v", up)
	}
}

// Settings PUT is a full replace: omitting a field clears it back to nil (unlike
// chapter update). Confirms the documented clear-on-omit semantics.
func TestSettings_PutClearsOmittedField(t *testing.T) {
	h, _ := newHandler(t, true, 0)
	router := settingsRoutes(h)
	do(t, router, http.MethodPut, setPath, `{"party_level":5,"party_size":4}`)
	rec := do(t, router, http.MethodPut, setPath, `{"party_level":5}`)
	var cs model.CampaignSettings
	_ = json.Unmarshal(rec.Body.Bytes(), &cs)
	if cs.PartyLevel == nil || *cs.PartyLevel != 5 || cs.PartySize != nil {
		t.Fatalf("PUT should clear omitted party_size, got %+v", cs)
	}
}
