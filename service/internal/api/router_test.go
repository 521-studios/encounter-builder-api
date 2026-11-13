package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/521studios/encounter-builder-api/internal/auth"
	"github.com/521studios/encounter-builder-api/internal/letsroll"
	"github.com/521studios/encounter-builder-api/internal/store"
	"github.com/go-chi/chi/v5"
)

// testDeps builds the non-Auth Config fields the router requires. The campaign
// routes are never reached without a token in these tests, so a store over a
// nil client is fine.
func testDeps() (*store.Store, *letsroll.Client) {
	return store.New(nil, "test-table"), letsroll.New("http://127.0.0.1:1")
}

// TestRouter_ProtectedRoutesRequireToken guards the auth wiring structurally:
// /healthz is public, every other route 401s without a bearer token. A route
// added outside the auth group (a real risk as M2 adds handlers) would fail
// this. No token is sent, so the middleware rejects at the bearer-check before
// any JWKS use — the verifier just needs to exist (warm-up is best-effort, so
// NewVerifier succeeds against an unreachable JWKS).
func TestRouter_ProtectedRoutesRequireToken(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	v, err := auth.NewVerifier(ctx, auth.Config{
		Issuer:  "https://issuer.test",
		JWKSURL: "http://127.0.0.1:1/jwks", // unreachable; warm-up is best-effort
	})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	st, lr := testDeps()
	r := NewRouter(Config{Auth: v, Env: "test", Store: st, LetsRoll: lr})

	cases := map[string]struct {
		method, path string
		want         int
	}{
		"health is public":    {http.MethodGet, "/api/app/healthz", http.StatusOK},
		"me requires a token": {http.MethodGet, "/api/app/me", http.StatusUnauthorized},
		// The map-image upload-url routes (uo69) must sit inside the auth group.
		"chapter map upload requires a token":   {http.MethodPost, "/api/app/campaigns/c1/chapters/ch1/map-image/upload-url", http.StatusUnauthorized},
		"encounter map upload requires a token": {http.MethodPost, "/api/app/campaigns/c1/encounters/e1/map-image/upload-url", http.StatusUnauthorized},
		// we5k.2.4: lets-roll's inbound route takes no user token; it's dark until
		// LETS_ROLL_TASK_ROLE_ARN is set, and the GM kill route still needs one.
		"kill reports are dark":     {http.MethodPost, "/api/app/campaigns/c1/encounters/e1/kill-reports", http.StatusServiceUnavailable},
		"GM kills require a token":  {http.MethodPost, "/api/app/campaigns/c1/encounters/e1/kills", http.StatusUnauthorized},
		"encounter still protected": {http.MethodGet, "/api/app/campaigns/c1/encounters/e1", http.StatusUnauthorized},
	}
	for name, tc := range cases {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != tc.want {
			t.Errorf("%s: %s %s = %d, want %d", name, tc.method, tc.path, rec.Code, tc.want)
		}
	}
}

// TestNewRouter_PanicsWithoutVerifier: a nil Auth is a construction-time error,
// not a first-request nil panic.
func TestNewRouter_PanicsWithoutVerifier(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected NewRouter to panic on a nil Auth verifier")
		}
	}()
	NewRouter(Config{Env: "test"})
}

// TestNewRouter_PanicsWithoutStoreOrLetsRoll: with Auth present, a nil Store or
// LetsRoll is still a construction-time panic (the campaign handlers deref them).
func TestNewRouter_PanicsWithoutStoreOrLetsRoll(t *testing.T) {
	v, err := auth.NewVerifier(context.Background(), auth.Config{
		Issuer:  "https://issuer.test",
		JWKSURL: "http://127.0.0.1:1/jwks",
	})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	st, lr := testDeps()
	cases := map[string]Config{
		"nil store":    {Auth: v, Env: "test", LetsRoll: lr},
		"nil letsroll": {Auth: v, Env: "test", Store: st},
	}
	for name, cfg := range cases {
		func() {
			defer func() {
				if recover() == nil {
					t.Fatalf("%s: expected NewRouter to panic", name)
				}
			}()
			NewRouter(cfg)
		}()
	}
}

// TestNewRouter_MountsMapImageUploadRoutes: the handler tests build their own
// route tables, and on staging auth 401s before routing, so only a walk of the real
// router proves both map-image upload-url routes are registered (uo69).
func TestNewRouter_MountsMapImageUploadRoutes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	v, err := auth.NewVerifier(ctx, auth.Config{Issuer: "https://issuer.test", JWKSURL: "http://127.0.0.1:1/jwks"})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	st, lr := testDeps()
	got := map[string]bool{}
	err = chi.Walk(NewRouter(Config{Auth: v, Env: "test", Store: st, LetsRoll: lr}),
		func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
			got[method+" "+route] = true
			return nil
		})
	if err != nil {
		t.Fatalf("chi.Walk: %v", err)
	}
	for _, want := range []string{
		"POST /api/app/campaigns/{campaignID}/chapters/{chapterID}/map-image/upload-url",     // muz0.1
		"POST /api/app/campaigns/{campaignID}/encounters/{encounterID}/map-image/upload-url", // muz0.7
	} {
		if !got[want] {
			t.Errorf("route not registered: %s", want)
		}
	}
}
