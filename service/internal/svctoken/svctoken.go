// Package svctoken fetches and caches EB's own client-credentials access token
// from the auth service (we5k.2.6, auth-service.md "Service tokens"), for EB's
// calls into other services (the lets-roll combat push, scope lr:eb-push). The
// scope picks the audience; the client authenticates with client_secret_post.
package svctoken

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// refreshBefore renews the token this long before it expires, so a call never
// leaves with a token that dies in flight (tokens live 5 min).
const refreshBefore = 60 * time.Second

// Source hands out a cached token for one scope.
type Source struct {
	issuer, clientID, secret, scope string
	http                            *http.Client
	now                             func() time.Time

	mu       sync.Mutex // serialises fetches; held across the network call
	tokenURL string     // discovered once
	tok      string
	exp      time.Time
}

// New builds a Source. Nothing is fetched until the first Token call.
func New(issuer, clientID, secret, scope string) *Source {
	return &Source{
		issuer:   strings.TrimRight(issuer, "/"),
		clientID: clientID,
		secret:   secret,
		scope:    scope,
		http:     &http.Client{Timeout: 5 * time.Second},
		now:      time.Now,
	}
}

// Token returns a valid access token, fetching a new one when the cached one is
// missing or within refreshBefore of expiry.
func (s *Source) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tok != "" && s.now().Before(s.exp.Add(-refreshBefore)) {
		return s.tok, nil
	}
	if s.tokenURL == "" {
		u, err := s.discover(ctx)
		if err != nil {
			return "", err
		}
		s.tokenURL = u
	}
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"scope":         {s.scope},
		"client_id":     {s.clientID},
		"client_secret": {s.secret},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := s.do(req, &out); err != nil {
		return "", err
	}
	if out.AccessToken == "" || out.ExpiresIn <= 0 {
		return "", fmt.Errorf("svctoken: token response has no access_token or expires_in")
	}
	s.tok, s.exp = out.AccessToken, s.now().Add(time.Duration(out.ExpiresIn)*time.Second)
	return s.tok, nil
}

// discover reads token_endpoint from the issuer's OIDC discovery document.
func (s *Source) discover(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.issuer+"/.well-known/openid-configuration", nil)
	if err != nil {
		return "", err
	}
	var doc struct {
		TokenEndpoint string `json:"token_endpoint"`
	}
	if err := s.do(req, &doc); err != nil {
		return "", err
	}
	if doc.TokenEndpoint == "" {
		return "", fmt.Errorf("svctoken: discovery has no token_endpoint")
	}
	return doc.TokenEndpoint, nil
}

func (s *Source) do(req *http.Request, out any) error {
	resp, err := s.http.Do(req)
	if err != nil {
		return fmt.Errorf("svctoken: %s: %w", req.URL.Path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// The body may echo the error code (invalid_client, invalid_scope); never the secret.
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("svctoken: %s returned %d: %s", req.URL.Path, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(out); err != nil {
		return fmt.Errorf("svctoken: decode %s: %w", req.URL.Path, err)
	}
	return nil
}
