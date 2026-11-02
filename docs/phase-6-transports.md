# Phase 6: Transports and background work

**Goal:** the rest of the packages that real backends use, added on demand. Items are independent and can be built in any order.

## Status (2026-10-31)

| Item | Status | Where |
|---|---|---|
| Shared foundation | Done | `transport.go` (`NewEnhancers`, `Pipeline`, `Transport`, `ConnectMicroservice`), `Discover`, `ModuleRef.App` (D37 to D40) |
| `events` | Done | main module |
| `schedule` | Done | main module, own cron parser (D41) |
| `cqrs` | Done | main module, commands and queries (D42) |
| `cache` | Done | main module, memory store, interceptor, `Wrap` (D43) |
| `queue` | Done | memory driver in the main module, Redis in `queue/asynq` (D44) |
| `micro` + `micro/nats` | Done | `micro` (router, client, `NewLocal`), `micro/nats` module (D45) |
| `micro/grpc` | Done | `micro/grpc` module, status code mapping |
| `ws` | Done | `ws` module on coder/websocket; per-message request scope with `ws.PerMessageScope()` (D51) |
| `micro/redis` | Done | `micro/redis` module on go-redis, wildcard patterns (D47) |
| `micro/kafka` | Done | `micro/kafka` module on franz-go, regexp patterns (D48) |
| `graphql` | Done | `graphql` module on gqlgen, guards per root field, `KindGraphQL` (D49) |
| Durable providers | Done | `durable.go`: `Durable`, `WithContextIDStrategy`, `ContextID`, `WithDurableContext`, `ReleaseContext` (D46) |

Generators: `dygo g listener`, `g cron`, `g job`, `g gateway`, `g rpc`. Example: `examples/orders` (HTTP, cqrs, events, queue, schedule, cache and a micro handler in one app). Integration tests run against an embedded NATS server, franz-go's in-memory Kafka (`kfake`) and a throwaway `redis-server` (skipped when it is not installed) instead of Testcontainers, so `make test` needs no Docker. Guide: [guide-transports.md](guide-transports.md).

## Shared foundation
- `ExecutionContext.Kind()` gains `RPC` and `WS`; guards, interceptors, pipes, and filters work unchanged across transports.
- Handlers are registered by interface: a provider that implements `Patterns(r micro.Router)`, `Jobs(s schedule.Registry)`, or `Handlers(r events.Registry)` is found through discovery at boot (replaces `@MessagePattern`, `@Cron`, `@OnEvent` scanning).
- Hybrid apps: `app.ConnectMicroservice(transport)` runs HTTP and transports in one process with shared lifecycle.

## Items

| Package | What | Notes |
|---|---|---|
| `dygo/micro` + `micro/nats` | microservices NATS | Request/response messages and events, JSON payloads, a client provider, pre-request hook |
| `micro/grpc` | gRPC transport | Wraps generated gRPC services, maps `errs.HTTPError` to real gRPC status codes |
| `micro/kafka` | Kafka transport | Pattern matching with regexp |
| `micro/redis` | Redis transport | |
| `dygo/ws` | WebSocket gateways | `coder/websocket`, rooms, request scoped gateways, disconnect reason |
| `dygo/schedule` | scheduler | cron, interval, timeout, registry to list or stop jobs |
| `dygo/events` | event emitter | In process, sync or async, typed events via generics |
| `dygo/queue` | job queues | Adapter over River (Postgres) or asynq (Redis) |
| `dygo/cqrs` | CQRS buses | Command, query, event buses with typed handlers |
| `dygo/cache` | cache | Cache interceptor + `cache.Wrap[I]` for service interfaces |
| `dygo/graphql` | GraphQL | Integrates gqlgen resolvers with DI and guards |
| container | durable providers | Multi tenant child containers keyed by a context ID |

## Exit criteria (per item)
- An example and an integration test (Testcontainers where a broker is needed).
- Guards and filters work the same as in HTTP.
- A `dygo generate` kind that writes a working handler and registers it (for example `g job`, `g cron`, `g listener`, `g gateway`), so the CLI keeps up with each phase ([D33](decisions.md)).
