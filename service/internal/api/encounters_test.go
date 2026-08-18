package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/521studios/encounter-builder-api/internal/auth"
	"github.com/521studios/encounter-builder-api/internal/letsroll"
	"github.com/521studios/encounter-builder-api/internal/model"
	"github.com/521studios/encounter-builder-api/internal/store"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/go-chi/chi/v5"
)

// --- in-memory DynamoAPI (mirrors store's own test fake) ---

type memDynamo struct {
	items map[string]map[string]types.AttributeValue
}

func newMem() *memDynamo {
	return &memDynamo{items: map[string]map[string]types.AttributeValue{}}
}
func av(v types.AttributeValue) string            { return v.(*types.AttributeValueMemberS).Value }
func ck(k map[string]types.AttributeValue) string { return av(k["pk"]) + "\x00" + av(k["sk"]) }

func (m *memDynamo) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	m.items[ck(in.Item)] = in.Item
	return &dynamodb.PutItemOutput{}, nil
}
func (m *memDynamo) GetItem(_ context.Context, in *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	return &dynamodb.GetItemOutput{Item: m.items[ck(in.Key)]}, nil
}
func (m *memDynamo) DeleteItem(_ context.Context, in *dynamodb.DeleteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	delete(m.items, ck(in.Key))
	return &dynamodb.DeleteItemOutput{}, nil
}
func (m *memDynamo) Query(_ context.Context, in *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	pk := av(in.ExpressionAttributeValues[":pk"])
	prefix := av(in.ExpressionAttributeValues[":sk"])
	var out []map[string]types.AttributeValue
	for _, it := range m.items {
		if av(it["pk"]) == pk && strings.HasPrefix(av(it["sk"]), prefix) {
			out = append(out, it)
		}
	}
	return &dynamodb.QueryOutput{Items: out}, nil
}

// campaignRoutes mounts only the campaign subtree with requireGM, seeding the
// verified-token context the real auth middleware would set. This isolates the
// gate + CRUD from the JWT machinery (covered in auth_test).
func campaignRoutes(h *handler) http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(auth.WithRawToken(req.Context(), "user-bearer")))
		})
	})
	r.Route("/api/app/campaigns/{campaignID}/encounters", func(r chi.Router) {
		r.Use(h.requireGM)
		r.Post("/", h.createEncounter)
		r.Get("/", h.listEncounters)
		r.Get("/{encounterID}", h.getEncounter)
		r.Put("/{encounterID}", h.updateEncounter)
		r.Delete("/{encounterID}", h.deleteEncounter)
		r.Post("/{encounterID}/release", h.releaseEncounter)
	})
	return r
}

// gmServer returns a lets-roll stub that reports the given GM status, or a raw
// status code when statusCode != 0.
func gmServer(t *testing.T, amGM bool, statusCode int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if statusCode != 0 {
			w.WriteHeader(statusCode)
			return
		}
		_ = json.NewEncoder(w).Encode(letsroll.Game{ID: 1, Name: "Camp", AmGM: amGM})
	}))
}

func newHandler(t *testing.T, amGM bool, status int) (*handler, *memDynamo) {
	t.Helper()
	srv := gmServer(t, amGM, status)
	t.Cleanup(srv.Close)
	mem := newMem()
	return &handler{cfg: Config{
		Env:      "test",
		Store:    store.New(mem, "t"),
		LetsRoll: letsroll.New(srv.URL),
	}}, mem
}

func do(t *testing.T, router http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, r)
	return rec
}

const encPath = "/api/app/campaigns/g1/encounters"

