// Package auth verifies the RS256 bearer access tokens minted by the lets-roll
// OIDC provider. It is app-level authorization on top of the Function URL's
// AWS_IAM/CloudFront-OAC network gate: the JWT proves *which user* is calling.
//
// The signing keys are fetched from the provider's JWKS (discovered from its
// issuer's /.well-known/openid-configuration) and cached with periodic refresh,
// so key rotation is picked up without a redeploy and verification is offline
// on the request path.
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"
)

type ctxKey string

const (
	subjectKey  ctxKey = "oidc.subject"
	tokenKey    ctxKey = "oidc.token"
	rawTokenKey ctxKey = "oidc.raw"
)

// Config configures a Verifier.
type Config struct {
	// Issuer is the OIDC issuer, e.g. https://lets-roll.staging.521studios.com.
	// It is both the discovery base and the expected `iss` claim.
	Issuer string
	// Audience, when non-empty, is the required `aud` claim (the SPA client_id,
	// e.g. encounter-builder-web). Empty accepts any audience.
	Audience string
	// JWKSURL overrides discovery (mainly for tests). Empty = discover from Issuer.
	JWKSURL string
	// HTTPClient is used for discovery + JWKS fetch. nil = http.DefaultClient.
	HTTPClient *http.Client
}

// Verifier validates bearer tokens against a cached JWKS. jwksURL is resolved
// via OIDC discovery and may be empty until the first authed request if the
// provider was unreachable at startup (see NewVerifier).
//
// sem is a 1-slot channel: holding the slot is what owns jwksURL. A channel, not
// a Mutex, so a request queued behind a slow discovery gives up on its OWN ctx
// rather than waiting out the holder's fetch (and sync.Once can't retry).
type Verifier struct {
	issuer     string
	audience   string
	httpClient *http.Client
	cache      *jwk.Cache
	sem        chan struct{}
	jwksURL    string
}

