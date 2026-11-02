package dygotest_test

import (
	"testing"

	"github.com/dygoframework/dygo/dygotest"
)

func TestRequireEnv(t *testing.T) {
	t.Setenv("DYGO_TEST_EXAMPLE", "value")
	if got := dygotest.RequireEnv(t, "DYGO_TEST_EXAMPLE"); got != "value" {
		t.Fatalf("RequireEnv = %q", got)
	}
	t.Run("missing", func(t *testing.T) {
		dygotest.RequireEnv(t, "DYGO_TEST_NOT_SET")
		t.Fatal("not skipped")
	})
}

func TestRequireCommand(t *testing.T) {
	if got := dygotest.RequireCommand(t, "go"); got == "" {
		t.Fatal("RequireCommand returned no path")
	}
	t.Run("missing", func(t *testing.T) {
		dygotest.RequireCommand(t, "dygo-no-such-program")
		t.Fatal("not skipped")
	})
}

func TestIntegrationShort(t *testing.T) {
	dygotest.Integration(t)
	if testing.Short() {
		t.Fatal("not skipped with -short")
	}
}
