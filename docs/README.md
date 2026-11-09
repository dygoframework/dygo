# Dygo docs

Start with [Getting started](getting-started.md): a tested JSON API in about
ten minutes. The same pages make up the docs site (`make docs`).

## Guides

- [Core](guide-core.md): modules, providers, lifecycle and testing.
- [HTTP](guide-http.md): controllers, typed handlers, errors, versioning,
  request scope and HTTP tests.
- [Pipeline](guide-pipeline.md): guards, interceptors, pipes, filters,
  metadata and validation.
- [Production](guide-production.md): config, logging, middleware, health,
  OpenAPI, rate limits, tracing and Docker.
- [CLI](guide-cli.md): `dygo new`, generators, `dygo db`, routes, graph,
  check and `start -watch`.
- [Transports](guide-transports.md): events, schedule, CQRS, cache, queues,
  microservices (NATS, gRPC, Kafka, Redis) and WebSocket gateways.
- [AI](guide-ai.md): call a model, stream the answer to the browser, typed
  output, errors and tests with a fake model.
- [Testing and benchmarks](guide-testing.md): unit and integration tests,
  micro benchmarks and the framework comparison.

## Recipes

- [Authentication](recipes/auth.md)
- [Security](recipes/security.md)
- [SQL databases](recipes/sql.md)
- [Testing](recipes/testing.md)

## Security

- [OWASP Top 10](owasp.md): what Dygo checks on every change.
- [Reporting a vulnerability](../SECURITY.md)
