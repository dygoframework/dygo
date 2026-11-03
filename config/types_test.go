package config_test

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dygoframework/dygo/config"
	"github.com/dygoframework/dygo/validate"
)

type kinds struct {
	Addr    netip.Addr `env:"ADDR"`
	Workers uint8      `env:"WORKERS"`
	Ratio   float32    `env:"RATIO"`
	Ports   []int      `env:"PORTS"`
	Skip    string     `env:"-"`
	hidden  string     //nolint:unused // unexported fields are skipped
}

func TestLoadKinds(t *testing.T) {
	cfg, err := config.Load[kinds](config.Env(map[string]string{
		"ADDR": "10.0.0.1", "WORKERS": "4", "RATIO": "0.25", "PORTS": "80, 443", "-": "x",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr.String() != "10.0.0.1" || cfg.Workers != 4 || cfg.Ratio != 0.25 || len(cfg.Ports) != 2 || cfg.Ports[1] != 443 || cfg.Skip != "" {
		t.Fatalf("got %+v", cfg)
	}
}

func TestLoadKindErrors(t *testing.T) {
	type unsupported struct {
		M map[string]string `env:"M"`
	}
	type bad struct {
		Debug   bool    `env:"DEBUG"`
		Workers uint    `env:"WORKERS"`
		Ratio   float64 `env:"RATIO"`
		Ports   []int   `env:"PORTS"`
		Ptr     *int    `env:"PTR"`
	}
	_, err := config.Load[bad](config.Env(map[string]string{
		"DEBUG": "maybe", "WORKERS": "-1", "RATIO": "half", "PORTS": "80,x", "PTR": "y",
	}))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"not a bool", "not a non-negative integer", "not a number", `PORTS: "x"`, `PTR: "y"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
	if _, err := config.Load[unsupported](config.Env(map[string]string{"M": "a"})); err == nil || !strings.Contains(err.Error(), "unsupported type") {
		t.Errorf("map: err = %v", err)
	}
	if _, err := config.Load[int](); err == nil || !strings.Contains(err.Error(), "is not a struct") {
		t.Errorf("int: err = %v", err)
	}
}

type checked struct {
	Port int `env:"PORT"`
}

func (c *checked) Validate(context.Context) error {
	var v validate.Errors
	if c.Port == 0 {
		v.Add("env", "PORT", "is required")
	}
	return v.Err()
}

func TestValidate(t *testing.T) {
	if _, err := config.Load[checked](config.Env(nil), config.Validate(validate.Methods())); err == nil || !strings.Contains(err.Error(), "PORT") {
		t.Fatalf("err = %v", err)
	}
	cfg, err := config.Load[checked](config.Env(map[string]string{"PORT": "1"}), config.Validate(validate.Methods()))
	if err != nil || cfg.Port != 1 {
		t.Fatalf("got %+v, %v", cfg, err)
	}
}

func TestDotEnvErrors(t *testing.T) {
	dir := t.TempDir()
	broken := filepath.Join(dir, ".env")
	if err := os.WriteFile(broken, []byte("JUSTAKEY\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load[checked](config.Files(broken)); err == nil || !strings.Contains(err.Error(), "expected KEY=VALUE") {
		t.Errorf("broken line: err = %v", err)
	}
	// A directory can be opened but not read.
	if _, err := config.Load[checked](config.Files(dir)); err == nil {
		t.Error("directory: no error")
	}
	long := filepath.Join(dir, "long.env")
	if err := os.WriteFile(long, []byte("K="+strings.Repeat("x", 70*1024)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load[checked](config.Files(long)); err == nil {
		t.Error("too long line: no error")
	}
	unreadable := filepath.Join(dir, "nope.env")
	if err := os.WriteFile(unreadable, []byte("K=v\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	if f, err := os.Open(unreadable); err == nil {
		_ = f.Close() // running as root: the file can still be read
		return
	}
	if _, err := config.Load[checked](config.Files(unreadable)); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Errorf("unreadable: err = %v", err)
	}
}
