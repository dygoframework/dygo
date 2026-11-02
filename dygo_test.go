package dygo_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/dygotest"
)

// --- fixtures ---------------------------------------------------------------

type Config struct{ DSN string }

type DB struct{ cfg *Config }

func NewDB(cfg *Config) *DB { return &DB{cfg: cfg} }

type Repo interface{ Find(id string) string }

type PgRepo struct{ db *DB }

func NewPgRepo(db *DB) *PgRepo                          { return &PgRepo{db: db} }
func (r *PgRepo) Find(id string) string                 { return "pg:" + id + "@" + r.db.cfg.DSN }
func NewUserService(r Repo) *UserService                { return &UserService{repo: r} }
func (s *UserService) Get(id string) string             { return s.repo.Find(id) }
func NewUserController(s *UserService) *UsersController { return &UsersController{svc: s} }

type UserService struct{ repo Repo }

type UsersController struct{ svc *UserService }

func dbModule(cfg *Config) *dygo.Module {
	return &dygo.Module{
		Name:      "database",
		Providers: []dygo.Provider{dygo.Value(cfg), dygo.Provide(NewDB)},
		Exports:   []dygo.Token{dygo.TypeOf[*DB]()},
	}
}

func usersModule(db *dygo.Module) *dygo.Module {
	return &dygo.Module{
		Name:    "users",
		Imports: []*dygo.Module{db},
		Providers: []dygo.Provider{
			dygo.Provide(NewPgRepo, dygo.As[Repo]()),
			dygo.Provide(NewUserService),
		},
		Controllers: []dygo.Provider{dygo.Provide(NewUserController)},
		Exports:     []dygo.Token{dygo.TypeOf[*UserService]()},
	}
}

func boot(t *testing.T, root *dygo.Module, opts ...dygo.Option) *dygo.App {
	t.Helper()
	app, err := dygo.CreateApplicationContext(context.Background(), root, opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close(context.Background()) })
	return app
}

func bootErr(t *testing.T, root *dygo.Module, opts ...dygo.Option) string {
	t.Helper()
	_, err := dygo.CreateApplicationContext(context.Background(), root, opts...)
	if err == nil {
		t.Fatal("expected a boot error")
	}
	var be *dygo.BootError
	if !errors.As(err, &be) {
		t.Fatalf("expected *BootError, got %T: %v", err, err)
	}
	return err.Error()
}

func mustContain(t *testing.T, s string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(s, p) {
			t.Errorf("expected %q in:\n%s", p, s)
		}
	}
}

// --- injection and modules ---------------------------------------------------

func TestConstructorInjectionAcrossModules(t *testing.T) {
	db := dbModule(&Config{DSN: "postgres://x"})
	app := boot(t, &dygo.Module{Name: "app", Imports: []*dygo.Module{usersModule(db)}})

	svc := dygo.MustGet[*UserService](app)
	if got := svc.Get("1"); got != "pg:1@postgres://x" {
		t.Fatalf("got %q", got)
	}
	ctrls := app.Controllers()
	if len(ctrls) != 1 || ctrls[0].(*UsersController).svc != svc {
		t.Fatalf("controller not built with the shared singleton: %#v", ctrls)
	}
}

func TestSharedModuleIsOneInstance(t *testing.T) {
	db := dbModule(&Config{})
	a := &dygo.Module{Name: "a", Imports: []*dygo.Module{db}, Providers: []dygo.Provider{
		dygo.Provide(func(d *DB) *UserService { return &UserService{repo: &PgRepo{db: d}} }),
	}, Exports: []dygo.Token{dygo.TypeOf[*UserService]()}}
	b := &dygo.Module{Name: "b", Imports: []*dygo.Module{db}, Providers: []dygo.Provider{
		dygo.Provide(func(d *DB) *PgRepo { return &PgRepo{db: d} }),
	}, Exports: []dygo.Token{dygo.TypeOf[*PgRepo]()}}
	app := boot(t, &dygo.Module{Name: "app", Imports: []*dygo.Module{a, b}})

	s := dygo.MustGet[*UserService](app)
	r := dygo.MustGet[*PgRepo](app)
	if s.repo.(*PgRepo).db != r.db {
		t.Fatal("database module was instantiated twice")
	}
}