func TestRequireGM_RejectsNonGM(t *testing.T) {
	h, _ := newHandler(t, false, 0)
	rec := do(t, campaignRoutes(h), http.MethodPost, encPath, `{"name":"x"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-GM create = %d, want 403", rec.Code)
	}
}

func TestRequireGM_ForbiddenFromLetsRollIs403(t *testing.T) {
	h, _ := newHandler(t, false, http.StatusNotFound) // lets-roll: not a member
	rec := do(t, campaignRoutes(h), http.MethodGet, encPath, "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-member list = %d, want 403", rec.Code)
	}
}

func TestRequireGM_LetsRollOutageIs502(t *testing.T) {
	h, _ := newHandler(t, false, http.StatusInternalServerError)
	rec := do(t, campaignRoutes(h), http.MethodGet, encPath, "")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("upstream outage = %d, want 502 (must not fall through to allow)", rec.Code)
	}
}

func TestCreate_ThenGetAndList(t *testing.T) {
	h, _ := newHandler(t, true, 0)
	router := campaignRoutes(h)

	rec := do(t, router, http.MethodPost, encPath, `{"name":"Goblins","currency":{"gp":10}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201; body=%s", rec.Code, rec.Body)
	}
	var created model.Encounter
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.ID == "" || created.CampaignID != "g1" || created.Status != model.StatusDraft {
		t.Fatalf("server-owned fields wrong: %+v", created)
	}

	rec = do(t, router, http.MethodGet, encPath+"/"+created.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get = %d, want 200", rec.Code)
	}
	rec = do(t, router, http.MethodGet, encPath, "")
	var list []model.Encounter
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 1 {
		t.Fatalf("list len = %d, want 1", len(list))
	}
}

func TestCreate_PersistsXPAwards(t *testing.T) {
	h, _ := newHandler(t, true, 0)
	router := campaignRoutes(h)

	body := `{"name":"Visitor's Reading Room","xp_awards":[{"amount":30,"reason":"gained Augrael as an ally"}]}`
	rec := do(t, router, http.MethodPost, encPath, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201; body=%s", rec.Code, rec.Body)
	}
	var created model.Encounter
	_ = json.Unmarshal(rec.Body.Bytes(), &created)

	rec = do(t, router, http.MethodGet, encPath+"/"+created.ID, "")
	var got model.Encounter
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got.XPAwards) != 1 || got.XPAwards[0].Amount != 30 || got.XPAwards[0].Reason != "gained Augrael as an ally" {
		t.Fatalf("xp_awards round-trip wrong: %+v", got.XPAwards)
	}

	// A routine edit (PUT) must carry xp_awards, not silently wipe them.
	rec = do(t, router, http.MethodPut, encPath+"/"+created.ID,
		`{"name":"Visitor's Reading Room","xp_awards":[{"amount":45,"reason":"quest milestone"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update = %d, want 200; body=%s", rec.Code, rec.Body)
	}
	var updated model.Encounter
	_ = json.Unmarshal(rec.Body.Bytes(), &updated)
	if len(updated.XPAwards) != 1 || updated.XPAwards[0].Amount != 45 || updated.XPAwards[0].Reason != "quest milestone" {
		t.Fatalf("update wiped/dropped xp_awards: %+v", updated.XPAwards)
	}

	// amount < 1 is rejected
	rec = do(t, router, http.MethodPost, encPath, `{"name":"x","xp_awards":[{"reason":"no amount"}]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("zero-amount award = %d, want 400", rec.Code)
	}
}

func TestCreate_PersistsTextBlocks(t *testing.T) {
	h, _ := newHandler(t, true, 0)
	router := campaignRoutes(h)

	// text_blocks must pass the strict (DisallowUnknownFields) decoder and round-trip
	// verbatim. A migrated block is untitled (title omitted); a named block keeps its title.
	body := `{"name":"A2 Decrepit Drawbridge","text_blocks":[{"body":"The bridge sags over the moat."},{"title":"Tactics","body":"It collapses on the third crossing."}]}`
	rec := do(t, router, http.MethodPost, encPath, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201; body=%s", rec.Code, rec.Body)
	}
	var created model.Encounter
	_ = json.Unmarshal(rec.Body.Bytes(), &created)

	rec = do(t, router, http.MethodGet, encPath+"/"+created.ID, "")
	var got model.Encounter
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got.TextBlocks) != 2 || got.TextBlocks[0].Title != "" || got.TextBlocks[0].Body != "The bridge sags over the moat." || got.TextBlocks[1].Title != "Tactics" {
		t.Fatalf("text_blocks round-trip wrong: %+v", got.TextBlocks)
	}

	// A routine edit (PUT) must carry text_blocks, not silently wipe them.
	rec = do(t, router, http.MethodPut, encPath+"/"+created.ID,
		`{"name":"A2 Decrepit Drawbridge","text_blocks":[{"body":"Rebuilt sturdier."}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update = %d, want 200; body=%s", rec.Code, rec.Body)
	}
	var updated model.Encounter
	_ = json.Unmarshal(rec.Body.Bytes(), &updated)
	if len(updated.TextBlocks) != 1 || updated.TextBlocks[0].Body != "Rebuilt sturdier." {
		t.Fatalf("update wiped/dropped text_blocks: %+v", updated.TextBlocks)
	}
}

func TestCreate_PersistsRoomTypeAndRewards(t *testing.T) {
	h, _ := newHandler(t, true, 0)
	router := campaignRoutes(h)

	body := `{"name":"Secure Collection","room_type":"knowledge",
		"rewards":[{"kind":"information","label":"Belcorra's history","description":"# lore"},
		           {"kind":"item","label":"The Whispering Reeds"}]}`
	rec := do(t, router, http.MethodPost, encPath, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201; body=%s", rec.Code, rec.Body)
	}
	var created model.Encounter
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if created.RoomType != model.RoomKnowledge || len(created.Rewards) != 2 {
		t.Fatalf("room_type/rewards not stored: %+v", created)
	}

	// A default (no room_type) create normalizes to combat.
	rec = do(t, router, http.MethodPost, encPath, `{"name":"Fight"}`)
	var plain model.Encounter
	_ = json.Unmarshal(rec.Body.Bytes(), &plain)
	if plain.RoomType != model.RoomCombat {
		t.Fatalf("default room_type = %q, want combat", plain.RoomType)
	}

	// A routine edit (PUT) must carry room_type + rewards, not wipe them.
	rec = do(t, router, http.MethodPut, encPath+"/"+created.ID,
		`{"name":"Secure Collection","room_type":"social","rewards":[{"kind":"ally","label":"Augrael"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update = %d, want 200; body=%s", rec.Code, rec.Body)
	}
	var updated model.Encounter
	_ = json.Unmarshal(rec.Body.Bytes(), &updated)
	if updated.RoomType != model.RoomSocial || len(updated.Rewards) != 1 || updated.Rewards[0].Kind != model.RewardAlly {
		t.Fatalf("update wiped/dropped room_type/rewards: %+v", updated)
	}

	// A reward with no label is rejected.
	rec = do(t, router, http.MethodPost, encPath, `{"name":"x","rewards":[{"kind":"information"}]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("labelless reward = %d, want 400", rec.Code)
	}
}

func TestCreate_PersistsSkillChecks(t *testing.T) {
	h, _ := newHandler(t, true, 0)
	router := campaignRoutes(h)

	body := `{"name":"Planked floor","skill_checks":[{"skill":"Perception","dc":12,"description":"spot the loose planks"}]}`
	rec := do(t, router, http.MethodPost, encPath, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201; body=%s", rec.Code, rec.Body)
	}
	var created model.Encounter
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if len(created.SkillChecks) != 1 || created.SkillChecks[0].Skill != "Perception" || created.SkillChecks[0].DC != 12 {
		t.Fatalf("skill_checks not stored: %+v", created.SkillChecks)
	}

	// A routine edit (PUT) must carry skill_checks, not wipe them.
	rec = do(t, router, http.MethodPut, encPath+"/"+created.ID,
		`{"name":"Planked floor","skill_checks":[{"skill":"Nature","dc":15}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update = %d, want 200; body=%s", rec.Code, rec.Body)
	}
	var updated model.Encounter
	_ = json.Unmarshal(rec.Body.Bytes(), &updated)
	if len(updated.SkillChecks) != 1 || updated.SkillChecks[0].Skill != "Nature" || updated.SkillChecks[0].DC != 15 {
		t.Fatalf("update wiped/dropped skill_checks: %+v", updated.SkillChecks)
	}

	// A skill check with no skill / dc < 1 is rejected.
	rec = do(t, router, http.MethodPost, encPath, `{"name":"x","skill_checks":[{"dc":10}]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("skill-less check = %d, want 400", rec.Code)
	}
}

// xhwl: the richer skill-check fields (successes, alternatives, per-degree
// outcomes) must be accepted (DisallowUnknownFields) and round-trip intact.
func TestCreate_PersistsRichSkillCheck(t *testing.T) {
	h, _ := newHandler(t, true, 0)
	router := campaignRoutes(h)

	body := `{"name":"Locked hatch","skill_checks":[{"skill":"Thievery","dc":25,"successes":4,"description":"pick the vault lock","alternatives":[{"skill":"Religion","dc":20}],"outcomes":{"crit_success":"open + no alarm","failure":"the lock jams"}}]}`
	rec := do(t, router, http.MethodPost, encPath, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201; body=%s", rec.Code, rec.Body)
	}
	var created model.Encounter
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	s := created.SkillChecks[0]
	if s.Successes != 4 {
		t.Fatalf("successes = %d, want 4", s.Successes)
	}
	if len(s.Alternatives) != 1 || s.Alternatives[0].Skill != "Religion" || s.Alternatives[0].DC != 20 {
		t.Fatalf("alternatives not stored: %+v", s.Alternatives)
	}
	if s.Outcomes == nil || s.Outcomes.CritSuccess != "open + no alarm" || s.Outcomes.Failure != "the lock jams" {
		t.Fatalf("outcomes not stored: %+v", s.Outcomes)
	}

	// An alternative with dc < 1 is rejected.
	rec = do(t, router, http.MethodPost, encPath, `{"name":"x","skill_checks":[{"skill":"Thievery","dc":22,"alternatives":[{"skill":"Religion","dc":0}]}]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad alternative dc = %d, want 400", rec.Code)
	}
}

func TestCreate_PersistsExits(t *testing.T) {
	h, _ := newHandler(t, true, 0)
	router := campaignRoutes(h)

	body := `{"name":"A1","exits":[{"to_encounter_id":"enc-a2","label":"north door"},{"label":"Exterior"}]}`
	rec := do(t, router, http.MethodPost, encPath, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201; body=%s", rec.Code, rec.Body)
	}
	var created model.Encounter
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if len(created.Exits) != 2 || created.Exits[0].ToEncounterID != "enc-a2" || created.Exits[1].Label != "Exterior" {
		t.Fatalf("exits not stored: %+v", created.Exits)
	}

	// A routine edit (PUT) must carry exits, not wipe them.
	rec = do(t, router, http.MethodPut, encPath+"/"+created.ID, `{"name":"A1","exits":[{"to_encounter_id":"enc-a3"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update = %d, want 200; body=%s", rec.Code, rec.Body)
	}
	var updated model.Encounter
	_ = json.Unmarshal(rec.Body.Bytes(), &updated)
	if len(updated.Exits) != 1 || updated.Exits[0].ToEncounterID != "enc-a3" {
		t.Fatalf("update wiped/dropped exits: %+v", updated.Exits)
	}

	// An entirely-empty exit (no target, no label) is ACCEPTED + persisted — it's a
	// placeholder "+ exit" row the GM fills in later, so it must not be dropped.
	rec = do(t, router, http.MethodPost, encPath, `{"name":"x","exits":[{}]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("empty exit = %d, want 201; body=%s", rec.Code, rec.Body)
	}
	var withBlank model.Encounter
	_ = json.Unmarshal(rec.Body.Bytes(), &withBlank)
	if len(withBlank.Exits) != 1 {
		t.Fatalf("blank exit not persisted: %+v", withBlank.Exits)
	}
}

func TestCreate_ValidationRejects(t *testing.T) {
	h, _ := newHandler(t, true, 0)
	// missing name
	rec := do(t, campaignRoutes(h), http.MethodPost, encPath, `{"currency":{"gp":1}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("nameless create = %d, want 400", rec.Code)
	}
}

func TestRelease_LifecycleAndImmutability(t *testing.T) {
	h, _ := newHandler(t, true, 0)
	router := campaignRoutes(h)

	rec := do(t, router, http.MethodPost, encPath, `{"name":"Loot"}`)
	var enc model.Encounter
	_ = json.Unmarshal(rec.Body.Bytes(), &enc)
	base := encPath + "/" + enc.ID

	rec = do(t, router, http.MethodPost, base+"/release", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("release = %d, want 200", rec.Code)
	}
	var released model.Encounter
	_ = json.Unmarshal(rec.Body.Bytes(), &released)
	if released.Status != model.StatusReleased || released.ReleasedAt == nil {
		t.Fatalf("release did not set status/timestamp: %+v", released)
	}

	// second release is a conflict
	if rec = do(t, router, http.MethodPost, base+"/release", ""); rec.Code != http.StatusConflict {
		t.Fatalf("double release = %d, want 409", rec.Code)
	}
	// released encounters are immutable via update
	if rec = do(t, router, http.MethodPut, base, `{"name":"changed"}`); rec.Code != http.StatusConflict {
		t.Fatalf("edit-after-release = %d, want 409", rec.Code)
	}
}

func TestUpdate_HappyPathAndStatusTransition(t *testing.T) {
	h, _ := newHandler(t, true, 0)
	router := campaignRoutes(h)
	rec := do(t, router, http.MethodPost, encPath, `{"name":"orig","currency":{"gp":1}}`)
	var enc model.Encounter
	_ = json.Unmarshal(rec.Body.Bytes(), &enc)
	base := encPath + "/" + enc.ID

	// draft -> run, with edited fields
	rec = do(t, router, http.MethodPut, base, `{"name":"renamed","currency":{"gp":5},"status":"run"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update = %d, want 200; body=%s", rec.Code, rec.Body)
	}
	var updated model.Encounter
	_ = json.Unmarshal(rec.Body.Bytes(), &updated)
	if updated.Name != "renamed" || updated.Currency.GP != 5 || updated.Status != model.StatusRun {
		t.Fatalf("update did not apply fields/status: %+v", updated)
	}
	if updated.ID != enc.ID || !updated.CreatedAt.Equal(enc.CreatedAt) {
		t.Fatalf("update mutated server-owned id/created_at: %+v", updated)
	}

	// update may not jump straight to released
	if rec = do(t, router, http.MethodPut, base, `{"name":"x","status":"released"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("update->released = %d, want 400", rec.Code)
	}
}

func TestUpdate_MissingIs404(t *testing.T) {
	h, _ := newHandler(t, true, 0)
	rec := do(t, campaignRoutes(h), http.MethodPut, encPath+"/nope", `{"name":"x"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("update missing = %d, want 404", rec.Code)
	}
}

func TestCreate_RejectsEmptyRef(t *testing.T) {
	h, _ := newHandler(t, true, 0)
	// monster line references no content -> Validate rejects
	rec := do(t, campaignRoutes(h), http.MethodPost, encPath, `{"name":"x","monsters":[{"count":1}]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty-ref create = %d, want 400", rec.Code)
	}
}

func TestCreate_DefaultsTreasureEnums(t *testing.T) {
	h, _ := newHandler(t, true, 0)
	body := `{"name":"x","treasure":[{"qty":1,"ref":{"game_id":"g"}}]}`
	rec := do(t, campaignRoutes(h), http.MethodPost, encPath, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201; body=%s", rec.Code, rec.Body)
	}
	var enc model.Encounter
	_ = json.Unmarshal(rec.Body.Bytes(), &enc)
	if enc.Treasure[0].SaleClass != model.SaleNormal || enc.Treasure[0].State != model.TreasureIntact {
		t.Fatalf("unset enums not defaulted: %+v", enc.Treasure[0])
	}
}

func TestCreate_PreservesTreasurePoolsAndValueTiers(t *testing.T) {
	h, _ := newHandler(t, true, 0)
	body := `{"name":"x",
		"treasure_pools":[{"id":"p1","name":"altar","description":"# hidden","gate":{"skill":"Perception","dc":18}}],
		"treasure":[{"qty":1,"ref":{"game_id":"g"},"pool_id":"p1","value_tiers":{"success":4000,"failure":2000,"crit_failure":0}}]}`
	rec := do(t, campaignRoutes(h), http.MethodPost, encPath, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201; body=%s", rec.Code, rec.Body)
	}
	var enc model.Encounter
	_ = json.Unmarshal(rec.Body.Bytes(), &enc)
	if len(enc.TreasurePools) != 1 || enc.TreasurePools[0].ID != "p1" ||
		enc.TreasurePools[0].Description != "# hidden" ||
		enc.TreasurePools[0].Gate == nil || enc.TreasurePools[0].Gate.DC != 18 {
		t.Fatalf("treasure pool not preserved: %+v", enc.TreasurePools)
	}
	tl := enc.Treasure[0]
	if tl.PoolID != "p1" || tl.ValueTiers == nil ||
		tl.ValueTiers.Success == nil || *tl.ValueTiers.Success != 4000 ||
		tl.ValueTiers.CritFailure == nil || *tl.ValueTiers.CritFailure != 0 {
		t.Fatalf("treasure pool_id/value_tiers not preserved: %+v", tl)
	}
}

func TestUpdate_PreservesTreasurePoolsAndValueTiers(t *testing.T) {
	h, _ := newHandler(t, true, 0)
	router := campaignRoutes(h)

	rec := do(t, router, http.MethodPost, encPath, `{"name":"x"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201; body=%s", rec.Code, rec.Body)
	}
	var created model.Encounter
	_ = json.Unmarshal(rec.Body.Bytes(), &created)

	// A routine edit (PUT) must carry treasure_pools + per-line value_tiers, not
	// silently wipe them.
	body := `{"name":"x",
		"treasure_pools":[{"id":"p1","name":"altar","description":"# hidden","gate":{"skill":"Perception","dc":18}}],
		"treasure":[{"qty":1,"ref":{"game_id":"g"},"pool_id":"p1","value_tiers":{"success":4000,"crit_failure":0}}]}`
	rec = do(t, router, http.MethodPut, encPath+"/"+created.ID, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("update = %d, want 200; body=%s", rec.Code, rec.Body)
	}
	var enc model.Encounter
	_ = json.Unmarshal(rec.Body.Bytes(), &enc)
	if len(enc.TreasurePools) != 1 || enc.TreasurePools[0].ID != "p1" ||
		enc.TreasurePools[0].Description != "# hidden" {
		t.Fatalf("update wiped treasure pools: %+v", enc.TreasurePools)
	}
	tl := enc.Treasure[0]
	if tl.PoolID != "p1" || tl.ValueTiers == nil ||
		tl.ValueTiers.Success == nil || *tl.ValueTiers.Success != 4000 {
		t.Fatalf("update wiped pool_id/value_tiers: %+v", tl)
	}
}

func TestDecodeBody_Rejects400(t *testing.T) {
	h, _ := newHandler(t, true, 0)
	router := campaignRoutes(h)
	for name, body := range map[string]string{
		"malformed json": `{`,
		"unknown field":  `{"name":"x","bogus":1}`,
	} {
		if rec := do(t, router, http.MethodPost, encPath, body); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: = %d, want 400", name, rec.Code)
		}
	}
}

// TestCreate_V2FieldsAndTemplatedMonster covers the v2 additions: chapter_id +
// A composed (runed) treasure item carries a computed copper total in ref.price_cp.
// The request decoder is strict (DisallowUnknownFields), so ContentRef must declare
// price_cp or the whole save 400s — and the value must round-trip for the budget.
func TestCreate_ComposedTreasureCarriesPriceCp(t *testing.T) {
	h, _ := newHandler(t, true, 0)
	router := campaignRoutes(h)
	body := `{
		"name":"Loot",
		"treasure":[{
			"qty":1,
			"ref":{
				"base":{"game_id":"Weapons:1"},
				"modifications":[{"effect_game_id":"Rune:striking","effect_name":"Striking","grade":4,"price_cp":6500}],
				"json":{"name":"Striking Longsword"},
				"price_cp":6600
			}
		}]
	}`
	rec := do(t, router, http.MethodPost, encPath, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create with a priced composed treasure ref = %d, want 201; body=%s", rec.Code, rec.Body)
	}
	var enc model.Encounter
	_ = json.Unmarshal(rec.Body.Bytes(), &enc)
	if len(enc.Treasure) != 1 || enc.Treasure[0].Ref.PriceCp == nil || *enc.Treasure[0].Ref.PriceCp != 6600 {
		t.Fatalf("composed ref price_cp not round-tripped: %+v", enc.Treasure)
	}
}

// markdown description round-trip, and a *templated* monster stored as a derived
// ContentRef ({base, modifications, json}) — which needs no new model field and
// must pass validation via the recursive isEmpty (base references content).
// 0o77: a monster's equipment loadout (catalog + composed item refs, qty) must be
// accepted (DisallowUnknownFields) and round-trip intact.
func TestCreate_PersistsMonsterLoadout(t *testing.T) {
	h, _ := newHandler(t, true, 0)
	router := campaignRoutes(h)

	body := `{"name":"Armed mitflit","monsters":[{"count":3,"ref":{"game_id":"Monsters:mitflit"},"loadout":[
		{"qty":3,"ref":{"game_id":"Weapons:shortsword"}},
		{"qty":1,"variant":"+1","ref":{"base":{"game_id":"Weapons:rapier"},"modifications":[{"effect_game_id":"Runes:potency"}],"price_cp":3500}}
	]}]}`
	rec := do(t, router, http.MethodPost, encPath, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201; body=%s", rec.Code, rec.Body)
	}
	var enc model.Encounter
	_ = json.Unmarshal(rec.Body.Bytes(), &enc)
	lo := enc.Monsters[0].Loadout
	if len(lo) != 2 {
		t.Fatalf("loadout not round-tripped: %+v", lo)
	}
	if lo[0].Qty != 3 || lo[0].Ref.GameID != "Weapons:shortsword" {
		t.Fatalf("loadout[0] wrong: %+v", lo[0])
	}
	if lo[1].Variant != "+1" || lo[1].Ref.Base == nil || lo[1].Ref.Base.GameID != "Weapons:rapier" {
		t.Fatalf("composed loadout item not preserved: %+v", lo[1])
	}

	// qty < 1 is rejected.
	rec = do(t, router, http.MethodPost, encPath, `{"name":"x","monsters":[{"count":1,"ref":{"game_id":"g"},"loadout":[{"qty":0,"ref":{"game_id":"w"}}]}]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("loadout qty 0 = %d, want 400", rec.Code)
	}
}

func TestCreate_V2FieldsAndTemplatedMonster(t *testing.T) {
	h, _ := newHandler(t, true, 0)
	router := campaignRoutes(h)

	body := `{
		"name":"Boss fight",
		"chapter_id":"ch-abc",
		"description":"# The Vault\n\nA **giant** guards the door.",
		"monsters":[{
			"count":1,
			"adjustment":"elite",
			"ref":{
				"base":{"game_id":"Monsters:2382"},
				"modifications":[{"template_game_id":"Templates:fire","selections":{"element":"fire"}}],
				"json":{"name":"Fire-touched Giant (elite)"}
			}
		}]
	}`
	rec := do(t, router, http.MethodPost, encPath, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201; body=%s", rec.Code, rec.Body)
	}
	var enc model.Encounter
	_ = json.Unmarshal(rec.Body.Bytes(), &enc)
	if enc.ChapterID != "ch-abc" || enc.Description == "" {
		t.Fatalf("v2 fields not persisted: chapter=%q desc=%q", enc.ChapterID, enc.Description)
	}
	if len(enc.Monsters) != 1 || enc.Monsters[0].Ref.Base == nil || enc.Monsters[0].Ref.Base.GameID != "Monsters:2382" {
		t.Fatalf("derived monster ref not round-tripped: %+v", enc.Monsters)
	}
	ref := enc.Monsters[0].Ref
	// Assert the modification's *content* round-tripped, not just its presence —
	// otherwise a serialization drop of template_game_id/selections would pass.
	if len(ref.Modifications) != 1 {
		t.Fatalf("template modifications dropped: %+v", ref)
	}
	var mod struct {
		TemplateGameID string            `json:"template_game_id"`
		Selections     map[string]string `json:"selections"`
	}
	if err := json.Unmarshal(ref.Modifications[0], &mod); err != nil {
		t.Fatalf("modification is not the shape we stored: %v", err)
	}
	if mod.TemplateGameID != "Templates:fire" || mod.Selections["element"] != "fire" {
		t.Fatalf("modification content not preserved: %+v", mod)
	}
	// And the resolved-creature snapshot (the display payload) survived.
	if len(ref.JSON) == 0 || !strings.Contains(string(ref.JSON), "Fire-touched Giant") {
		t.Fatalf("resolved json snapshot not preserved: %s", ref.JSON)
	}

	// Update can move it to a different chapter and edit the description.
	base := encPath + "/" + enc.ID
	rec = do(t, router, http.MethodPut, base, `{"name":"Boss fight","chapter_id":"ch-xyz","description":"moved"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update = %d, want 200; body=%s", rec.Code, rec.Body)
	}
	var updated model.Encounter
	_ = json.Unmarshal(rec.Body.Bytes(), &updated)
	if updated.ChapterID != "ch-xyz" || updated.Description != "moved" {
		t.Fatalf("v2 fields not updated: %+v", updated)
	}
}

// errDynamo fails every operation, to exercise the handlers' 500 paths.
type errDynamo struct{}

func (errDynamo) PutItem(context.Context, *dynamodb.PutItemInput, ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	return nil, errBoom
}
func (errDynamo) GetItem(context.Context, *dynamodb.GetItemInput, ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	return nil, errBoom
}
func (errDynamo) Query(context.Context, *dynamodb.QueryInput, ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	return nil, errBoom
}
func (errDynamo) DeleteItem(context.Context, *dynamodb.DeleteItemInput, ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	return nil, errBoom
}

var errBoom = errorString("dynamo down")

type errorString string

func (e errorString) Error() string { return string(e) }

func TestHandlers_StoreErrorsAre500(t *testing.T) {
	srv := gmServer(t, true, 0)
	t.Cleanup(srv.Close)
	h := &handler{cfg: Config{Env: "test", Store: store.New(errDynamo{}, "t"), LetsRoll: letsroll.New(srv.URL)}}
	router := campaignRoutes(h)

	cases := map[string]struct {
		method, path, body string
	}{
		"create": {http.MethodPost, encPath, `{"name":"x"}`},
		"list":   {http.MethodGet, encPath, ""},
		"get":    {http.MethodGet, encPath + "/id1", ""},
		"delete": {http.MethodDelete, encPath + "/id1", ""},
	}
	for name, tc := range cases {
		if rec := do(t, router, tc.method, tc.path, tc.body); rec.Code != http.StatusInternalServerError {
			t.Fatalf("%s under store error = %d, want 500", name, rec.Code)
		}
	}
}

func TestGet_NotFoundIs404(t *testing.T) {
	h, _ := newHandler(t, true, 0)
	rec := do(t, campaignRoutes(h), http.MethodGet, encPath+"/nope", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing get = %d, want 404", rec.Code)
	}
}

func TestDelete_IsNoContentAndRemoves(t *testing.T) {
	h, _ := newHandler(t, true, 0)
	router := campaignRoutes(h)
	rec := do(t, router, http.MethodPost, encPath, `{"name":"tmp"}`)
	var enc model.Encounter
	_ = json.Unmarshal(rec.Body.Bytes(), &enc)

	if rec = do(t, router, http.MethodDelete, encPath+"/"+enc.ID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204", rec.Code)
	}
	if rec = do(t, router, http.MethodGet, encPath+"/"+enc.ID, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("get after delete = %d, want 404", rec.Code)
	}
}

func TestTreasureVariant_RoundTrips(t *testing.T) {
	h, _ := newHandler(t, true, 0)
	router := campaignRoutes(h)
	// A treasure line carrying a chosen item variant (by name).
	body := `{"name":"loot","treasure":[{"qty":1,"ref":{"game_id":"Weapons:1"},"variant":"Striking (Greater)"}]}`
	rec := do(t, router, http.MethodPost, encPath, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201; body=%s", rec.Code, rec.Body)
	}
	var enc model.Encounter
	_ = json.Unmarshal(rec.Body.Bytes(), &enc)
	if len(enc.Treasure) != 1 || enc.Treasure[0].Variant != "Striking (Greater)" {
		t.Fatalf("treasure variant not preserved: %+v", enc.Treasure)
	}
}
