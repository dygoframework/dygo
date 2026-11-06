GOLANGCI := go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0

.PHONY: test test-unit test-integration services-up services-down vet lint fmt cover bench bench-save bench-diff bench-smoke bench-compare fuzz vulncheck gosec test-owasp owasp docs example example-http cli check

# These are their own Go modules (they have third party dependencies, or use
# one that does), so ./... at the root does not reach them.
SUBMODULES := validate/playground observe examples/http-users examples/academy queue/asynq micro/nats micro/grpc micro/redis micro/kafka ws graphql sql ai

# Tests come in two levels (docs/guide-testing.md):
#   unit         fast, nothing outside the process: go test -short
#   integration  needs a database, redis-server or a go build of a generated
#                project; starts with dygotest.Integration, RequireEnv or
#                RequireCommand, so -short and missing services skip it.
# test runs both, skipping integration tests whose services are missing.
test:
	go test -race ./...
	for m in $(SUBMODULES); do (cd $$m && go test -race ./...) || exit 1; done

test-unit:
	go test -race -short ./...
	for m in $(SUBMODULES); do (cd $$m && go test -race -short ./...) || exit 1; done

# The databases and Redis the CI uses, from compose.test.yml.
TEST_ENV ?= DYGO_TEST_POSTGRES_URL='postgres://postgres:dygo@localhost:5432/dygo?sslmode=disable' \
	DYGO_TEST_MYSQL_URL='mysql://root:dygo@127.0.0.1:3306/dygo' \
	DYGO_TEST_REDIS_ADDR=127.0.0.1:6379

# Everything, with the services up so no integration test is skipped.
test-integration: services-up
	$(TEST_ENV) go test -race -count=1 ./...
	for m in $(SUBMODULES); do (cd $$m && $(TEST_ENV) go test -race -count=1 ./...) || exit 1; done

services-up:
	docker compose -f compose.test.yml up -d --wait

services-down:
	docker compose -f compose.test.yml down -v

vet:
	go vet ./...
	for m in $(SUBMODULES); do (cd $$m && go vet ./...) || exit 1; done

lint:
	$(GOLANGCI) run ./...
	for m in $(SUBMODULES); do (cd $$m && $(GOLANGCI) run --config $(CURDIR)/.golangci.yml ./...) || exit 1; done

fmt:
	$(GOLANGCI) fmt ./...

