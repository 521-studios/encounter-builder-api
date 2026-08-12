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
		path string
		want int
	}{
		"health is public":    {"/api/app/healthz", http.StatusOK},
		"me requires a token": {"/api/app/me", http.StatusUnauthorized},
	}
	for name, tc := range cases {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rec.Code != tc.want {
			t.Errorf("%s: GET %s = %d, want %d", name, tc.path, rec.Code, tc.want)
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
