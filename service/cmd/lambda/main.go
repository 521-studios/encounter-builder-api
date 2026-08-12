// Command lambda is the AWS Lambda entrypoint. It wraps the shared chi router
// in the API-Gateway-v2 proxy adapter (matching the Function URL payload
// format) and starts the Lambda runtime.
package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/521studios/encounter-builder-api/internal/api"
	"github.com/521studios/encounter-builder-api/internal/auth"
	"github.com/aws/aws-lambda-go/lambda"
	chiproxy "github.com/awslabs/aws-lambda-go-api-proxy/chi"
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

	router := api.NewRouter(api.Config{Auth: verifier, Env: os.Getenv("ENV")})
	adapter := chiproxy.NewV2(router)
	lambda.Start(adapter.ProxyWithContextV2)
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("%s is required", key)
	}
	return v
}