# The same tests as make test, with coverage. Every module's profile is merged
# into coverage.out, with file paths from the repo root (what Coveralls reads).
# Left out of the numbers: the examples, generated code (the gqlgen test
# schema) and helpers only tests use (the shared database test suite).
MODULE := github.com/dygoframework/dygo
cover:
	rm -rf .cover && mkdir .cover
	go test -race -covermode=atomic -coverprofile=.cover/root.out ./...
	for m in $(SUBMODULES); do (cd $$m && go test -race -covermode=atomic -coverprofile=$(CURDIR)/.cover/$$(echo $$m | tr / -).out ./...) || exit 1; done
	echo "mode: atomic" > coverage.out
	tail -q -n +2 .cover/*.out | sed 's|^$(MODULE)/||' | grep -v -e '^examples/' -e '^graphql/internal/testgraph/' -e '^sql/internal/suite/' >> coverage.out
	@awk 'NR > 1 { all += $$2; if ($$3 > 0) hit += $$2 } END { printf "coverage: %.1f%% of statements\n", 100 * hit / all }' coverage.out

# Performance tests come in two kinds (docs/guide-testing.md):
#   micro benchmarks  bench_test.go next to the code, in every module
#   comparison        the benchmarks/ module: Dygo vs net/http, chi, Gin, Huma
# Results go to benchmarks/results: dygo-<date>-<go>.txt for the micro
# benchmarks, compare-<date>-<go>.txt for the comparison.
# BENCH picks benchmarks by regexp, e.g. make bench BENCH=Pipeline.
BENCH ?= .
BENCHTIME ?= 1s
COUNT ?= 1
BENCHFLAGS = -run '^$$' -bench '$(BENCH)' -benchmem -benchtime $(BENCHTIME) -count $(COUNT)
RESULTS := benchmarks/results
STAMP = $(shell date +%Y-%m-%d)-$(shell go env GOVERSION)
BENCHSTAT := go run golang.org/x/perf/cmd/benchstat@v0.0.0-20261009192801-be2c69fb417e

bench:
	go test $(BENCHFLAGS) ./...
	for m in $(SUBMODULES); do (cd $$m && go test $(BENCHFLAGS) ./...) || exit 1; done

# Ten runs saved as the new baseline. Commit the file when a change is meant
# to move the numbers.
bench-save:
	$(MAKE) -s bench COUNT=10 > $(RESULTS)/dygo-$(STAMP).txt
	@echo "saved $(RESULTS)/dygo-$(STAMP).txt"

# Ten runs now, compared with the newest saved baseline.
bench-diff:
	@base=$$(ls $(RESULTS)/dygo-*.txt 2>/dev/null | tail -1); \
	if [ -z "$$base" ]; then echo "no baseline yet: run make bench-save first"; exit 1; fi; \
	$(MAKE) -s bench COUNT=10 > .bench-new.txt && \
	$(BENCHSTAT) $$base .bench-new.txt

# Every benchmark once, to check they still build and run (CI); not timed.
bench-smoke:
	$(MAKE) -s bench BENCHTIME=1x

# Dygo vs net/http, chi, Gin and Huma; see benchmarks/README.md. Its own
# module, so it is not part of test, vet or bench.
bench-compare:
	cd benchmarks && go test ./... && go test -run '^$$' -bench . -benchmem -count 10 -cpu 1 . > results/compare-$(STAMP).txt
	@echo "saved $(RESULTS)/compare-$(STAMP).txt"

FUZZTIME ?= 30s
# govulncheck reports standard library issues for the Go that runs it, so run
# it with a supported release (it needs Go 1.26+ to build). VULNDB overrides
# the database, e.g. VULNDB=https://storage.googleapis.com/go-vulndb.
VULNDB ?=
GOVULNCHECK := go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 $(if $(VULNDB),-db $(VULNDB))

# go test -fuzz takes one target at a time.
fuzz:
	for f in $$(go test -list '^Fuzz' ./internal/binder | grep ^Fuzz); do \
		go test -run xxx -fuzz "^$$f\$$" -fuzztime $(FUZZTIME) ./internal/binder || exit 1; done
	cd sql && for f in $$(go test -list '^Fuzz' . | grep ^Fuzz); do \
		go test -run xxx -fuzz "^$$f\$$" -fuzztime $(FUZZTIME) . || exit 1; done

vulncheck:
	$(GOVULNCHECK) ./...
	for m in $(SUBMODULES); do (cd $$m && $(GOVULNCHECK) ./...) || exit 1; done

# OWASP Top 10 checks, run by the owasp CI job; docs/owasp.md maps each
# category to what checks it.
GOSEC := $(GOLANGCI) run --enable-only gosec

gosec:
	$(GOSEC) ./...
	for m in $(SUBMODULES); do (cd $$m && $(GOSEC) --config $(CURDIR)/.golangci.yml ./...) || exit 1; done

# The tests that docs/owasp.md lists: access rules, tokens, login, logging,
# injection, error answers, headers and limits. TestNewSecurityProject builds
# dygo new -security projects (plain and OIDC) and runs their own tests.
test-owasp:
	go test -race -count=1 ./middleware ./ratelimit ./internal/binder ./errs
	go test -race -count=1 -run 'TestHTTP(BindErrorsAndLimits|NotFoundAndPanic)' .
	go test -count=1 -run 'TestGenerateSecurity|TestNewSecurityProject' ./cmd/dygo
	cd examples/academy && go test -race -count=1 ./internal/modules/...
	cd ai && go test -race -count=1 -run 'PublicMessage|SSEErrorIsPublic|NeverLogsPrompts|MessageIsCapped|CollectStopsAtLimit|RetryHonoursRetryAfter|SecretHidden|ConfigValidate|ModuleFromEnv|TestClient|DecodeJSON|EventsLimits' ./...
	cd sql && go test -race -count=1 -run 'OrderByChecksColumns|MemoryRepository|TagErrors|MapError|WithoutLeakingPassword' ./...

owasp: gosec vulncheck test-owasp fuzz

# The docs site (mkdocs.yml). Root files that are also site pages are copied
# into docs/ first; the copies are git-ignored.
docs:
	cp CHANGELOG.md docs/changelog.md
	cp SECURITY.md docs/security.md
	cp benchmarks/README.md docs/benchmarks.md
	mkdocs build --strict

example:
	go run ./examples/worker

example-http:
	cd examples/http-users && go run .

cli:
	go install ./cmd/dygo

# Validate the examples' wiring without starting them (dygo check).
check:
	go build -o $(CURDIR)/.bin/dygo ./cmd/dygo
	cd examples/http-users && $(CURDIR)/.bin/dygo check && $(CURDIR)/.bin/dygo gen -check
	cd examples/worker && $(CURDIR)/.bin/dygo check
	cd examples/orders && $(CURDIR)/.bin/dygo check
	cd examples/academy && $(CURDIR)/.bin/dygo check
