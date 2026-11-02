package logger_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/dygoframework/dygo/logger"
)

func TestNew(t *testing.T) {
	var buf bytes.Buffer
	t.Setenv("APP_ENV", "production")
	t.Setenv("LOG_LEVEL", "warn")
	log := logger.New(logger.Options{Output: &buf})
	log.Info("hidden")
	log.Warn("shown", "k", 1)
	if out := buf.String(); strings.Contains(out, "hidden") || !strings.HasPrefix(out, `{"time"`) || !strings.Contains(out, `"k":1`) {
		t.Fatal(out)
	}
	buf.Reset()
	logger.New(logger.Options{Output: &buf, Format: logger.Text, Level: slog.LevelDebug}).Debug("x")
	if !strings.Contains(buf.String(), "level=DEBUG msg=x") {
		t.Fatal(buf.String())
	}
	if logger.ParseLevel("ERROR") != slog.LevelError || logger.ParseLevel("nope") != slog.LevelInfo {
		t.Fatal("ParseLevel")
	}
}
