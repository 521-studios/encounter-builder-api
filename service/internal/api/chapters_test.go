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

// chapterRoutes mounts only the chapters subtree with requireGM, seeding the
// verified-token context (mirrors campaignRoutes for encounters).
func chapterRoutes(h *handler) http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(auth.WithRawToken(req.Context(), "user-bearer")))
		})
	})
	r.Route("/api/app/campaigns/{campaignID}/chapters", func(r chi.Router) {
		r.Use(h.requireGM)
		r.Post("/", h.createChapter)
		r.Get("/", h.listChapters)
		r.Put("/{chapterID}", h.updateChapter)
		r.Delete("/{chapterID}", h.deleteChapter)
	})
	return r
}

const chPath = "/api/app/campaigns/g1/chapters"

func TestChapter_RequireGMRejectsNonGM(t *testing.T) {
	h, _ := newHandler(t, false, 0)
	rec := do(t, chapterRoutes(h), http.MethodPost, chPath, `{"name":"x"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-GM create = %d, want 403", rec.Code)
	}
}

func TestChapter_CreateListUpdateDelete(t *testing.T) {
	h, _ := newHandler(t, true, 0)
	router := chapterRoutes(h)

	rec := do(t, router, http.MethodPost, chPath, `{"name":"Chapter 1","order":1}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201; body=%s", rec.Code, rec.Body)
	}
	var created model.Chapter
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.ID == "" || created.CampaignID != "g1" || created.Order != 1 {
		t.Fatalf("server-owned fields wrong: %+v", created)
	}

	// rename + reorder
	rec = do(t, router, http.MethodPut, chPath+"/"+created.ID, `{"name":"Prologue","order":0}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update = %d, want 200; body=%s", rec.Code, rec.Body)
	}
	var updated model.Chapter
	_ = json.Unmarshal(rec.Body.Bytes(), &updated)
	if updated.Name != "Prologue" || updated.Order != 0 || updated.ID != created.ID {
		t.Fatalf("update did not apply: %+v", updated)
	}

	rec = do(t, router, http.MethodGet, chPath, "")
	var list []model.Chapter
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 1 || list[0].Name != "Prologue" {
		t.Fatalf("list = %+v, want one Prologue", list)
	}

	if rec = do(t, router, http.MethodDelete, chPath+"/"+created.ID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204", rec.Code)
	}
	rec = do(t, router, http.MethodGet, chPath, "")
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 0 {
		t.Fatalf("after delete list = %+v, want empty", list)
	}
}

// order-only omission: a rename with no "order" must not reset the order.
func TestChapter_UpdateKeepsOrderWhenOmitted(t *testing.T) {
	h, _ := newHandler(t, true, 0)
	router := chapterRoutes(h)
	rec := do(t, router, http.MethodPost, chPath, `{"name":"C","order":7}`)
	var c model.Chapter
	_ = json.Unmarshal(rec.Body.Bytes(), &c)

	rec = do(t, router, http.MethodPut, chPath+"/"+c.ID, `{"name":"C renamed"}`)
	var updated model.Chapter
	_ = json.Unmarshal(rec.Body.Bytes(), &updated)
	if updated.Order != 7 {
		t.Fatalf("omitted order was reset to %d, want 7", updated.Order)
	}
}

func TestChapter_CreateValidationRejects(t *testing.T) {
	h, _ := newHandler(t, true, 0)
	rec := do(t, chapterRoutes(h), http.MethodPost, chPath, `{"order":1}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("nameless create = %d, want 400", rec.Code)
	}
}

func TestChapter_UpdateMissingIs404(t *testing.T) {
	h, _ := newHandler(t, true, 0)
	rec := do(t, chapterRoutes(h), http.MethodPut, chPath+"/nope", `{"name":"x"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("update missing = %d, want 404", rec.Code)
	}
}

// Mirror TestHandlers_StoreErrorsAre500 for chapters: every chapter handler must
// surface a store failure as 500, never a silent success or wrong status.
func TestChapterHandlers_StoreErrorsAre500(t *testing.T) {
	srv := gmServer(t, true, 0)
	t.Cleanup(srv.Close)
	h := &handler{cfg: Config{Env: "test", Store: store.New(errDynamo{}, "t"), LetsRoll: letsroll.New(srv.URL)}}
	router := chapterRoutes(h)

	cases := map[string]struct{ method, path, body string }{
		"create": {http.MethodPost, chPath, `{"name":"x"}`},
		"list":   {http.MethodGet, chPath, ""},
		"update": {http.MethodPut, chPath + "/id1", `{"name":"x"}`}, // loadChapter's non-NotFound path -> 500
		"delete": {http.MethodDelete, chPath + "/id1", ""},
	}
	for name, tc := range cases {
		if rec := do(t, router, tc.method, tc.path, tc.body); rec.Code != http.StatusInternalServerError {
			t.Fatalf("%s under store error = %d, want 500", name, rec.Code)
		}
	}
}
