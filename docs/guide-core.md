# Guide: modules, providers and lifecycle

A short tour of what you can use today. Full API in the Go doc comments (`go doc github.com/dygoframework/dygo`).

## A provider and a module

```go
type UserService struct{ repo Repo }

func NewUserService(repo Repo) *UserService { return &UserService{repo: repo} }

var Module = &dygo.Module{
    Name:    "users",
    Imports: []*dygo.Module{store.Module},
    Providers: []dygo.Provider{
        dygo.Provide(NewStoreRepo, dygo.As[Repo]()), // bind *StoreRepo to the Repo interface
        dygo.Provide(NewUserService),
    },
    Exports: []dygo.Token{dygo.TypeOf[*UserService]()},
}
```

## Starting the app

```go
app, err := dygo.CreateApplicationContext(ctx, AppModule)
if err != nil {
    fmt.Fprintln(os.Stderr, err) // lists every wiring problem at once
    os.Exit(1)
}
svc := dygo.MustGet[*users.UserService](app)
_ = app.Run(ctx) // waits for Ctrl+C / SIGTERM, then runs shutdown hooks
```

## Values, named tokens, many dependencies

```go
var PrimaryDSN = dygo.NewToken[string]("dsn.primary")

dygo.Value("postgres://...", dygo.For(PrimaryDSN))

type Params struct {
    dygo.In
    DSN     string       `name:"dsn.primary"`
    Cache   Cache        `optional:"true"`  // zero value if nobody provides it
    Plugins []Plugin     `group:"plugins"`  // every provider registered with dygo.Group("plugins")
}

func NewThing(p Params) *Thing { ... }
```

## Dynamic module (forRoot)

```go
func ForRoot(opts Options) *dygo.Module {
    return &dygo.Module{
        Name:      "config",
        Global:    true,
        Providers: []dygo.Provider{dygo.Value(opts), dygo.Provide(load)},
        Exports:   []dygo.Token{dygo.TypeOf[*Config]()},
    }
}
```

## Breaking a cycle

```go
func NewA(b *B) *A { ... }
func NewB(a dygo.Lazy[*A]) *B { return &B{a: a} }   // call b.a.Get() later, not in the constructor
```

## Lifecycle

Implement any of these on a provider:

```go
func (s *Store) OnModuleInit(ctx context.Context) error                       // connect
func (s *Store) OnApplicationBootstrap(ctx context.Context) error             // start workers
func (s *Store) OnModuleDestroy(ctx context.Context) error                    // stop workers
func (s *Store) BeforeApplicationShutdown(ctx context.Context, sig string) error
func (s *Store) OnApplicationShutdown(ctx context.Context, sig string) error   // close connections
```

Startup goes from modules with no imports up to the root; shutdown goes the other way.

## Testing

```go
app := dygotest.New(AppModule).
    OverrideValue(dygo.TypeOf[users.Repo](), &fakeRepo{}).
    Compile(t) // fails the test on boot error, shuts down on cleanup
```

See `examples/worker` for all of this working together.
