# Phase 0: Foundation and skeleton

**Goal:** a repository that builds, tests, and lints, with the layout every later phase fills in.

## Scope
- `go.mod` with Go 1.24 and the module path from [decisions D5](decisions.md).
- Directory layout (only folders with code exist yet; the rest is created in their phase):

```
dygo-framework/
  go.mod
  *.go                 root package dygo: container, modules, lifecycle, app
  dygotest/            testing helpers (OverrideProvider, Compile)
  examples/worker/     phase 1 acceptance app (no HTTP)
  docs/                these plans
  Makefile             test, vet, lint, cover
  .golangci.yml        lint config
  .github/workflows/ci.yml
```

- Testing package is named `dygotest`, not `testing`, so it never clashes with the standard library import.

## Tasks
1. `go mod init github.com/dygoframework/dygo`, `go 1.24`.
2. Makefile targets: `test` (`go test -race ./...`), `vet`, `lint` (golangci-lint), `cover`.
3. GitHub Actions workflow running vet, test, and lint on Go 1.24 and the latest stable Go.
4. `.golangci.yml` enabling `govet`, `staticcheck`, `errcheck`, `revive`, `gofumpt`.

## Exit criteria
- `make test vet` passes on an empty-but-compiling root package.
- CI file is present (it runs once the folder is pushed to GitHub).

## Status
Done on 2026-10-29, together with phase 1. The CI workflow is written but has not run, because the folder is not a GitHub repository yet.

Review on 2026-10-29: running the configured linters for the first time found 22 issues that CI would have failed on (gofumpt formatting, unchecked `Close` errors, missing doc comments on the `errs` constructors). All fixed; `make lint` reports 0 issues. golangci-lint is now pinned (v2.1.6) in both the Makefile (run through `go run`, so nothing needs installing) and CI, and the Makefile gained `fmt`, `bench`, `fuzz` and `example-http` targets.
