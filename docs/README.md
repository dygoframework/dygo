# Dygo Framework docs

Detailed plans for each phase of [PLAN.md](../PLAN.md). Each phase doc has the same shape: goal, scope, public API, internal design, tasks, tests, and the exit criteria that mark the phase as done.

| Phase | Doc | Status |
|---|---|---|
| 0 | [Foundation and skeleton](phase-0-foundation.md) | Done |
| 1 | [Core: container, modules, lifecycle](phase-1-core.md) | Done (first cut) |
| 2 | [HTTP layer](phase-2-http.md) | Done |
| 3 | [Pipeline and metadata](phase-3-pipeline.md) | Done |
| 4 | [Production essentials](phase-4-production.md) | Done |
| 5 | [CLI and codegen](phase-5-cli-codegen.md) | Done |
| 6 | [Transports and background work](phase-6-transports.md) | Done |
| 7 | [v1.0](phase-7-v1.md) | In progress |
| 8 | [Database: the `sql` module](phase-8-database.md) | In progress (milestone 1 done) |
| 9 | [Security: `dygo g security`](phase-9-security.md) | Done |

Other docs:

- [Getting started](getting-started.md), the entry point of the docs site (`make docs`, see `mkdocs.yml`).
- Recipes: [authentication](recipes/auth.md), [security](recipes/security.md), [SQL databases](recipes/sql.md), [testing](recipes/testing.md).
- [Decisions](decisions.md): the choices made so far and why, so later work stays consistent.
- [Core guide](guide-core.md): how to use what phase 1 shipped (modules, providers, lifecycle, testing).
- [HTTP guide](guide-http.md): controllers, typed handlers, errors, versioning, request scope, HTTP tests.
- [Pipeline guide](guide-pipeline.md): guards, interceptors, pipes, filters, metadata and validation.
- [Production guide](guide-production.md): config, logging, middleware, health, OpenAPI, rate limits, tracing, Docker.
- [CLI guide](guide-cli.md): dygo new, generate, routes, graph, check, start -watch, and route directives.
- [Transports guide](guide-transports.md): events, schedule, cqrs, cache, queue, microservices (NATS, gRPC) and WebSocket gateways.
- [Testing and benchmarks](guide-testing.md): unit vs integration tests, micro benchmarks, the comparison, and the make targets.

## Conventions

- Minimum Go version is **1.27**, the latest release (D52). The core has **no third party dependencies**.
- Module path: `github.com/dygoframework/dygo` (see [decisions](decisions.md)). License: MIT (D36).
- Root package `dygo` holds the core API. Optional features live in sub-packages.
- Every exported identifier has a doc comment. Every phase ends with `go vet ./...` and `go test -race ./...` passing.
