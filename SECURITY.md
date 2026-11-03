# Security policy

## Supported versions

| Version | Supported |
|---------|-----------|
| 1.x (latest minor) | Yes |
| 0.x (pre-release) | No, please upgrade |

Fixes go into the latest minor release of the current major version. The core
module and every sub-module (`ws`, `graphql`, `micro/*`, `queue/asynq`,
`observe`, `validate/playground`) follow the same policy.

## Reporting a vulnerability

Please do not open a public issue. Report it privately through GitHub:
**Security > Report a vulnerability** on
[github.com/dygoframework/dygo](https://github.com/dygoframework/dygo/security/advisories/new).

Include the affected version, a minimal reproduction, and the impact you
expect. You can expect:

- an acknowledgement within 3 working days,
- a first assessment (accepted or not, and severity) within 10 working days,
- a fix and a GitHub security advisory, with credit if you want it, once a
  patched release is out. We aim for 30 days for high and critical issues.

## Scope

In scope: the code in this repository, including request binding, the error
writer, the WebSocket and GraphQL transports, rate limiting, and the code the
`dygo` CLI generates (for example the `dygo g auth` templates).

Out of scope: vulnerabilities in third party dependencies that Dygo does not
make reachable (report those upstream), and issues that need a misconfigured
application, such as disabling body size limits or exposing `/docs` in
production on purpose.

## What the project does

- The "Security (OWASP Top 10)" CI job checks every change against the OWASP
  Top 10: gosec, govulncheck, the security tests and fuzzing (`make owasp`).
  See `docs/owasp.md` for what each category checks.
- `govulncheck` runs on every module in CI (`make vulncheck`).
- The request binder is fuzzed (`make fuzz`), covering path, query, header,
  cookie, JSON, url-encoded and multipart input.
- The core module uses only the Go standard library, which keeps the
  dependency surface of a plain HTTP service small.
- 5xx responses never include the underlying error; it is logged instead
  (see `errs.DefaultWriter`).
