package api

import (
	"context"
	"fmt"
	"os"

	"github.com/521studios/encounter-builder-api/internal/auth"
	"github.com/521studios/encounter-builder-api/internal/combatpush"
	"github.com/521studios/encounter-builder-api/internal/letsroll"
	"github.com/521studios/encounter-builder-api/internal/maps"
	"github.com/521studios/encounter-builder-api/internal/partytreasure"
	"github.com/521studios/encounter-builder-api/internal/store"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
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

	cfg := Config{
		Auth:     verifier,
		Env:      env,
		Store:    store.New(dynamodb.NewFromConfig(awsCfg), table),
		LetsRoll: letsroll.New(issuer),
	}
	// §5b: push released loot to party-treasure when its Function URL is configured.
	// Empty (local/unset) leaves PartyTreasure nil and the handler skips the push.
	if url := os.Getenv("PARTY_TREASURE_URL"); url != "" {
		cfg.PartyTreasure = partytreasure.New(url, awsCfg.Credentials, awsCfg.Region)
	}
	// we5k.2.3: off until lets-roll serves POST /api/v1/games/:id/eb_encounters
	// (lets-roll-f6e). lets-roll is the OIDC issuer host.
	if os.Getenv("LETS_ROLL_COMBAT_PUSH") == "true" {
		cfg.CombatPush = combatpush.New(issuer, env, awsCfg.Credentials, awsCfg.Region)
	}
	if bucket := os.Getenv("MAPS_BUCKET"); bucket != "" {
		cfg.Maps = maps.New(s3.NewFromConfig(awsCfg), bucket)
	}
	return cfg, nil
}
