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
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg, err := api.BuildConfig(ctx, "local")
	if err != nil {
		log.Fatalf("startup: %v", err)
	}

	router := api.NewRouter(cfg)
	port := envOrDefault("PORT", "8090")
	log.Printf("encounter-builder-api listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, router))
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
