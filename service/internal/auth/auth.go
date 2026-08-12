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
	"net/http"
	"strings"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"
)

type ctxKey string

const (
	subjectKey ctxKey = "oidc.subject"
	tokenKey   ctxKey = "oidc.token"
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

// Verifier validates bearer tokens against a cached JWKS.
type Verifier struct {
	issuer   string
	audience string
	jwksURL  string
	cache    *jwk.Cache
}

// NewVerifier discovers the JWKS (unless Config.JWKSURL is set), warms the
// cache, and returns a Verifier. It fails if the provider is unreachable at
// startup — the identity provider is a hard dependency, so fail loud.
func NewVerifier(ctx context.Context, cfg Config) (*Verifier, error) {
	if cfg.Issuer == "" {
		return nil, errors.New("auth: Issuer is required")
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	jwksURL := cfg.JWKSURL
	if jwksURL == "" {
		var err error
		jwksURL, err = discoverJWKS(ctx, httpClient, cfg.Issuer)
		if err != nil {
			return nil, err
		}
	}

	cache := jwk.NewCache(ctx)
	if err := cache.Register(jwksURL,
		jwk.WithHTTPClient(httpClient),
		jwk.WithMinRefreshInterval(15*time.Minute),
	); err != nil {
		return nil, fmt.Errorf("auth: register jwks: %w", err)
	}
	if _, err := cache.Refresh(ctx, jwksURL); err != nil {
		return nil, fmt.Errorf("auth: fetch jwks %s: %w", jwksURL, err)
	}

	return &Verifier{
		issuer:   cfg.Issuer,
		audience: cfg.Audience,
		jwksURL:  jwksURL,
		cache:    cache,
	}, nil
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
	set, err := v.cache.Get(ctx, v.jwksURL)
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
			writeUnauthorized(w, "invalid token")
			return
		}
		ctx := context.WithValue(r.Context(), subjectKey, tok.Subject())
		ctx = context.WithValue(ctx, tokenKey, tok)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return strings.TrimSpace(h[len(prefix):])
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
