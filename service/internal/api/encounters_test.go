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
		_ = json.NewEncoder(w).Encode(letsroll.Game{ID: "g1", Name: "Camp", AmGM: amGM})
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
