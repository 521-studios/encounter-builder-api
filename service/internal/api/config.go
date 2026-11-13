package api

import (
	"context"
	"fmt"
	"os"

	"github.com/521studios/encounter-builder-api/internal/auth"
	"github.com/521studios/encounter-builder-api/internal/combatpush"
	"github.com/521studios/encounter-builder-api/internal/letsroll"
	"github.com/521studios/encounter-builder-api/internal/lrident"
	"github.com/521studios/encounter-builder-api/internal/maps"
	"github.com/521studios/encounter-builder-api/internal/partytreasure"
	"github.com/521studios/encounter-builder-api/internal/store"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// BuildConfig assembles the router Config from the environment: the OIDC
// verifier, the DynamoDB-backed store, and the lets-roll client (at LETS_ROLL_URL,
// which defaults to the OIDC issuer until the auth-service cut-over).
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

	// lets-roll's API host. It's the issuer today, but not after the auth-service
	// cut-over (auth-service.md), so it's its own setting, defaulting to the issuer.
	lrURL := os.Getenv("LETS_ROLL_URL")
	if lrURL == "" {
		lrURL = issuer
	}

	cfg := Config{
		Auth:            verifier,
		Env:             env,
		Store:           store.New(dynamodb.NewFromConfig(awsCfg), table),
		LetsRoll:        letsroll.New(lrURL),
		Service:         verifier,
		ServiceAudience: "encounter-builder-" + env + "-svc",
	}
	// we5k.2.6: service tokens (lets-roll's kill reports) are verified against
	// SVC_OIDC_ISSUER, defaulting to OIDC_ISSUER. Same issuer: share the GM
	// verifier's JWKS cache (the audience is checked per route). Before the
	// cut-over the issuer is lets-roll, which mints no service audience, so the
	// service path admits nothing until the issuer config flips.
	if svcIssuer := os.Getenv("SVC_OIDC_ISSUER"); svcIssuer != "" && svcIssuer != issuer {
		if cfg.Service, err = auth.NewVerifier(ctx, auth.Config{Issuer: svcIssuer, Audience: cfg.ServiceAudience}); err != nil {
			return Config{}, fmt.Errorf("service auth init: %w", err)
		}
	}
	// §5b: push released loot to party-treasure when its Function URL is configured.
	// Empty (local/unset) leaves PartyTreasure nil and the handler skips the push.
	if url := os.Getenv("PARTY_TREASURE_URL"); url != "" {
		cfg.PartyTreasure = partytreasure.New(url, awsCfg.Credentials, awsCfg.Region)
	}
	// we5k.2.3: off until lets-roll serves POST /api/v1/games/:id/eb_encounters
	// (lets-roll-f6e). lets-roll is the OIDC issuer host.
	if os.Getenv("LETS_ROLL_COMBAT_PUSH") == "true" {
		cfg.CombatPush = combatpush.New(lrURL, env, awsCfg.Credentials, awsCfg.Region)
	}
	// we5k.2.4: off until lets-roll signs kill reports with its task role.
	if arn := os.Getenv("LETS_ROLL_TASK_ROLE_ARN"); arn != "" {
		if cfg.LetsRollIdentity, err = lrident.New(arn, env, awsCfg.Region); err != nil {
			return Config{}, err
		}
	}
	if bucket := os.Getenv("MAPS_BUCKET"); bucket != "" {
		cfg.Maps = maps.New(s3.NewFromConfig(awsCfg), bucket)
	}
	return cfg, nil
}
