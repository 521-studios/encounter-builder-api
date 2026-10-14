# Reviewer notes (orchestrator only; reviewers don't see this file)

Go reviewers for the Encounter Builder API (`service/` Go module, `terraform/` app layer).

**Dropped defaults:** `silent-failure-hunter` (covered by error-handling-reviewer), `comment-analyzer` (clarity-reviewer), `code-simplifier` (complexity-reviewer + dead-code-reviewer). Kept as starters: `code-reviewer`, `pr-test-analyzer`, `type-design-analyzer`.

**Deferral:** a P2 or P3 finding may be deferred with a beads ticket linked in the PR; P1 is not deferrable.

**CI today:** gofmt, `go vet`, `go test -race ./...`. staticcheck, gocognit and deadcode are not in CI; their absence is not a finding.

Gemini Code Assist is retired from this org, so `config.json` disables it.
