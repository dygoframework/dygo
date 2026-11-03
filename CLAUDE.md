# Working on Dygo

Dygo is a modular Go framework (modules, dependency injection, an HTTP
pipeline) plus the `dygo` CLI in `cmd/dygo`. The core uses only the standard
library. Keep things simple.

## Tests for new code (required)

Every change must come with tests. The Go code you add or change must be
**more than 80% covered** by tests. Old code you did not touch does not count.

- Write the tests in the same change, next to the code (`x.go` gets `x_test.go`).
- Check the coverage of the packages you touched:

  ```sh
  go test -coverprofile=/tmp/c.out ./path/to/pkg/
  go tool cover -func=/tmp/c.out   # the functions you added or changed: over 80%
  ```

  For a sub-module (its own `go.mod`, listed in `SUBMODULES` in the Makefile),
  run the same commands inside its folder.
- Code that only an integration test reaches (a database, Redis, a built
  project) counts too: write the test with `dygotest.Integration` or
  `dygotest.RequireEnv`, see `docs/guide-testing.md`.
- Put the coverage of the new code in the pull request description.
- CLI templates (`cmd/dygo/templates`) are covered by the golden tests: after
  changing one, run `go test ./cmd/dygo/ -run <Test> -update` and review the
  diff.

## Before a pull request

```sh
make vet
make test-unit        # fast tests
make test             # everything; integration tests skip without their services
```

- Add a line to `CHANGELOG.md` under "Unreleased" for anything a user notices.
- Pull requests go to `main`; never push to `main` directly.

## Writing rules

- Docs, comments, CLI text and templates describe what Dygo does on its own:
  don't compare it with other frameworks or name them.
- Don't leave build outputs (`dygo`, `*.test`, `coverage.out`) in the repo.
