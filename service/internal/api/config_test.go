package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

func TestBuildConfig_RequiresEnv(t *testing.T) {
	// No AWS calls are reached: the required-env guards return first.
	t.Setenv("OIDC_ISSUER", "")
	t.Setenv("ENCOUNTERS_TABLE", "")
	if _, err := BuildConfig(context.Background(), "test"); err == nil || !strings.Contains(err.Error(), "OIDC_ISSUER") {
		t.Fatalf("missing issuer: err=%v, want OIDC_ISSUER error", err)
	}

	t.Setenv("OIDC_ISSUER", "https://issuer.test")
	if _, err := BuildConfig(context.Background(), "test"); err == nil || !strings.Contains(err.Error(), "ENCOUNTERS_TABLE") {
		t.Fatalf("missing table: err=%v, want ENCOUNTERS_TABLE error", err)
	}
}

func TestBuildConfig_MapsBucketEnablesMapStore(t *testing.T) {
	// Offline: OIDC discovery is lazy, and AWS config loads without network.
	t.Setenv("OIDC_ISSUER", "https://issuer.test")
	t.Setenv("ENCOUNTERS_TABLE", "t")
	t.Setenv("AWS_REGION", "us-east-2")
	t.Setenv("MAPS_BUCKET", "")
	cfg, err := BuildConfig(context.Background(), "test")
	if err != nil || cfg.Maps != nil {
		t.Fatalf("no MAPS_BUCKET: maps=%v err=%v, want nil store", cfg.Maps, err)
	}
	t.Setenv("MAPS_BUCKET", "b")
	if cfg, err = BuildConfig(context.Background(), "test"); err != nil || cfg.Maps == nil {
		t.Fatalf("MAPS_BUCKET set: maps=%v err=%v, want a store", cfg.Maps, err)
	}
}

type fakeSecrets struct{ val *string }

func (f fakeSecrets) GetSecretValue(_ context.Context, in *secretsmanager.GetSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
	if *in.SecretId != "auth-service/staging/clients/encounter-builder-staging" {
		return nil, fmt.Errorf("unexpected secret %q", *in.SecretId)
	}
	if f.val == nil {
		return nil, errors.New("ResourceNotFoundException")
	}
	return &secretsmanager.GetSecretValueOutput{SecretString: f.val}, nil
}

// we5k.2.6: EB's client credentials come from 521-auth's secret; anything but a
// complete {client_id, client_secret} is an error (the push then stays off).
func TestClientTokens(t *testing.T) {
	str := func(s string) *string { return &s }
	if _, err := clientTokens(context.Background(), fakeSecrets{str(`{"client_id":"encounter-builder-staging","client_secret":"x"}`)}, "https://auth.test", "staging"); err != nil {
		t.Fatalf("valid secret: %v", err)
	}
	for name, v := range map[string]*string{"missing": nil, "not json": str("x"), "no secret": str(`{"client_id":"c"}`)} {
		if _, err := clientTokens(context.Background(), fakeSecrets{v}, "https://auth.test", "staging"); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}