func TestMissingExportHint(t *testing.T) {
	db := &dygo.Module{Name: "database", Providers: []dygo.Provider{dygo.Value(&Config{}), dygo.Provide(NewDB)}}
	msg := bootErr(t, &dygo.Module{Name: "app", Imports: []*dygo.Module{usersModule(db)}})
	mustContain(t, msg, `needs *dygo_test.DB`, `provided by module "database"`, `add it to that module's Exports`, `app > users`)
}

func TestMissingImportHint(t *testing.T) {
	db := dbModule(&Config{})
	users := usersModule(db)
	users.Imports = nil
	msg := bootErr(t, &dygo.Module{Name: "app", Imports: []*dygo.Module{users, db}})
	mustContain(t, msg, `exported by module "database": import "database" into "users"`)
}

func TestAllProblemsReportedTogether(t *testing.T) {
	msg := bootErr(t, &dygo.Module{Name: "app", Providers: []dygo.Provider{
		dygo.Provide(NewUserService),
		dygo.Provide(NewDB),
		dygo.Provide(42),
	}})
	mustContain(t, msg, "3 problems", "needs dygo_test.Repo", "needs *dygo_test.Config", "expected a constructor function")
}

func TestGlobalModule(t *testing.T) {
	db := dbModule(&Config{DSN: "g"})
	db.Global = true
	users := usersModule(db)
	users.Imports = nil
	app := boot(t, &dygo.Module{Name: "app", Imports: []*dygo.Module{db, users}})
	if got := dygo.MustGet[*UserService](app).Get("1"); got != "pg:1@g" {
		t.Fatal(got)
	}
}

func TestReExport(t *testing.T) {
	db := dbModule(&Config{DSN: "re"})
	core := &dygo.Module{Name: "core", Imports: []*dygo.Module{db}, Exports: []dygo.Token{dygo.TypeOf[*DB]()}}
	users := usersModule(core)
	app := boot(t, &dygo.Module{Name: "app", Imports: []*dygo.Module{users}})
	if got := dygo.MustGet[*UserService](app).Get("1"); got != "pg:1@re" {
		t.Fatal(got)
	}
}

func TestExportOfUnknownToken(t *testing.T) {
	msg := bootErr(t, &dygo.Module{Name: "app", Exports: []dygo.Token{dygo.TypeOf[*DB]()}})
	mustContain(t, msg, `module "app" exports *dygo_test.DB but neither provides it`)
}

func TestDynamicModuleForRoot(t *testing.T) {
	forRoot := func(dsn string) *dygo.Module { return dbModule(&Config{DSN: dsn}) }
	app := boot(t, &dygo.Module{Name: "app", Imports: []*dygo.Module{usersModule(forRoot("dyn"))}})
	if got := dygo.MustGet[*UserService](app).Get("1"); got != "pg:1@dyn" {
		t.Fatal(got)
	}
}

func TestImportCycle(t *testing.T) {
	a := &dygo.Module{Name: "a"}
	b := &dygo.Module{Name: "b", Imports: []*dygo.Module{a}}
	a.Imports = []*dygo.Module{b}
	mustContain(t, bootErr(t, a), "module import cycle: a > b > a")
}

func TestNameRequired(t *testing.T) {
	mustContain(t, bootErr(t, &dygo.Module{}), "Module.Name is required")
}

func TestDuplicateProvider(t *testing.T) {
	msg := bootErr(t, &dygo.Module{Name: "app", Providers: []dygo.Provider{dygo.Value(&Config{}), dygo.Value(&Config{})}})
	mustContain(t, msg, "provided twice")
}

func TestAmbiguousImports(t *testing.T) {
	a := dbModule(&Config{})
	a.Name = "a"
	b := dbModule(&Config{})
	b.Name = "b"
	msg := bootErr(t, &dygo.Module{
		Name: "app", Imports: []*dygo.Module{a, b},
		Providers: []dygo.Provider{dygo.Provide(func(*DB) *UserService { return nil })},
	})
	mustContain(t, msg, `more than one imported module ("a", "b")`)
}

