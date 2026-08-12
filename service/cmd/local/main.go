// Command local runs the shared chi router as a plain HTTP server for local
// development (docker compose / go run), on :8090 by default.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/521studios/encounter-builder-api/internal/api"
	"github.com/521studios/encounter-builder-api/internal/auth"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	verifier, err := auth.NewVerifier(ctx, auth.Config{
		Issuer:   mustEnv("OIDC_ISSUER"),
		Audience: os.Getenv("OIDC_AUDIENCE"),
	})
	if err != nil {
		log.Fatalf("auth init: %v", err)
	}

	router := api.NewRouter(api.Config{Auth: verifier, Env: envOrDefault("ENV", "local")})
	port := envOrDefault("PORT", "8090")
	log.Printf("encounter-builder-api listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, router))
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("%s is required", key)
	}
	return v
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
