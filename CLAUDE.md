# encounter-builder-api

Go Lambda backend for the Encounter Builder, part of the 521 cluster. Serves
`/api/app/*`; authenticates via lets-roll's OIDC provider and stores encounters
in DynamoDB. Architecture: `../521-architect/docs/architecture/encounter-treasure-cluster.md`.

## Layout
```
encounter-builder-api/
├── service/                  # Go module (root at service/)
│   ├── cmd/lambda/           # Lambda entrypoint (chi via API-GW-v2 proxy)
│   ├── cmd/local/            # local net/http server (:8090)
│   └── internal/
│       ├── api/              # NewRouter + handlers (routes under /api/app)
│       └── auth/             # OIDC bearer-token verification (JWKS)
├── terraform/                # Lambda + Function URL + DynamoDB
└── .github/workflows/        # ci.yml (gofmt+vet+test), deploy.yml (guarded CD)
```

## Auth
Every non-health route requires a valid bearer access token minted by the
lets-roll OIDC provider. `internal/auth` fetches the provider's JWKS (via OIDC
discovery from `OIDC_ISSUER`), caches it, and verifies RS256 signature + `iss`
+ `exp` + (optional) `aud` offline on the request path. The verified `sub` is on
the request context (`auth.Subject(ctx)`). This is app-level authZ on top of the
Function URL's AWS_IAM/CloudFront-OAC network gate.

The bearer is read from `Authorization: Bearer <jwt>`, or, as a fallback, from
`X-Access-Token` (raw JWT). The fallback exists because behind CloudFront Lambda
OAC the viewer's `Authorization` header is overwritten by the OAC SigV4
signature, so the SPA forwards the OIDC token in `X-Access-Token` (passed through
by the CloudFront origin-request-policy). The JWT is fully verified either way.

Env: `OIDC_ISSUER` (required), `OIDC_AUDIENCE` (the SPA client_id), `ENV`,
`LETS_ROLL_URL` (lets-roll's API host; defaults to `OIDC_ISSUER`, which stops
being lets-roll at the auth-service cut-over), `SVC_OIDC_ISSUER` (service-token
issuer; defaults to `OIDC_ISSUER`),
`ENCOUNTERS_TABLE`, `MAPS_BUCKET` (optional; unset makes map uploads 503),
`PARTY_TREASURE_URL` (optional; unset disables the release push, retry 503s, and
`/me` reports `party_treasure: false` so the SPA hides Release),
`LETS_ROLL_TASK_ROLE_ARN` (optional; lets-roll's ECS task role, whose STS
identity in `X-LR-Identity` admits its kill reports; unset makes them 503).

Service tokens (we5k.2.6, `../521-architect/docs/architecture/auth-service.md`):
a kill report carrying `X-Service-Token` is judged only as a client-credentials
JWT with `aud` = `encounter-builder-<env>-svc` and scope `eb:kill-report`, at most
5 min to live. Without that header it falls back to `X-LR-Identity` (lrident),
which is deleted at the cut-over. Before the cut-over the issuer (lets-roll) mints
no service audience, so the new path admits nothing.
Outbound, once the service issuer differs from `LETS_ROLL_URL` (the cut-over),
the combat push sends EB's client-credentials token (scope `lr:eb-push`,
`internal/svctoken`, secret `auth-service/<env>/clients/encounter-builder-<env>`)
in `X-Service-Token` and the GM's token in `X-Access-Token`, instead of the STS
identity. If that secret can't be read, the push stays off.

## Dev
```bash
docker compose up                        # Air hot-reload on :8090
cd service && go test ./...              # unit tests
# live-provider auth check (needs network):
cd service && OIDC_ISSUER=https://lets-roll.staging.521studios.com go test ./internal/auth -run Live
```

## Deploy
Guarded CD (matches infra-frontend): staging auto-applies on merge to `main`;
production is manual `workflow_dispatch`. The apply refuses to destroy/replace
any resource (notably the DynamoDB table) unless dispatched with
`confirm_destroy=true`. Build is `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 -> bootstrap`.

## Conventions
- `gofmt` clean (CI + pre-commit gate); `go vet` clean; tests via `go test`.
- All work via PR; staging deploys on merge.
