package api

import (
	"context"
	"strings"
	"testing"
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
