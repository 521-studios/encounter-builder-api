package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/521studios/encounter-builder-api/internal/model"
	"github.com/go-chi/chi/v5"
)

func TestMarkKill(t *testing.T) {
	h, _ := newHandler(t, true, 0)
	router := campaignRoutes(h)
	rec := do(t, router, http.MethodPost, encPath, goblins)
	var e model.Encounter
	_ = json.Unmarshal(rec.Body.Bytes(), &e)
	base := encPath + "/" + e.ID

	if rec := do(t, router, http.MethodPost, base+"/kills", `{"eb_monster_key":"m1:2","killed":true}`); rec.Code != http.StatusOK {
		t.Fatalf("kill = %d %s", rec.Code, rec.Body)
	}
	// A full-replace PUT (the GM's autosave) never touches kills.
	do(t, router, http.MethodPut, base, goblins)
	rec = do(t, router, http.MethodGet, base, "")
	var got model.Encounter
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if k := got.Kills["m1:2"]; !k.Killed || k.Source != model.KillSourceEB || k.At.IsZero() || len(got.Kills) != 1 {
		t.Fatalf("kills after PUT = %+v", got.Kills)
	}
	// Un-kill.
	do(t, router, http.MethodPost, base+"/kills", `{"eb_monster_key":"m1:2","killed":false}`)
	rec = do(t, router, http.MethodGet, base, "")
	got = model.Encounter{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Kills["m1:2"].Killed {
		t.Fatalf("un-kill didn't stick: %+v", got.Kills)
	}

	for _, body := range []string{
		`{"eb_monster_key":"m1:3","killed":true}`, // count is 2
		`{"eb_monster_key":"nope:1","killed":true}`,
	} {
		if rec := do(t, router, http.MethodPost, base+"/kills", body); rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s = %d, want 422", body, rec.Code)
		}
	}
	if rec := do(t, router, http.MethodPost, base+"/kills", `{"eb_monster_key":"m1:1","killed":true,"x":1}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field = %d, want 400", rec.Code)
	}
}

func TestMarkKill_LatestWins(t *testing.T) {
	var e model.Encounter
	t0 := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	if !e.MarkKill("a:1", model.Kill{Killed: true, At: t0}) {
		t.Fatal("first mark refused")
	}
	if e.MarkKill("a:1", model.Kill{Killed: false, At: t0.Add(-time.Second)}) || !e.Kills["a:1"].Killed {
		t.Fatal("older change applied")
	}
	if e.MarkKill("a:1", model.Kill{Killed: false, At: t0}) {
		t.Fatal("same-instant change applied")
	}
	if !e.MarkKill("a:1", model.Kill{Killed: false, At: t0.Add(time.Second)}) || e.Kills["a:1"].Killed {
		t.Fatal("newer change refused")
	}
}

func TestKillReports(t *testing.T) {
	h, _ := newHandler(t, true, 0)
	gm := campaignRoutes(h)
	lr := chi.NewRouter() // the lrident middleware is tested in its own package
	lr.Post("/api/app/campaigns/{campaignID}/encounters/{encounterID}/kill-reports", h.killReports)

	rec := do(t, gm, http.MethodPost, encPath, goblins)
	var e model.Encounter
	_ = json.Unmarshal(rec.Body.Bytes(), &e)
	base := encPath + "/" + e.ID
	report := func(body string) (int, map[string]string) {
		rec := do(t, lr, http.MethodPost, base+"/kill-reports", body)
		var out struct{ Results map[string]string }
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out.Results
	}
	t1 := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)

	// Prepared: not in combat yet.
	if code, _ := report(`{"kills":[{"eb_monster_key":"m1:1","killed":true,"at":"` + t1 + `"}]}`); code != http.StatusConflict {
		t.Fatalf("prepared = %d, want 409", code)
	}
	do(t, gm, http.MethodPut, base, `{"name":"Goblins","status":"executing","content":`+goblinsContent+`}`)

	code, res := report(`{"kills":[{"eb_monster_key":"m1:1","killed":true,"at":"` + t1 + `"},{"eb_monster_key":"m9:1","killed":true,"at":"` + t1 + `"}]}`)
	if code != http.StatusOK || res["m1:1"] != "applied" || res["m9:1"] != "unknown" {
		t.Fatalf("report = %d %v", code, res)
	}
	// Equal at is stale; the GM's later mark in EB beats an older report.
	if _, res := report(`{"kills":[{"eb_monster_key":"m1:1","killed":false,"at":"` + t1 + `"}]}`); res["m1:1"] != "stale" {
		t.Fatalf("tie = %v, want stale", res)
	}
	do(t, gm, http.MethodPost, base+"/kills", `{"eb_monster_key":"m1:2","killed":true}`)
	if _, res := report(`{"kills":[{"eb_monster_key":"m1:2","killed":false,"at":"` + t1 + `"}]}`); res["m1:2"] != "stale" {
		t.Fatalf("older than GM mark = %v, want stale", res)
	}
	rec = do(t, gm, http.MethodGet, base, "")
	var got model.Encounter
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if k := got.Kills["m1:1"]; !k.Killed || k.Source != model.KillSourceLetsRoll || !got.Kills["m1:2"].Killed {
		t.Fatalf("kills = %+v", got.Kills)
	}

	if code, _ := report(`{"kills":[{"eb_monster_key":"m1:1","killed":true}]}`); code != http.StatusBadRequest {
		t.Fatalf("missing at = %d, want 400", code)
	}
	// Closed out: the package is built from the kills, so late reports are refused.
	do(t, gm, http.MethodPut, base, `{"name":"Goblins","status":"complete","content":`+goblinsContent+`}`)
	if code, _ := report(`{"kills":[{"eb_monster_key":"m1:1","killed":false,"at":"` + time.Now().UTC().Format(time.RFC3339Nano) + `"}]}`); code != http.StatusConflict {
		t.Fatalf("complete = %d, want 409", code)
	}
}