// NewVerifier returns a Verifier for the issuer. With Config.JWKSURL set it
// registers that URL directly (tests); otherwise it discovers the JWKS from the
// issuer.
//
// OIDC discovery is NOT a fatal startup dependency: a transient provider outage
// (e.g. lets-roll staging's nightly scale-to-zero) would otherwise crash every
// Lambda cold start into a 502 at the edge. A startup discovery failure is
// logged and deferred to the first authed request, which retries; the public
// /healthz and the SigV4 service-release path never touch the provider. JWKS
// warming is likewise best-effort.
func NewVerifier(ctx context.Context, cfg Config) (*Verifier, error) {
	if cfg.Issuer == "" {
		return nil, errors.New("auth: Issuer is required")
	}
	if cfg.Audience == "" {
		slog.Warn("auth: no Audience configured — access tokens for any client will be accepted")
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	// The JWKS cache's background refresh goroutine lives for the ctx passed to
	// NewCache. Callers hand us a short startup-timeout ctx, which would kill that
	// goroutine ~30s after boot and freeze key rotation — so detach it (keep
	// values, drop the deadline). The passed ctx still bounds the startup fetches.
	v := &Verifier{
		issuer:     cfg.Issuer,
		audience:   cfg.Audience,
		httpClient: httpClient,
		cache:      jwk.NewCache(context.WithoutCancel(ctx)),
		sem:        make(chan struct{}, 1),
	}

	var err error
	jwksURL := cfg.JWKSURL
	if jwksURL != "" {
		if err := v.register(jwksURL); err != nil {
			return nil, err
		}
	} else if jwksURL, err = v.resolve(ctx); err != nil {
		slog.Warn("auth: OIDC discovery failed at startup; will retry on first authed request",
			"issuer", cfg.Issuer, "err", err)
	}

	// Best-effort warm when the jwks url is known, so a healthy boot serves the
	// first authed request without a fetch stall. A blip is fine — the first
	// authed request triggers the fetch; a persistent failure is a 401 there.
	if jwksURL != "" {
		if _, err := v.cache.Refresh(ctx, jwksURL); err != nil {
			slog.Warn("auth: initial JWKS fetch failed; will retry on first request",
				"jwks", jwksURL, "err", err)
		}
	}

	return v, nil
}

// register adds jwksURL to the cache and records it. The caller holds v.sem
// (resolve) or runs at init before the Verifier is shared.
func (v *Verifier) register(jwksURL string) error {
	if err := v.cache.Register(jwksURL,
		jwk.WithHTTPClient(v.httpClient),
		jwk.WithMinRefreshInterval(15*time.Minute),
	); err != nil {
		return fmt.Errorf("auth: register jwks: %w", err)
	}
	v.jwksURL = jwksURL
	return nil
}

// resolve returns the jwks url, discovering and registering it on first need.
func (v *Verifier) resolve(ctx context.Context) (string, error) {
	select {
	case v.sem <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-v.sem }()
	if v.jwksURL != "" {
		return v.jwksURL, nil
	}
	jwksURL, err := discoverJWKS(ctx, v.httpClient, v.issuer)
	if err != nil {
		return "", err
	}
	if err := v.register(jwksURL); err != nil {
		return "", err
	}
	return v.jwksURL, nil
}

// discoverJWKS reads jwks_uri from the issuer's OIDC discovery document.
func discoverJWKS(ctx context.Context, client *http.Client, issuer string) (string, error) {
	url := strings.TrimRight(issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("auth: discovery %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("auth: discovery %s: status %d", url, resp.StatusCode)
	}
	var doc struct {
		JWKSURI string `json:"jwks_uri"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return "", fmt.Errorf("auth: decode discovery: %w", err)
	}
	if doc.JWKSURI == "" {
		return "", fmt.Errorf("auth: discovery %s has no jwks_uri", url)
	}
	return doc.JWKSURI, nil
}

// Verify parses and validates a raw bearer token, enforcing signature (against
// the current JWKS), issuer, expiry, and — when configured — audience.
func (v *Verifier) Verify(ctx context.Context, raw string) (jwt.Token, error) {
	// Resolve the jwks url on first use if startup discovery was deferred (the
	// provider was down at boot); a persistent failure surfaces here as a 401.
	jwksURL, err := v.resolve(ctx)
	if err != nil {
		return nil, fmt.Errorf("auth: jwks unavailable: %w", err)
	}
	set, err := v.cache.Get(ctx, jwksURL)
	if err != nil {
		return nil, fmt.Errorf("auth: jwks unavailable: %w", err)
	}
	opts := []jwt.ParseOption{
		jwt.WithKeySet(set),
		jwt.WithValidate(true),
		jwt.WithIssuer(v.issuer),
		jwt.WithAcceptableSkew(30 * time.Second),
	}
	if v.audience != "" {
		opts = append(opts, jwt.WithAudience(v.audience))
	}
	return jwt.ParseString(raw, opts...)
}

// Middleware requires a valid bearer token on every wrapped request and stashes
// the verified subject + token on the request context. It fails closed: any
// missing/invalid token is a 401.
func (v *Verifier) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := bearerToken(r)
		if raw == "" {
			writeUnauthorized(w, "missing bearer token")
			return
		}
		tok, err := v.Verify(r.Context(), raw)
		if err != nil {
			slog.Warn("auth: token rejected", "err", err)
			writeUnauthorized(w, "invalid token")
			return
		}
		ctx := context.WithValue(r.Context(), subjectKey, tok.Subject())
		ctx = context.WithValue(ctx, tokenKey, tok)
		ctx = WithRawToken(ctx, raw)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	// Fallback: behind CloudFront Lambda OAC the viewer's Authorization header is
	// overwritten by the OAC SigV4 signature, so the bearer can't ride there. The
	// SPA forwards the raw OIDC JWT in X-Access-Token, which the CloudFront
	// origin-request-policy passes through untouched. The token is still fully
	// verified downstream, so this is transport only — no trust in the header.
	if t := strings.TrimSpace(r.Header.Get("X-Access-Token")); t != "" {
		return t
	}
	return ""
}

func writeUnauthorized(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// Subject returns the verified user id (`sub`) from a request context, or "".
func Subject(ctx context.Context) string {
	s, _ := ctx.Value(subjectKey).(string)
	return s
}

// Token returns the verified token from a request context, or nil.
func Token(ctx context.Context) jwt.Token {
	t, _ := ctx.Value(tokenKey).(jwt.Token)
	return t
}

// RawToken returns the verified request's original bearer string, for
// forwarding to lets-roll's /api/v1 on the user's behalf (campaign authZ).
func RawToken(ctx context.Context) string {
	s, _ := ctx.Value(rawTokenKey).(string)
	return s
}

// WithRawToken seeds the bearer string the middleware sets. Exported so
// downstream packages (and their tests) can construct an authorized context.
func WithRawToken(ctx context.Context, raw string) context.Context {
	return context.WithValue(ctx, rawTokenKey, raw)
}
