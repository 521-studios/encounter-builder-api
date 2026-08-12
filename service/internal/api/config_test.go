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
