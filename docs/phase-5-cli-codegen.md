# Phase 5: CLI and optional codegen

**Goal:** a productive CLI, plus compile time wiring for teams that want zero runtime reflection.

## `cmd/dygo` CLI

| Command | What it does |
|---|---|
| `dygo new <name>` | Creates a project with app module, config, logger, health, Dockerfile (no Makefile since [D65](decisions.md)) |
| `dygo generate module users` (`g mo`) | Creates `users/module.go` and adds it to the parent module's `Imports` |
| `dygo g service users`, `g controller users` | Adds the constructor and registers it in the module |
| `dygo g resource users` | Module + service + controller + DTOs + tests for CRUD |
| `dygo start --watch` | Rebuild and restart on change |
| `dygo info` | Go version, dygo version, modules, routes |
| `dygo routes` | Prints the route table from the built app |
| `dygo graph` | Prints or renders the module and provider graph (DOT / Mermaid) |

Editing existing Go files (adding to `Imports`, `Providers`) uses `go/ast` + `go/format`, so user formatting is kept.

## `dygo gen`: compile time wiring

1. Load packages with `golang.org/x/tools/go/packages`.
2. Find module variables (`*dygo.Module` literals) starting from a root given by `//dygo:root` or a flag.
3. Evaluate them statically: constructor function references, option calls (`As`, `For`, `Group`, `WithScope`), imports and exports. Anything not statically evaluable (a module built in a loop) is an error with a hint to use runtime mode.
4. Run the **same graph validator** as runtime (shared package `internal/graph`), so errors are identical.
5. Emit `dygo_gen.go` with a function `func NewGeneratedApp(ctx) (*dygo.App, error)` that calls constructors directly in topological order and registers instances and hooks. Route tables are emitted too.
6. Mismatches between generated code and module definitions are compile errors, because generated code calls constructors with typed arguments.

Optional comment directives (`//dygo:get /users/{id}` above a method) are read by `dygo gen` to generate the `Routes` method. This is the closest Go gets to decorators and stays opt-in.

## Tasks
1. Extract graph validation into `internal/graph` usable without `reflect.Value` (types only).
2. CLI skeleton with `new` and `generate` templates.
3. `dygo gen` static evaluator and emitter.
4. Directive parser for routes.
5. Golden file tests for generated code.

## Exit criteria
- `examples/http-users` builds and passes the same tests in runtime mode and generated mode.
- `dygo new demo && cd demo && go test ./...` passes.

## Status (2026-10-30)

Built: the CLI in `cmd/dygo` (standard library only, in the main module), `dygo.Describe`, the inspection hook (`DYGO_INSPECT_OUT`, `dygo.ErrInspected`), `dygo check`, and `dygo gen` for route directives. User guide: [guide-cli.md](guide-cli.md).

| Command | State |
|---|---|
| `new` | Done. Simple one-package layout by default; `-t modular` for `main.go` and `app.module.go` at the top plus `src/{config,database,health,common,modules}` ([D32](decisions.md), [D62](decisions.md)); in a simple project `dygo g` writes flat files in package `main` ([D63](decisions.md)). `-module`, `-replace` (local checkout), `-skip-tidy`. Both layouts pass `go vet` and `go test`. |
| `generate module/service/controller/resource` | Done, with aliases `mo`, `s`, `co`, `res`, and `-dry-run`. Registration edits the nearest module literal by text insertion at AST positions plus gofmt ([D31](decisions.md)). |
| `generate entity/resource -fields` | Done (2026-10-31). Fields like `title:string price:float note:text? owner:email`; a terminal prompts for them when `-fields` is missing. Re-running `g entity` on an existing entity adds fields to the entity, the bodies, `Validate`, `Entity`, `Apply` and the tests' sample bodies ([D33](decisions.md)). `dygo g crud <Entity>` builds the module, service, controller and test around an existing entity, and asks for the entity when none is named. |
| `generate command` | Done (2026-10-31). A provider with `Name` and `Run`, run by `go run . <name>` through the new `console` package; new projects' `main.go` hands over the arguments ([D34](decisions.md)). |
| `generate test` | Done (2026-10-31). `routes_test.go` for a feature package from the routes the app registers: GET routes without parameters are checked, the rest are skipped stubs to fill in. |
| `generate auth` | Done (2026-10-31). Register, login (HS256 JWT), me, PBKDF2 password hashing, `Required()` and `Roles()` guards, `AUTH_SECRET` in `.env.example`. Standard library only ([D35](decisions.md)). |
| `start -watch` | Done. Polls file stats (no fsnotify dependency); swaps the process only after a successful build. |
| `info`, `routes`, `graph` | Done. `graph` prints text, Mermaid, DOT or JSON. |
| `check` | New: validates the wiring with the runtime validator without building anything ([D29](decisions.md)). |
| `gen` | Route directives only (`//dygo:controller`, `//dygo:get` and friends, `//dygo:use`, `//dygo:import`), plus `gen -check` for CI. The generated container (`NewGeneratedApp`) was not built; see [D30](decisions.md). |

Tests: golden files for every `generate` kind (including adding fields to an existing entity) and for `gen` (`go test ./cmd/dygo -update` rewrites them), unit tests for the module editor, and `TestNewProject`, which runs `dygo new`, `g resource -fields`, `g entity` (adding fields), `g service`, `g command`, `g auth`, `g test`, `go run . seed`, `go vet`, `go test`, `routes`, `check`, `graph` and a broken `check` against a fresh project.

### Exit criteria
- `dygo new demo && cd demo && go test ./...` passes: yes (`TestNewProject`, with `-replace` while dygo is unpublished).
- `examples/http-users` in runtime and generated mode: replaced by `dygo check` and `dygo gen -check` on both examples, in `make check` and CI, because there is no generated mode ([D30](decisions.md)).

### Differences from the plan
- No `internal/graph` extraction and no `go/packages`: `check` runs the real validator through the inspection hook, so messages are identical by construction and there is no second implementation to keep in sync.
- `dygo routes` is not limited to the built app: it runs the app's main package up to `dygo.Create`.
