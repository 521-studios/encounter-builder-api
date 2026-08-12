// Package letsroll is a thin client for lets-roll's /api/v1, used to authorize
// campaign operations. encounter-builder-api owns no notion of who runs a game;
// it forwards the caller's bearer to lets-roll and trusts lets-roll's answer.
package letsroll

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ErrForbidden means lets-roll says the caller can't see this game — either
// they aren't a member or it doesn't exist. Either way, no access.
var ErrForbidden = fmt.Errorf("letsroll: caller has no access to this game")

// Client talks to a lets-roll instance. baseURL is the same host as the OIDC
// issuer (e.g. https://lets-roll.staging.521studios.com).
type Client struct {
	baseURL string
	http    *http.Client
}

// New builds a client. baseURL trailing slash is trimmed.
func New(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 5 * time.Second},
	}
}

// Game is the subset of lets-roll's /api/v1/games/:id we care about.
type Game struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	AmGM bool   `json:"am_gm"`
}

// FetchGame calls GET /api/v1/games/:id as the user (forwarding their bearer).
// 404/403 collapse to ErrForbidden — the caller has no business with this game.
func (c *Client) FetchGame(ctx context.Context, bearer, gameID string) (Game, error) {
	url := fmt.Sprintf("%s/api/v1/games/%s", c.baseURL, gameID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Game{}, err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return Game{}, fmt.Errorf("letsroll: fetch game: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var g Game
		if err := json.NewDecoder(resp.Body).Decode(&g); err != nil {
			return Game{}, fmt.Errorf("letsroll: decode game: %w", err)
		}
		return g, nil
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		// Our verifier already accepted this bearer, so a 401 here means
		// lets-roll itself rejected it — from the caller's view, no access.
		return Game{}, ErrForbidden
	default:
		return Game{}, fmt.Errorf("letsroll: unexpected status %d fetching game", resp.StatusCode)
	}
}
