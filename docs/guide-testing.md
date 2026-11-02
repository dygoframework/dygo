# Testing and benchmarks

This page is about testing Dygo itself: where tests and benchmarks live, how
they are split, and the `make` targets that run them. The helpers it uses,
`dygotest.Integration`, `RequireEnv` and `RequireCommand`, work the same way in
an app built with Dygo.

## Where things live

Tests sit next to the code they test, as Go expects. Nothing lives in a separate `tests/` tree.

| What | Where |
|---|---|
| Unit and integration tests | `*_test.go` next to the code |
| Micro benchmarks | `bench_test.go` next to the code |
| Shared test data | `testdata/` in the package (CLI golden files: `cmd/dygo/testdata/golden`) |
| Test helpers for apps | `dygotest/` (`dygotest.New`, the HTTP helpers, the integration helpers) |
| Test helpers inside one module | an `internal/` package, such as `sql/internal/suite` |
| Comparison with other frameworks | the `benchmarks/` module |
| Saved benchmark results | `benchmarks/results/` |
| Services for integration tests | `compose.test.yml` |

## Two levels of tests

**Unit tests** are fast and need nothing outside the process. Most tests are
unit tests. They run with `go test -short`.

**Integration tests** need something from outside: a database, a
`redis-server`, or a `go build` of a project the CLI generated. Each one starts
with a helper from `dygotest`, so it skips itself under `-short` and when what
it needs is missing:

```go
func TestPostgres(t *testing.T) {
	url := dygotest.RequireEnv(t, "DYGO_TEST_POSTGRES_URL") // skips if unset
	// ...
}

func TestServiceOverRedis(t *testing.T) {
	bin := dygotest.RequireCommand(t, "redis-server") // skips if not on PATH
	// ...
}

func TestNewProject(t *testing.T) {
	dygotest.Integration(t) // builds real programs: skipped with -short
	// ...
}
```

Integration tests are plain tests, not hidden behind build tags, so `go vet`,
the linter and your editor always see them.

The environment variables are:

| Variable | Used by | Value from `compose.test.yml` |
|---|---|---|
| `DYGO_TEST_POSTGRES_URL` | `sql/postgres`, `sql/migrate`, `cmd/dygo` | `postgres://postgres:dygo@localhost:5432/dygo?sslmode=disable` |
| `DYGO_TEST_MYSQL_URL` | `sql/mysql`, `sql/migrate`, `cmd/dygo` | `mysql://root:dygo@127.0.0.1:3306/dygo` |
| `DYGO_TEST_REDIS_ADDR` | `queue/asynq`, `micro/redis` | `127.0.0.1:6379` (without it, the tests start a `redis-server` if one is installed) |

## Running tests

```sh
make test-unit         # unit tests only, every module: fast, no services
make test              # everything, skipping integration tests whose services are missing
make test-integration  # starts compose.test.yml, then everything, nothing skipped
make services-down     # stops the services and drops their data
```

To run one package or one test, use `go test` directly:

```sh
go test -short ./cache
go test . -run TestDurableProvidersPerTenant
cd sql && DYGO_TEST_POSTGRES_URL=... go test ./postgres
go test ./cmd/dygo -update   # rewrite the CLI golden files after a template change
```

CI runs `make test-unit` on two Go versions, and `make test` with Postgres,
MySQL and Redis on two database versions.

## Two kinds of performance tests

**Micro benchmarks** measure one piece of Dygo: the pipeline, the cache
interceptor, a WebSocket broadcast. They live in a `bench_test.go` file next to
the code. Add one when you change a hot path, and use `b.Loop()` and
`b.ReportAllocs()`.

**The comparison** in `benchmarks/` runs the same four requests on Dygo, bare
`net/http`, chi, Gin and Huma. It is its own Go module, so those frameworks
never become dependencies of Dygo. See the [benchmarks README](https://github.com/dygoframework/dygo/blob/main/benchmarks/README.md).

## Running benchmarks

```sh
make bench                    # every micro benchmark in every module, once
make bench BENCH=Pipeline     # only the ones matching a regexp
make bench-save               # 10 runs, saved to benchmarks/results/dygo-<date>-<go>.txt
make bench-diff               # 10 runs now, compared with the newest saved file (benchstat)
make bench-compare            # Dygo vs the other frameworks, saved as compare-<date>-<go>.txt
```

The usual way to check a change for speed:

1. On `main`, run `make bench-save` (or keep the file saved last time).
2. On your branch, run `make bench-diff` and read the `vs base` column.
3. If the change is meant to move the numbers, commit a new `make bench-save`
   result with it.

Benchmark numbers only compare well on the same machine, so compare runs from
one machine, and close other programs while they run.

CI runs `make bench-smoke`: every benchmark once, so a broken benchmark fails
the build. It does not check speed.
