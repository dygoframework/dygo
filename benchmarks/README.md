# Benchmarks: Dygo vs net/http, chi, Gin and Huma

This module compares Dygo with bare `net/http` and three popular Go
frameworks on the same four requests. It is its own Go module, so chi, Gin and
Huma never become dependencies of Dygo.

```sh
make bench-compare    # checks every server answers the same, then saves
                      # benchmarks/results/compare-<date>-<go>.txt
go run golang.org/x/perf/cmd/benchstat@latest benchmarks/results/compare-*.txt
```

Dygo's own micro benchmarks (`bench_test.go` files next to the code) are a
separate thing: `make bench`, `make bench-diff`, see the
[testing guide](https://github.com/dygoframework/dygo/blob/main/docs/guide-testing.md).

## Scenarios

| Name | Request | Work |
|---|---|---|
| static | `GET /ping` | write `pong` |
| param | `GET /users/42` | parse an int path param, write a JSON object |
| json | `POST /users/42?limit=5` with `{"name":"bench"}` | path int, query int with default, JSON body in, JSON object out, 201 |
| routing | `GET /r99/items/7` | the last of 100 param routes, write the param |

`TestSameResponses` checks that every implementation returns the same status
and body for each scenario, so the numbers compare the same work.

## How each framework is used

Each one is used the way its docs recommend for a typical API, with no tuning:

- **net/http**: Go 1.22+ `ServeMux` patterns, hand written `strconv` parsing,
  `json.NewDecoder` and `json.NewEncoder`. This is the floor.
- **chi** v5.4.0: chi router, the same hand written handlers.
- **Gin** v1.12.0: `gin.New()` (no logger or recovery middleware),
  `ShouldBindUri`, `ShouldBindQuery`, `ShouldBindJSON`, `c.JSON`.
- **Huma** v2.39.1: typed handlers on the `humago` (net/http) adapter, with its
  default config, so OpenAPI generation and validation of the input schema
  are on, as in a real Huma app.
- **Dygo**: typed handlers (`dygo.Handle`) in a module, created with
  `dygo.Create` and no options, so the error writer, panic recovery and body
  limit are on, as in a real Dygo app.

## Method

- Handlers are called in process through `ServeHTTP`, with no network, so the
  numbers are the framework's cost: routing, binding, encoding and its own
  bookkeeping.
- The request is built once per benchmark; only its body is reset each
  iteration. The response writer discards the body. Without this,
  `httptest.NewRequest` and `ResponseRecorder` are about 3 µs and 5 KB per
  request and hide the differences.
- `-cpu 1 -count 10 -benchtime 500ms`, summarized with benchstat (median,
  95% confidence interval).
- Machine: 4 vCPU Intel Xeon @ 2.10GHz cloud VM, Linux amd64, Go 1.27.3.
  Absolute numbers depend on the machine; compare the ratios. Raw output is
  [benchmarks/results/compare-2026-10-31-go1.27.3.txt](https://github.com/dygoframework/dygo/blob/main/benchmarks/results/compare-2026-10-31-go1.27.3.txt).

## Results (2026-10-31)

Time per request (lower is better):

| Scenario | net/http | chi | Gin | Huma | Dygo |
|---|---|---|---|---|---|
| static | 149 ns | 547 ns | 236 ns | 610 ns | 679 ns |
| param | 829 ns | 1.55 µs | 1.88 µs | 2.18 µs | 1.29 µs |
| json | 2.49 µs | 3.23 µs | 4.63 µs | 5.39 µs | **2.51 µs** |
| routing (100 routes) | 287 ns | 933 ns | 279 ns | 994 ns | 911 ns |

Allocations per request (bytes / count):

| Scenario | net/http | chi | Gin | Huma | Dygo |
|---|---|---|---|---|---|
| static | 8 B / 1 | 376 B / 3 | 48 B / 1 | 152 B / 6 | 328 B / 6 |
| param | 96 B / 4 | 784 B / 7 | 544 B / 10 | 384 B / 11 | 392 B / 8 |
| json | 1032 B / 13 | 1720 B / 16 | 1480 B / 21 | 856 B / 20 | 832 B / 11 |
| routing | 24 B / 2 | 712 B / 5 | 48 B / 1 | 176 B / 8 | 352 B / 8 |

## What the numbers say

- **Typed JSON endpoints, the common case, cost the same as hand written
  net/http.** The json scenario is within 1% of the floor, faster than chi
  with hand written handlers, and about 2x faster than Gin and Huma, with the
  fewest allocations of all five. The binder plan is compiled once per input
  type at boot, so a request runs a flat list of setters.
- **Dygo has a fixed cost of about 0.5 µs and 5 allocations per request.**
  It shows in the static and routing scenarios, where there is almost no work
  to hide it: there Dygo is close to chi and Huma, and about 2 to 4x slower
  than ServeMux and Gin. This is the per-request state (execution context,
  response tracking, request context values) that the pipeline needs. It is
  the first place to look for later performance work.
- Routing is ServeMux underneath, so it scales like ServeMux: hitting the last
  of 100 routes costs Dygo about the same as the static route.
- For scale: a single small database query is typically 100 µs or more, so
  every framework here is far from the bottleneck of a real API.
