package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"github.com/521studios/encounter-builder-api/internal/auth"
	"github.com/521studios/encounter-builder-api/internal/combatpush"
	"github.com/521studios/encounter-builder-api/internal/letsroll"
	"github.com/521studios/encounter-builder-api/internal/lrident"
	"github.com/521studios/encounter-builder-api/internal/maps"
	"github.com/521studios/encounter-builder-api/internal/partytreasure"
	"github.com/521studios/encounter-builder-api/internal/store"
	"github.com/521studios/encounter-builder-api/internal/svctoken"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
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
	svcIssuer := os.Getenv("SVC_OIDC_ISSUER")
	if svcIssuer == "" {
		svcIssuer = issuer
	}
	if svcIssuer != issuer {
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
		push := combatpush.New(lrURL, env, awsCfg.Credentials, awsCfg.Region)
		cfg.CombatPush = push
		// we5k.2.6: once the service issuer is the auth service (not lets-roll),
		// the push carries EB's client-credentials token instead of the STS
		// identity. So the issuer flip is the one switch. Its secret is written
		// by 521-auth's client registration. If it can't be read the push stays
		// off (503, and /me hides it) rather than sending a scheme lets-roll
		// no longer takes.
		if svcIssuer != lrURL {
			src, err := clientTokens(ctx, secretsmanager.NewFromConfig(awsCfg), svcIssuer, env)
			if err != nil {
				slog.Error("combat push disabled: no service client credentials", "err", err)
				cfg.CombatPush = nil
			} else {
				push.Tokens = src
			}
		}
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

type secretGetter interface {
	GetSecretValue(context.Context, *secretsmanager.GetSecretValueInput, ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error)
}

// clientTokens reads EB's client credentials (JSON {client_id, client_secret} at
// auth-service/<env>/clients/encounter-builder-<env>, owned by 521-auth) and
// returns a token source for the lets-roll push scope.
func clientTokens(ctx context.Context, sm secretGetter, issuer, env string) (*svctoken.Source, error) {
	name := "auth-service/" + env + "/clients/encounter-builder-" + env
	out, err := sm.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: &name})
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	var c struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if out.SecretString == nil || json.Unmarshal([]byte(*out.SecretString), &c) != nil || c.ClientID == "" || c.ClientSecret == "" {
		return nil, fmt.Errorf("%s is not {client_id, client_secret}", name)
	}
	return svctoken.New(issuer, c.ClientID, c.ClientSecret, "lr:eb-push"), nil
}
