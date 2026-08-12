# encounter-builder-api

Backend for the Encounter Builder — a Go Lambda serving encounters + treasure
for the 521 Studios cluster. Authenticates via lets-roll's OIDC provider
(bearer JWTs verified against its JWKS) and reads campaign/membership from
`lets-roll /api/v1`. Stores encounters in DynamoDB.

Part of the 521 cluster; see `../521-architect/docs/architecture/encounter-treasure-cluster.md`.

## Layout
- `service/` — Go module (chi router; `cmd/lambda` + `cmd/local` entrypoints; `internal/`)
- `terraform/` — Lambda + Function URL + DynamoDB
- `.github/workflows/` — CI (gofmt + test) and deploy (staging auto / prod manual)

## Local
    docker compose up        # Air hot-reload on :8090
    # or
    cd service && go run ./cmd/local
