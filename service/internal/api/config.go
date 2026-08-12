package api

import (
	"context"
	"fmt"
	"os"

	"github.com/521studios/encounter-builder-api/internal/auth"
	"github.com/521studios/encounter-builder-api/internal/letsroll"
	"github.com/521studios/encounter-builder-api/internal/store"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
)

// BuildConfig assembles the router Config from the environment: the OIDC
// verifier, the DynamoDB-backed store, and the lets-roll client (whose base URL
// is the OIDC issuer — lets-roll is both the token issuer and the /api/v1 host).
// envDefault names ENV when it's unset. Shared by the lambda and local mains.
func BuildConfig(ctx context.Context, envDefault string) (Config, error) {
	issuer := os.Getenv("OIDC_ISSUER")
	if issuer == "" {
		return Config{}, fmt.Errorf("OIDC_ISSUER is required")
	}
	table := os.Getenv("ENCOUNTERS_TABLE")
	if table == "" {
		return Config{}, fmt.Errorf("ENCOUNTERS_TABLE is required")
	}

	verifier, err := auth.NewVerifier(ctx, auth.Config{
		Issuer:   issuer,
		Audience: os.Getenv("OIDC_AUDIENCE"),
	})
	if err != nil {
		return Config{}, fmt.Errorf("auth init: %w", err)
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return Config{}, fmt.Errorf("aws config: %w", err)
	}

	env := os.Getenv("ENV")
	if env == "" {
		env = envDefault
	}

	return Config{
		Auth:     verifier,
		Env:      env,
		Store:    store.New(dynamodb.NewFromConfig(awsCfg), table),
		LetsRoll: letsroll.New(issuer),
	}, nil
}