func TestAsRejectsTypeThatDoesNotImplement(t *testing.T) {
	msg := bootErr(t, &dygo.Module{Name: "app", Providers: []dygo.Provider{dygo.Provide(NewDB, dygo.As[Repo]())}})
	mustContain(t, msg, "cannot be registered as dygo_test.Repo")
}

// --- tokens, In structs, optional, groups ------------------------------------

var (
	PrimaryDSN = dygo.NewToken[string]("dsn.primary")
	ReplicaDSN = dygo.NewToken[string]("dsn.replica")
)

type (
	Plugin interface{ Name() string }
	plugin string
)

func (p plugin) Name() string { return string(p) }

type Cache interface{ Hit() bool }

type Wiring struct {
	Primary, Replica string
	Plugins          []string
	HasCache         bool
	Port             int
}

func TestNamedTokensInStructOptionalAndGroups(t *testing.T) {
	type params struct {
		dygo.In
		Primary string   `name:"dsn.primary"`
		Replica string   `name:"dsn.replica"`
		Plugins []Plugin `group:"plugins"`
		Cache   dygo.Optional[Cache]
		Port    int `optional:"true"`
	}
	app := boot(t, &dygo.Module{Name: "app", Providers: []dygo.Provider{
		dygo.Value("p", dygo.For(PrimaryDSN)),
		dygo.Provide(func() string { return "r" }, dygo.For(ReplicaDSN)),
		dygo.Value(plugin("auth"), dygo.Group("plugins")),
		dygo.Provide(func() plugin { return "audit" }, dygo.Group("plugins")),
		dygo.Provide(func(p params) *Wiring {
			w := &Wiring{Primary: p.Primary, Replica: p.Replica, Port: p.Port}
			for _, pl := range p.Plugins {
				w.Plugins = append(w.Plugins, pl.Name())
			}
			_, w.HasCache = p.Cache.Get()
			return w
		}),
	}})
	w := dygo.MustGet[*Wiring](app)
	if w.Primary != "p" || w.Replica != "r" || strings.Join(w.Plugins, ",") != "auth,audit" || w.HasCache || w.Port != 0 {
		t.Fatalf("%+v", w)
	}
	if v, _ := dygo.GetNamed(app, ReplicaDSN); v != "r" {
		t.Fatal(v)
	}
	// group members are not registered under their own type
	if _, err := dygo.Get[plugin](app); !errors.Is(err, dygo.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

type memCache struct{}

func (memCache) Hit() bool { return true }

func TestOptionalPresent(t *testing.T) {
	app := boot(t, &dygo.Module{Name: "app", Providers: []dygo.Provider{
		dygo.Value(memCache{}, dygo.For(dygo.TypeOf[Cache]())),
		dygo.Provide(func(c dygo.Optional[Cache]) *Wiring {
			_, ok := c.Get()
			return &Wiring{HasCache: ok && c.OrElse(nil).Hit()}
		}),
	}})
	if !dygo.MustGet[*Wiring](app).HasCache {
		t.Fatal("optional cache not injected")
	}
}

func TestAlias(t *testing.T) {
	app := boot(t, &dygo.Module{Name: "app", Providers: []dygo.Provider{
		dygo.Value(memCache{}),
		dygo.Alias(dygo.TypeOf[Cache](), dygo.TypeOf[memCache]()),
	}})
	if !dygo.MustGet[Cache](app).Hit() {
		t.Fatal("alias")
	}
}

func TestContextInjection(t *testing.T) {
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "boot")
	app, err := dygo.CreateApplicationContext(ctx, &dygo.Module{Name: "app", Providers: []dygo.Provider{
		dygo.Provide(func(ctx context.Context) *Config { return &Config{DSN: ctx.Value(key{}).(string)} }),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if dygo.MustGet[*Config](app).DSN != "boot" {
		t.Fatal("context not injected")
	}
}

// --- scopes, cycles, lazy ------------------------------------------------------

type Counter struct{ n int }

func TestTransientScope(t *testing.T) {
	built := 0
	app := boot(t, &dygo.Module{Name: "app", Providers: []dygo.Provider{
		dygo.Provide(func() *Counter { built++; return &Counter{n: built} }, dygo.WithScope(dygo.Transient)),
		dygo.Provide(func(a *Counter) *Config { return &Config{DSN: fmt.Sprint(a.n)} }),
		dygo.Provide(func(b *Counter) *DB { return &DB{cfg: &Config{DSN: fmt.Sprint(b.n)}} }),
	}})
	if dygo.MustGet[*Config](app).DSN == dygo.MustGet[*DB](app).cfg.DSN {
		t.Fatal("transient provider was shared")
	}
	if a, b := dygo.MustGet[*Counter](app), dygo.MustGet[*Counter](app); a == b {
		t.Fatal("Get on a transient provider returned the same instance")
	}
}

type (
	Chicken struct{ egg *Egg }
	Egg     struct{ chicken dygo.Lazy[*Chicken] }
)

func TestCycleDetected(t *testing.T) {
	msg := bootErr(t, &dygo.Module{Name: "app", Providers: []dygo.Provider{
		dygo.Provide(func(e *Egg) *Chicken { return &Chicken{egg: e} }),
		dygo.Provide(func(*Chicken) *Egg { return &Egg{} }),
	}})
	mustContain(t, msg, "dependency cycle: *dygo_test.Chicken", "dygo.Lazy[T]")
}

func TestLazyBreaksCycle(t *testing.T) {
	app := boot(t, &dygo.Module{Name: "app", Providers: []dygo.Provider{
		dygo.Provide(func(e *Egg) *Chicken { return &Chicken{egg: e} }),
		dygo.Provide(func(c dygo.Lazy[*Chicken]) *Egg { return &Egg{chicken: c} }),
	}})
	egg := dygo.MustGet[*Egg](app)
	if egg.chicken.MustGet().egg != egg {
		t.Fatal("lazy did not resolve to the singleton")
	}
}

func TestLazyGetDuringConstructionFails(t *testing.T) {
	msg := bootErr(t, &dygo.Module{Name: "app", Providers: []dygo.Provider{
		dygo.Provide(func(e *Egg) *Chicken { return &Chicken{egg: e} }),
		dygo.Provide(func(c dygo.Lazy[*Chicken]) (*Egg, error) {
			_, err := c.Get()
			return &Egg{}, err
		}),
	}})
	mustContain(t, msg, "while it is being built")
}

// --- errors at construction ----------------------------------------------------

func TestConstructorErrorAndPanic(t *testing.T) {
	boom := errors.New("boom")
	_, err := dygo.CreateApplicationContext(context.Background(), &dygo.Module{Name: "app", Providers: []dygo.Provider{
		dygo.Provide(func() (*Config, error) { return nil, boom }),
	}})
	if !errors.Is(err, boom) {
		t.Fatalf("constructor error not wrapped: %v", err)
	}
	msg := bootErr(t, &dygo.Module{Name: "app", Providers: []dygo.Provider{
		dygo.Provide(func() *Config { panic("kaboom") }),
	}})
	mustContain(t, msg, "panicked: kaboom")
}

// --- lifecycle -----------------------------------------------------------------

type hooks struct {
	name   string
	log    *[]string
	failOn string
}

func (h *hooks) rec(ev string) error {
	*h.log = append(*h.log, h.name+"."+ev)
	if ev == h.failOn {
		return errors.New("fail " + h.name)
	}
	if "panic:"+ev == h.failOn {
		panic("hook " + h.name)
	}
	return nil
}
func (h *hooks) OnModuleInit(context.Context) error           { return h.rec("init") }
func (h *hooks) OnApplicationBootstrap(context.Context) error { return h.rec("bootstrap") }
func (h *hooks) OnModuleDestroy(context.Context) error        { return h.rec("destroy") }
func (h *hooks) BeforeApplicationShutdown(_ context.Context, sig string) error {
	return h.rec("before(" + sig + ")")
}
func (h *hooks) OnApplicationShutdown(context.Context, string) error { return h.rec("shutdown") }

type (
	leafHooks struct{ *hooks }
	midHooks  struct{ *hooks }
	rootHooks struct{ *hooks }
)

func lifecycleTree(log *[]string, failOn string) *dygo.Module {
	leaf := &dygo.Module{Name: "leaf", Providers: []dygo.Provider{
		dygo.Provide(func() *leafHooks { return &leafHooks{&hooks{name: "leaf", log: log}} }),
	}, Exports: []dygo.Token{dygo.TypeOf[*leafHooks]()}}
	mid := &dygo.Module{Name: "mid", Imports: []*dygo.Module{leaf}, Providers: []dygo.Provider{
		dygo.Provide(func(*leafHooks) *midHooks { return &midHooks{&hooks{name: "mid", log: log, failOn: failOn}} }),
	}}
	// root is constructed first in registration order but is at the highest level
	return &dygo.Module{Name: "root", Imports: []*dygo.Module{mid}, Providers: []dygo.Provider{
		dygo.Provide(func() *rootHooks { return &rootHooks{&hooks{name: "root", log: log}} }),
		dygo.Provide(func() *hooks { return &hooks{name: "transient", log: log} }, dygo.WithScope(dygo.Transient)),
	}}
}

func TestLifecycleOrder(t *testing.T) {
	var log []string
	app := boot(t, lifecycleTree(&log, ""))
	if err := app.Shutdown(context.Background(), "SIGTERM"); err != nil {
		t.Fatal(err)
	}
	want := "leaf.init mid.init root.init leaf.bootstrap mid.bootstrap root.bootstrap " +
		"root.destroy mid.destroy leaf.destroy " +
		"root.before(SIGTERM) mid.before(SIGTERM) leaf.before(SIGTERM) " +
		"root.shutdown mid.shutdown leaf.shutdown"
	if got := strings.Join(log, " "); got != want {
		t.Fatalf("hook order\n got: %s\nwant: %s", got, want)
	}
	if err := app.Close(context.Background()); err != nil || len(log) != 15 {
		t.Fatal("second Close should do nothing")
	}
}

func TestFailedInitShutsDownInitialized(t *testing.T) {
	var log []string
	_, err := dygo.CreateApplicationContext(context.Background(), lifecycleTree(&log, "init"))
	if err == nil || !strings.Contains(err.Error(), "fail mid") {
		t.Fatalf("expected init failure, got %v", err)
	}
	want := "leaf.init mid.init leaf.destroy leaf.before() leaf.shutdown"
	if got := strings.Join(log, " "); got != want {
		t.Fatalf("got: %s\nwant: %s", got, want)
	}
}

func TestPanickingHooksAreErrors(t *testing.T) {
	var log []string
	_, err := dygo.CreateApplicationContext(context.Background(), lifecycleTree(&log, "panic:init"))
	var pe *dygo.PanicError
	if !errors.As(err, &pe) || pe.Value != "hook mid" {
		t.Fatalf("expected a PanicError from init, got %v", err)
	}
	if got := strings.Join(log, " "); got != "leaf.init mid.init leaf.destroy leaf.before() leaf.shutdown" {
		t.Fatalf("initialized providers not shut down: %s", got)
	}

	log = nil
	app := boot(t, lifecycleTree(&log, "panic:shutdown"))
	err = app.Shutdown(context.Background(), "")
	if !errors.As(err, &pe) || !strings.Contains(err.Error(), "OnApplicationShutdown") {
		t.Fatalf("expected a PanicError from shutdown, got %v", err)
	}
	if log[len(log)-1] != "leaf.shutdown" {
		t.Fatalf("hooks after the panic did not run: %v", log)
	}
}

func TestRunStopsOnContextCancel(t *testing.T) {
	var log []string
	app, err := dygo.CreateApplicationContext(context.Background(), lifecycleTree(&log, ""),
		dygo.WithShutdownTimeout(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := app.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if log[len(log)-1] != "leaf.shutdown" {
		t.Fatalf("shutdown hooks did not run: %v", log)
	}
}

// --- ModuleRef, App.Get, discovery ---------------------------------------------

type Factory struct{ ref *dygo.ModuleRef }

func TestModuleRef(t *testing.T) {
	db := dbModule(&Config{DSN: "ref"})
	app := boot(t, &dygo.Module{Name: "app", Imports: []*dygo.Module{db}, Providers: []dygo.Provider{
		dygo.Provide(func(r *dygo.ModuleRef) *Factory { return &Factory{ref: r} }),
	}})
	f := dygo.MustGet[*Factory](app)
	if f.ref.Module() != "app" {
		t.Fatal(f.ref.Module())
	}
	if d := dygo.MustGet[*DB](f.ref); d.cfg.DSN != "ref" {
		t.Fatal("ModuleRef.Get")
	}
	// *Config is private to the database module
	if _, err := dygo.Get[*Config](f.ref); !errors.Is(err, dygo.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	repo, err := dygo.Construct[*PgRepo](f.ref, NewPgRepo)
	if err != nil || repo.db.cfg.DSN != "ref" {
		t.Fatalf("Create: %v", err)
	}
	if _, err := f.ref.Create(NewUserService); err == nil {
		t.Fatal("Create should fail when a dependency is not visible")
	}
}

func TestAppGetFindsPrivateProvidersButReportsAmbiguity(t *testing.T) {
	a, b := dbModule(&Config{}), dbModule(&Config{})
	a.Name, b.Name = "a", "b"
	app := boot(t, &dygo.Module{Name: "app", Imports: []*dygo.Module{a}, Providers: []dygo.Provider{
		dygo.Provide(func() *UserService { return &UserService{} }),
	}})
	if _, err := dygo.Get[*Config](app); err != nil {
		t.Fatalf("non-strict Get should find a private provider: %v", err)
	}
	app2 := boot(t, &dygo.Module{Name: "app", Imports: []*dygo.Module{a, b}})
	if _, err := dygo.Get[*Config](app2); !errors.Is(err, dygo.ErrAmbiguous) {
		t.Fatalf("expected ErrAmbiguous, got %v", err)
	}
}

func TestProvidersImplementing(t *testing.T) {
	var log []string
	app := boot(t, lifecycleTree(&log, ""))
	got := dygo.ProvidersImplementing[dygo.OnModuleInit](app)
	if len(got) != 3 {
		t.Fatalf("expected 3 singletons with OnModuleInit, got %d", len(got))
	}
	if n := len(app.Providers()); n != 3 {
		t.Fatalf("expected 3 built providers, got %d", n)
	}
}

// --- testing helpers -----------------------------------------------------------

type fakeRepo struct{}

func (fakeRepo) Find(id string) string { return "fake:" + id }

func TestOverrideSkipsOriginalProvider(t *testing.T) {
	built := false
	users := &dygo.Module{Name: "users", Providers: []dygo.Provider{
		dygo.Provide(func() (*PgRepo, error) { built = true; return nil, errors.New("no database in tests") }, dygo.As[Repo]()),
		dygo.Provide(NewUserService),
	}}
	app := dygotest.New(users).
		OverrideProvider(dygo.TypeOf[*PgRepo](), dygo.Value(&PgRepo{db: &DB{cfg: &Config{DSN: "x"}}})).
		OverrideValue(dygo.TypeOf[Repo](), fakeRepo{}).
		Compile(t)
	if got := dygo.MustGet[*UserService](app).Get("7"); got != "fake:7" || built {
		t.Fatalf("got %q, original built: %v", got, built)
	}
}

func TestUnusedOverrideIsAnError(t *testing.T) {
	_, err := dygotest.Module(nil).OverrideValue(dygo.TypeOf[Repo](), fakeRepo{}).Build(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no module provides this token") {
		t.Fatalf("got %v", err)
	}
}
