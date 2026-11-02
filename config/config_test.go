package config_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/config"
)

type Redis struct {
	Addr string `env:"ADDR" default:"localhost:6379"`
	DB   int    `env:"DB"`
}

type Config struct {
	Port    int           `env:"PORT" default:"9900"`
	DBURL   string        `env:"DB_URL" required:"true"`
	Timeout time.Duration `env:"TIMEOUT" default:"5s"`
	Origins []string      `env:"ORIGINS"`
	Debug   bool          `env:"DEBUG"`
	Ratio   *float64      `env:"RATIO"`
	Redis   Redis         `envPrefix:"REDIS_"`
	Ignored string
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	env := filepath.Join(dir, ".env")
	local := filepath.Join(dir, ".env.local")
	if err := os.WriteFile(env, []byte("# comment\nexport DB_URL=postgres://file\nPORT=8080 # trailing\nORIGINS=a.com, b.com\nREDIS_DB='2'\nMSG=\"a\\nb\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, []byte("PORT=9090\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load[Config](config.Files(local, env, filepath.Join(dir, "missing")),
		config.Env(map[string]string{"DEBUG": "true", "RATIO": "0.5", "DB_URL": "postgres://env"}))
	if err != nil {
		t.Fatal(err)
	}
	ratio := 0.5
	want := Config{
		Port: 9090, DBURL: "postgres://env", Timeout: 5 * time.Second, Origins: []string{"a.com", "b.com"},
		Debug: true, Ratio: &ratio, Redis: Redis{Addr: "localhost:6379", DB: 2},
	}
	if !reflect.DeepEqual(*cfg, want) {
		t.Fatalf("got %+v\nwant %+v", *cfg, want)
	}
}

func TestLoadErrors(t *testing.T) {
	_, err := config.Load[Config](config.Env(map[string]string{"PORT": "x", "TIMEOUT": "soon"}))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"DB_URL is required", `PORT: "x" is not an integer`, `TIMEOUT: "soon" is not a duration`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
}

func TestPrefixAndModule(t *testing.T) {
	type Users struct {
		Limit int `env:"LIMIT" default:"10"`
	}
	t.Setenv("USERS_LIMIT", "25")
	type needs struct{ cfg *Users }
	var got *Users
	app, err := dygo.CreateApplicationContext(context.Background(), &dygo.Module{
		Name:    "app",
		Imports: []*dygo.Module{config.ForFeature[Users](config.Prefix("USERS_"))},
		Providers: []dygo.Provider{dygo.Provide(func(u *Users) *needs {
			got = u
			return &needs{u}
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Close(context.Background()) }()
	if got.Limit != 25 {
		t.Fatalf("limit %d", got.Limit)
	}

	_, err = dygo.CreateApplicationContext(context.Background(), &dygo.Module{
		Name: "bad", Imports: []*dygo.Module{config.ForRoot[Config](config.Env(nil))},
	})
	if err == nil || !strings.Contains(err.Error(), "DB_URL is required") {
		t.Fatalf("boot error %v", err)
	}
}
