package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwa"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"
)

// testProvider stands up a fake OIDC provider: a discovery endpoint and a JWKS
// endpoint serving the public half of a freshly generated RSA key, plus a
// signer for minting tokens against it.
type testProvider struct {
	server   *httptest.Server
	issuer   string
	signKey  jwk.Key // private, kid set
	audience string
}

func newTestProvider(t *testing.T) *testProvider {
	t.Helper()
	raw, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	priv, err := jwk.FromRaw(raw)
	if err != nil {
		t.Fatalf("jwk from raw: %v", err)
	}
	_ = priv.Set(jwk.KeyIDKey, "test-key-1")
	_ = priv.Set(jwk.AlgorithmKey, jwa.RS256)
	pub, err := priv.PublicKey()
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	pubSet := jwk.NewSet()
	_ = pubSet.AddKey(pub)

	tp := &testProvider{signKey: priv, audience: "encounter-builder-web"}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":   tp.issuer,
			"jwks_uri": tp.issuer + "/oauth/discovery/keys",
		})
	})
	mux.HandleFunc("/oauth/discovery/keys", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(pubSet)
	})
	tp.server = httptest.NewServer(mux)
	tp.issuer = tp.server.URL
	return tp
}

func (tp *testProvider) close() { tp.server.Close() }

// mint builds and signs a token, applying any per-test overrides.
func (tp *testProvider) mint(t *testing.T, build func(b *jwt.Builder) *jwt.Builder) string {
	t.Helper()
	b := jwt.NewBuilder().
		Issuer(tp.issuer).
		Subject("42").
		Audience([]string{tp.audience}).
		IssuedAt(time.Now()).
		Expiration(time.Now().Add(15 * time.Minute))
	if build != nil {
		b = build(b)
	}
	tok, err := b.Build()
	if err != nil {
		t.Fatalf("build token: %v", err)
	}
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.RS256, tp.signKey))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return string(signed)
}

func (tp *testProvider) verifier(t *testing.T) *Verifier {
	t.Helper()
	v, err := NewVerifier(context.Background(), Config{
		Issuer:   tp.issuer,
		Audience: tp.audience,
	})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	return v
}

func TestVerify_ValidTokenYieldsSubject(t *testing.T) {
	tp := newTestProvider(t)
	defer tp.close()
	v := tp.verifier(t) // discovers JWKS from the fake issuer, no JWKSURL override

	tok, err := v.Verify(context.Background(), tp.mint(t, nil))
	if err != nil {
		t.Fatalf("expected valid token, got error: %v", err)
	}
	if tok.Subject() != "42" {
		t.Fatalf("subject = %q, want 42", tok.Subject())
	}
}

func TestVerify_RejectsBadCases(t *testing.T) {
	tp := newTestProvider(t)
	defer tp.close()
	v := tp.verifier(t)

	// A token signed by a DIFFERENT key must fail signature verification.
	other := newTestProvider(t)
	defer other.close()
	foreign := other.mint(t, func(b *jwt.Builder) *jwt.Builder { return b.Issuer(tp.issuer) })

	cases := map[string]string{
		"wrong signature": foreign,
		"wrong audience":  tp.mint(t, func(b *jwt.Builder) *jwt.Builder { return b.Audience([]string{"some-other-app"}) }),
		"wrong issuer":    tp.mint(t, func(b *jwt.Builder) *jwt.Builder { return b.Issuer("https://evil.example.com") }),
		"expired":         tp.mint(t, func(b *jwt.Builder) *jwt.Builder { return b.Expiration(time.Now().Add(-1 * time.Hour)) }),
	}
	for name, raw := range cases {
		if _, err := v.Verify(context.Background(), raw); err == nil {
			t.Errorf("%s: expected rejection, got nil error", name)
		}
	}
}

func TestMiddleware_401WithoutValidToken(t *testing.T) {
	tp := newTestProvider(t)
	defer tp.close()
	v := tp.verifier(t)

	protected := v.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(Subject(r.Context())))
	}))

	// No token -> 401.
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: status = %d, want 401", rec.Code)
	}

	// Valid token -> 200 and the subject reaches the handler.
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+tp.mint(t, nil))
	protected.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid token: status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "42" {
		t.Fatalf("handler saw subject %q, want 42", rec.Body.String())
	}
}

// TestMiddleware_PropagatesRawToken proves the verified bearer reaches the
// handler via RawToken — the forwarding path the campaign GM-check depends on.
func TestMiddleware_PropagatesRawToken(t *testing.T) {
	tp := newTestProvider(t)
	defer tp.close()
	v := tp.verifier(t)

	protected := v.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(RawToken(r.Context())))
	}))

	raw := tp.mint(t, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	protected.ServeHTTP(rec, req)
	if rec.Body.String() != raw {
		t.Fatalf("handler saw raw token %q, want the verified bearer", rec.Body.String())
	}
}

