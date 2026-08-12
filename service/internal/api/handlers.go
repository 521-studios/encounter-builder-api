package api

import (
	"encoding/json"
	"net/http"

	"github.com/521studios/encounter-builder-api/internal/auth"
)

type handler struct {
	cfg Config
}

func (h *handler) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "env": h.cfg.Env})
}

// me echoes the verified identity from the bearer token. It's the end-to-end
// proof of the auth seam: reachable only through the auth middleware, so a 200
// here means the token was minted by the OIDC provider and verified against its
// JWKS. The encounter/treasure CRUD (Milestone 2) hangs off this same guard.
func (h *handler) me(w http.ResponseWriter, r *http.Request) {
	tok := auth.Token(r.Context())
	if tok == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "no token"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sub":       tok.Subject(),
		"issuer":    tok.Issuer(),
		"audience":  tok.Audience(),
		"expiresAt": tok.Expiration(),
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
