// Command lambda is the AWS Lambda entrypoint. It wraps the shared chi router
// in the API-Gateway-v2 proxy adapter (matching the Function URL payload
// format) and starts the Lambda runtime.
package main

import (
	"context"
	"log"
	"time"

	"github.com/521studios/encounter-builder-api/internal/api"
	"github.com/aws/aws-lambda-go/lambda"
	chiproxy "github.com/awslabs/aws-lambda-go-api-proxy/chi"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg, err := api.BuildConfig(ctx, "production")
	if err != nil {
		log.Fatalf("startup: %v", err)
	}

	adapter := chiproxy.NewV2(api.NewRouter(cfg))
	lambda.Start(adapter.ProxyWithContextV2)
}