// TestMiddleware_AcceptsForwardedBearerHeader proves the X-Access-Token
// fallback: behind CloudFront OAC the bearer arrives here (Authorization is
// consumed by SigV4), and the middleware must still authenticate.
func TestMiddleware_AcceptsForwardedBearerHeader(t *testing.T) {
	tp := newTestProvider(t)
	defer tp.close()
	v := tp.verifier(t)

	protected := v.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(Subject(r.Context())))
	}))

	// No Authorization header — token only in X-Access-Token (raw JWT).
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Access-Token", tp.mint(t, nil))
	protected.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("forwarded bearer: status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "42" {
		t.Fatalf("handler saw subject %q, want 42", rec.Body.String())
	}
}

// TestMiddleware_RejectsInvalidToken guards the fail-open path: a present but
// invalid token must 401 AND must not reach the handler. (Without this, a
// regression that dropped the Verify-error early-return would go uncaught.)
func TestMiddleware_RejectsInvalidToken(t *testing.T) {
	tp := newTestProvider(t)
	defer tp.close()
	v := tp.verifier(t)

	reached := false
	protected := v.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
	}))

	tokens := map[string]string{
		"garbage":        "not-a-jwt",
		"expired":        tp.mint(t, func(b *jwt.Builder) *jwt.Builder { return b.Expiration(time.Now().Add(-time.Hour)) }),
		"wrong audience": tp.mint(t, func(b *jwt.Builder) *jwt.Builder { return b.Audience([]string{"evil"}) }),
	}
	for name, token := range tokens {
		reached = false
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		protected.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", name, rec.Code)
		}
		if reached {
			t.Errorf("%s: handler was reached despite an invalid token (fail-open!)", name)
		}
	}
}

// TestVerify_RejectsAlgNone covers the classic alg=none JWT bypass vector.
func TestVerify_RejectsAlgNone(t *testing.T) {
	tp := newTestProvider(t)
	defer tp.close()
	v := tp.verifier(t)

	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload, _ := json.Marshal(map[string]any{
		"sub": "42", "iss": tp.issuer, "aud": tp.audience, "exp": time.Now().Add(time.Hour).Unix(),
	})
	none := header + "." + base64.RawURLEncoding.EncodeToString(payload) + "."
	if _, err := v.Verify(context.Background(), none); err == nil {
		t.Fatal("alg=none token was accepted")
	}
}

// TestVerify_RejectsUnknownKid: a token whose kid isn't in the JWKS is rejected
// (key not found), distinct from the same-kid wrong-signature case.
func TestVerify_RejectsUnknownKid(t *testing.T) {
	tp := newTestProvider(t)
	defer tp.close()
	v := tp.verifier(t)

	raw, _ := rsa.GenerateKey(rand.Reader, 2048)
	k, _ := jwk.FromRaw(raw)
	_ = k.Set(jwk.KeyIDKey, "unknown-kid")
	_ = k.Set(jwk.AlgorithmKey, jwa.RS256)
	tok, _ := jwt.NewBuilder().Issuer(tp.issuer).Subject("42").
		Audience([]string{tp.audience}).Expiration(time.Now().Add(time.Hour)).Build()
	signed, _ := jwt.Sign(tok, jwt.WithKey(jwa.RS256, k))
	if _, err := v.Verify(context.Background(), string(signed)); err == nil {
		t.Fatal("token with an unknown kid was accepted")
	}
}

// TestVerify_RejectsMissingAudience: when an audience is required, a token with
// no aud claim at all must be rejected (absence, not just mismatch).
func TestVerify_RejectsMissingAudience(t *testing.T) {
	tp := newTestProvider(t)
	defer tp.close()
	v := tp.verifier(t) // audience = encounter-builder-web

	tok, _ := jwt.NewBuilder().Issuer(tp.issuer).Subject("42").
		Expiration(time.Now().Add(time.Hour)).Build() // no Audience()
	signed, _ := jwt.Sign(tok, jwt.WithKey(jwa.RS256, tp.signKey))
	if _, err := v.Verify(context.Background(), string(signed)); err == nil {
		t.Fatal("token with no audience claim was accepted despite a required audience")
	}
}

// TestLiveProvider_JWKSLoads proves the seam against the REAL lets-roll OIDC
// provider: discovery + JWKS actually fetch and parse. Opt-in (needs network)
// via OIDC_ISSUER, so CI stays hermetic; run locally with e.g.
//
//	OIDC_ISSUER=https://lets-roll.staging.521studios.com go test ./internal/auth -run Live
func TestLiveProvider_JWKSLoads(t *testing.T) {
	issuer := os.Getenv("OIDC_ISSUER")
	if issuer == "" {
		t.Skip("set OIDC_ISSUER to run the live-provider check")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := NewVerifier(ctx, Config{Issuer: issuer, Audience: ""}); err != nil {
		t.Fatalf("live provider %s: %v", issuer, err)
	}
}
