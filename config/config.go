// Package config loads typed configuration from the environment and .env
// files:
//
//	type Config struct {
//		Port     int           `env:"PORT" default:"9900"`
//		DBURL    string        `env:"DB_URL" required:"true"`
//		Timeout  time.Duration `env:"TIMEOUT" default:"5s"`
//		Origins  []string      `env:"CORS_ORIGINS"` // comma separated
//		Redis    RedisConfig   `envPrefix:"REDIS_"` // REDIS_ADDR, ...
//	}
//
//	var AppModule = &dygo.Module{
//		Imports: []*dygo.Module{config.ForRoot[Config](config.Files(".env"))},
//	}
//
//	func NewServer(cfg *Config) *Server { ... }
//
// Real environment variables win over .env files. Every missing
// or invalid value is reported at once and fails the boot.
package config

import (
	"bufio"
	"context"
	"encoding"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/validate"
)

// Option configures loading.
type Option func(*options)

type options struct {
	files  []string
	prefix string
	lookup func(string) (string, bool)
	schema validate.Schema
}

// Files reads .env files, earlier files winning over later ones. Missing files
// are skipped, so ".env.local" can be listed for developers only.
func Files(paths ...string) Option {
	return func(o *options) { o.files = append(o.files, paths...) }
}

// Prefix is put in front of every variable name, to give each part of the
// config its own namespace: with Prefix("USERS_"), `env:"LIMIT"` reads
// USERS_LIMIT.
func Prefix(p string) Option { return func(o *options) { o.prefix = p } }

// Env replaces the process environment, for tests.
func Env(vars map[string]string) Option {
	return func(o *options) {
		o.lookup = func(k string) (string, bool) { v, ok := vars[k]; return v, ok }
	}
}

// Validate checks the loaded struct with a schema (for example
// validate.Methods() or the playground adapter) after loading.
func Validate(s validate.Schema) Option { return func(o *options) { o.schema = s } }

// Load reads T from the environment. T must be a struct.
func Load[T any](opts ...Option) (*T, error) {
	o := options{lookup: os.LookupEnv}
	for _, fn := range opts {
		fn(&o)
	}
	cfg := new(T)
	v := reflect.ValueOf(cfg).Elem()
	if v.Kind() != reflect.Struct {
		return nil, fmt.Errorf("config: %s is not a struct", v.Type())
	}
	file := map[string]string{}
	for i := len(o.files) - 1; i >= 0; i-- {
		vars, err := readDotEnv(o.files[i])
		if err != nil {
			return nil, err
		}
		for k, val := range vars {
			file[k] = val
		}
	}
	lookup := func(k string) (string, bool) {
		if val, ok := o.lookup(k); ok {
			return val, true
		}
		val, ok := file[k]
		return val, ok
	}
	var errs []error
	fill(v, o.prefix, lookup, &errs)
	if len(errs) > 0 {
		return nil, fmt.Errorf("config %s: %w", v.Type(), errors.Join(errs...))
	}
	if o.schema != nil {
		if err := o.schema.Validate(context.Background(), cfg); err != nil {
			return nil, fmt.Errorf("config %s: %w", v.Type(), err)
		}
	}
	return cfg, nil
}

// ForRoot returns a global module that loads T at boot and provides *T to
// every module.
func ForRoot[T any](opts ...Option) *dygo.Module {
	return &dygo.Module{
		Name:      "config." + reflect.TypeFor[T]().String(),
		Global:    true,
		Providers: []dygo.Provider{dygo.Provide(func() (*T, error) { return Load[T](opts...) })},
		Exports:   []dygo.Token{dygo.TypeOf[*T]()},
	}
}

// ForFeature is ForRoot for one feature module's settings: not global, so the
// module that needs it imports it. Use Prefix to namespace the variables.
func ForFeature[T any](opts ...Option) *dygo.Module {
	m := ForRoot[T](opts...)
	m.Global = false
	return m
}

var textUnmarshaler = reflect.TypeFor[encoding.TextUnmarshaler]()

