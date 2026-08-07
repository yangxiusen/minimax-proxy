# Repository Guidelines

## Project Structure & Module Organization

Executable entry points live in `cmd/server/` and `cmd/healthcheck/`. Business code is grouped by capability under `internal/`: `config` loads YAML, `httpapi/v2` implements the four public endpoints, `store/sqlite` owns persistence, `upstream/gradio` adapts the private API, and `scheduler`/`worker` execute tasks. Embedded schema files live in `migrations/`. Keep tests beside their packages as `*_test.go`.

`docs/` contains external reference material. It may inform development, but runtime code must never import, embed, read, or directly reference it. Product and engineering specifications live in `specs/`.

## Build, Test, and Development Commands

- `go test ./...`: run all unit and integration tests with temporary SQLite databases and local HTTP fakes.
- `go test -race ./...`: run concurrency checks where the host has CGO and a C compiler.
- `go vet ./...`: run Go static analysis.
- `go build ./cmd/server ./cmd/healthcheck`: compile both executables.
- `go run ./cmd/server -config config.yaml`: run locally.
- `docker build -t minimax-h3-tc:v0.0.1 .`: build the deployable image.

Format changed Go files with `gofmt -w <paths>` before testing.

## Coding Style & Naming Conventions

Use UTF-8 without BOM and standard `gofmt` formatting. Follow Go naming conventions: exported identifiers use `PascalCase`, local identifiers use `camelCase`, and package names are short lowercase nouns. Keep HTTP DTOs, domain models, and database records separate.

Write code comments in Chinese and only for non-obvious intent or constraints. Emit structured `slog` records in Chinese at proxy boundaries such as task claim, upstream submission, polling, recovery, and completion. Include correlation IDs and stable resource IDs, but never log API keys, prompts, media URLs, private addresses, or raw request bodies.

## Testing Guidelines

Use Go's `testing` and `httptest`; do not require live external services. Name tests after observable behavior, for example `TestClaimAndCancelHaveSingleWinner`. Cover success, validation, authentication, ownership isolation, timeouts, malformed upstream responses, idempotency, and state races. Every defect fix requires a regression test that fails before the fix.

## Commit & Pull Request Guidelines

Git history is unavailable in this checkout. Use concise Conventional Commits such as `feat: add V2 task query` or `fix: preserve queue cancellation atomicity`. Pull requests should describe behavior and compatibility changes, list verification commands, identify configuration or migration impact, and call out any real-upstream checks still awaiting manual confirmation.
