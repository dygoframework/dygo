# OWASP Top 10

Every pull request runs the "Security (OWASP Top 10)" CI job (`make owasp`), which fails when a
change breaks one of the checks below. It has four steps:

| Step | Make target | What it does |
|---|---|---|
| Static analysis | `make gosec` | [gosec](https://github.com/securego/gosec) through golangci-lint on every module: SQL built with string formatting, weak crypto and random numbers, unsafe file paths and commands, integer overflows, insecure cookies. `make lint` runs it too. |
| Known vulnerabilities | `make vulncheck` | `govulncheck` on every module, including the standard library of the Go that runs it. |
| Security tests | `make test-owasp` | The tests listed below, including the tests of a fresh `dygo new -security` project, plain and OIDC. |
| Fuzzing | `make fuzz` | The request binder (path, query, header, cookie, JSON, form, multipart) and the SQL placeholder compiler. |

Dependabot (`.github/dependabot.yml`) opens a weekly PR for Go modules and
CI actions, and every action in the workflows is pinned to a commit.

Run `make owasp` locally before a release; it needs network access for the
tools and the vulnerability database.

## What each category checks

The tests named `security_test.go` are the ones `dygo g security` generates;
`examples/academy/internal/modules/security` runs the same tests.

| Category | What protects an app | Checked by |
|---|---|---|
| A01 Broken access control | Deny by default, ordered path rules, `Roles`, `Authorities`, `Require`, `RequireOwner`; generated CRUD needs a role to write | `TestDenyByDefault`, `TestPathRules`, `TestRolesAndAuthorities`, `TestRequireAndOwner`; generated `*.controller_test.go` check 401 and 403; `TestGenerateSecurityGolden` |
| A02 Cryptographic failures | HS256 with a secret of 32+ characters (required in production); RS256/ES256 from the provider's keys; PBKDF2 passwords with rehash; refresh tokens stored hashed; the `ai` providers use TLS 1.2 or newer and refuse a plain http `AI_BASE_URL` unless it is this machine | `TestForgedTokens`, `TestProviderTokens`, `TestRehash`; gosec, `TestClient`, `TestConfigValidate` |
| A03 Injection | `?` placeholders; `OrderBy` and `Patch` only accept known columns; strict JSON (unknown fields and trailing data refused) | `TestRepositoryOrderByChecksColumns`, `TestMemoryRepository`, `TestStrictJSONAndContentType`; `FuzzCompile`, `FuzzBind*`; gosec (G201, G202) |
| A04 Insecure design | Limits by default: body size, login and refresh rate limits, refresh reuse ends the login; `ai` caps a collected answer at 8 MiB and retries at 5, never waiting more than 30 s; providers cap error bodies at 64 KiB, answers at 9 MiB and stream lines at 1 MiB | `TestBodyLimit`, `TestHTTPBindErrorsAndLimits`, `TestRateLimit`, `TestRefreshReuseRevokesLogin`, `TestRefreshRace`, `TestCollectStopsAtLimit`, `TestRetryHonoursRetryAfter`, `TestDecodeJSON`, `TestEventsLimits` |
| A05 Security misconfiguration | `SecureHeaders` and `CORS` middleware (`dygo new` and `dygo g security` add them); a 500 never shows the error; `AUTH_SECRET` and https issuer required in production; `AI_API_KEY` prints, logs and encodes as `***`, and `AI_BASE_URL` may not hold a user or password | `TestSecureHeaders`, `TestCORS`, `TestCSRF`, `TestHTTPNotFoundAndPanic`, `TestGenerateSecurityGolden` (app middleware), `TestSecretHidden`, `TestModuleFromEnv` |
| A06 Vulnerable components | Few dependencies; the core uses only the standard library | `make vulncheck`; Dependabot |
| A07 Identification and authentication failures | Same answer and time for unknown email and wrong password; tokens only from the `Authorization` header; refresh rotation and logout | `TestRegisterLoginMe`, `TestForgedTokens`, `TestRefreshAndLogout`, `TestDenyByDefault` |
| A08 Software and data integrity failures | Signed tokens with a fixed algorithm (`alg: none` and algorithm swaps refused); actions pinned to commits; `go.sum` | `TestForgedTokens`, `TestProviderTokens`; Dependabot |
| A09 Security logging and monitoring failures | Denied requests, bad tokens, failed logins and reused refresh tokens are logged; passwords and tokens never are; `ai.Log` never logs prompts or answers, and clients only see `ai.PublicMessage`, never the provider's error | `TestSecurityLog`; `TestOpenFailsWithoutLeakingPassword`; `TestLogNeverLogsPrompts`, `TestSSEErrorIsPublic`, `TestPublicMessageHidesDetails` |
| A10 Server-side request forgery | The app only calls the configured `OAUTH_ISSUER_URL` (https in production) and the `jwks_uri` it names; unknown key IDs fetch the keys at most once a minute; the `ai` providers only call `AI_BASE_URL` (or the vendor's URL) and never follow a redirect, which would carry the API key to another host | `TestUnknownKeyFetchLimit`, `TestKeyRotation`; gosec, `TestClient` |

## What stays with the app

These checks cover the framework and the code it generates. An app still
has to:

- clean any user input it passes to `dygo.File` or `dygo.RedirectTo`;
- build SQL with placeholders, never by joining strings, in its own queries;
- set `AUTH_SECRET`, `CORS_ALLOWED_ORIGINS` and HTTPS in production;
- send its logs somewhere they are watched.

The [Security recipe](recipes/security.md) explains the generated security
package, and [SECURITY.md](security.md) how to report a vulnerability.