func fill(v reflect.Value, prefix string, lookup func(string) (string, bool), errs *[]error) {
	t := v.Type()
	for i := range t.NumField() {
		sf := t.Field(i)
		if !sf.IsExported() {
			continue
		}
		fv := v.Field(i)
		name, hasEnv := sf.Tag.Lookup("env")
		if !hasEnv && fv.Kind() == reflect.Struct && !reflect.PointerTo(sf.Type).Implements(textUnmarshaler) && sf.Type != reflect.TypeFor[time.Time]() {
			fill(fv, prefix+sf.Tag.Get("envPrefix"), lookup, errs)
			continue
		}
		if !hasEnv || name == "-" {
			continue
		}
		key := prefix + name
		raw, ok := lookup(key)
		if !ok || raw == "" {
			if d, has := sf.Tag.Lookup("default"); has {
				raw, ok = d, true
			}
		}
		if !ok || raw == "" {
			if sf.Tag.Get("required") == "true" {
				*errs = append(*errs, fmt.Errorf("%s is required", key))
			}
			continue
		}
		if err := set(fv, raw); err != nil {
			*errs = append(*errs, fmt.Errorf("%s: %w", key, err))
		}
	}
}

func set(v reflect.Value, s string) error {
	if v.CanAddr() && v.Addr().Type().Implements(textUnmarshaler) {
		return v.Addr().Interface().(encoding.TextUnmarshaler).UnmarshalText([]byte(s))
	}
	if v.Type() == reflect.TypeFor[time.Duration]() {
		d, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("%q is not a duration", s)
		}
		v.SetInt(int64(d))
		return nil
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString(s)
	case reflect.Bool:
		b, err := strconv.ParseBool(s)
		if err != nil {
			return fmt.Errorf("%q is not a bool", s)
		}
		v.SetBool(b)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(s, 10, v.Type().Bits())
		if err != nil {
			return fmt.Errorf("%q is not an integer", s)
		}
		v.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(s, 10, v.Type().Bits())
		if err != nil {
			return fmt.Errorf("%q is not a non-negative integer", s)
		}
		v.SetUint(n)
	case reflect.Float32, reflect.Float64:
		f, err := strconv.ParseFloat(s, v.Type().Bits())
		if err != nil {
			return fmt.Errorf("%q is not a number", s)
		}
		v.SetFloat(f)
	case reflect.Slice:
		parts := strings.Split(s, ",")
		out := reflect.MakeSlice(v.Type(), len(parts), len(parts))
		for i, p := range parts {
			if err := set(out.Index(i), strings.TrimSpace(p)); err != nil {
				return err
			}
		}
		v.Set(out)
	case reflect.Pointer:
		p := reflect.New(v.Type().Elem())
		if err := set(p.Elem(), s); err != nil {
			return err
		}
		v.Set(p)
	default:
		return fmt.Errorf("unsupported type %s", v.Type())
	}
	return nil
}

// readDotEnv parses KEY=VALUE lines. It accepts comments, blank lines, an
// "export " prefix, and single or double quoted values (double quoted values
// understand \n). A missing file is not an error.
func readDotEnv(path string) (map[string]string, error) {
	f, err := os.Open(path) //nolint:gosec // G304: the app names its own .env files
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	defer func() { _ = f.Close() }()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, val, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("config: %s:%d: expected KEY=VALUE", path, n)
		}
		k, val = strings.TrimSpace(k), strings.TrimSpace(val)
		switch {
		case len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"':
			val = strings.NewReplacer(`\n`, "\n", `\"`, `"`, `\\`, `\`).Replace(val[1 : len(val)-1])
		case len(val) >= 2 && val[0] == '\'' && val[len(val)-1] == '\'':
			val = val[1 : len(val)-1]
		default:
			if i := strings.Index(val, " #"); i >= 0 {
				val = strings.TrimSpace(val[:i])
			}
		}
		out[k] = val
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("config: %s: %w", path, err)
	}
	return out, nil
}
