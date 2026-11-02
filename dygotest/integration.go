package dygotest

import (
	"os"
	"os/exec"
	"testing"
)

// Dygo has two levels of tests:
//
//   - Unit tests are fast and need nothing outside the process. They run with
//     go test -short (make test-unit).
//   - Integration tests need something from outside: a database, a
//     redis-server, or a go build of a generated project. They start with one
//     of the helpers below, so go test -short skips them, and so do runs where
//     what they need is missing (make test-integration provides it).

// Integration marks t as an integration test: it is skipped under go test
// -short.
func Integration(t testing.TB) {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test; skipped with -short")
	}
}

// RequireEnv marks t as an integration test and returns the environment
// variable name, such as DYGO_TEST_POSTGRES_URL. The test is skipped when the
// variable is not set.
func RequireEnv(t testing.TB, name string) string {
	t.Helper()
	Integration(t)
	v := os.Getenv(name)
	if v == "" {
		t.Skipf("%s not set; skipping integration test", name)
	}
	return v
}

// RequireCommand marks t as an integration test and returns the path of the
// program name, such as redis-server. The test is skipped when the program is
// not on PATH.
func RequireCommand(t testing.TB, name string) string {
	t.Helper()
	Integration(t)
	p, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s not installed; skipping integration test", name)
	}
	return p
}
