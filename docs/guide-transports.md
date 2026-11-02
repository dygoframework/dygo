# Transports and background work

Phase 6 adds the parts that run outside a request: in-process events, scheduled jobs, command and query buses, a cache, background job queues, microservices and WebSocket gateways. The packages in the main module use only the standard library; the ones that need a broker library are separate Go modules.

| Package | What it does | Module |
|---|---|---|
| `events` | in-process events | main |
| `schedule` | cron, interval and timeout jobs | main |
| `cqrs` | command and query buses | main |
| `cache` | key-value cache and response cache | main |
| `queue` | background job queues | main (memory driver), `queue/asynq` (Redis) |
| `micro` | microservices | main (router, client, in-process transport) |
| `micro/nats` | NATS transport | own module |
| `micro/grpc` | gRPC transport | own module |
| `ws` | WebSocket gateways | own module |

Every package follows the same pattern: import its module once in the root module, and any provider with the right method is found at startup (there are no decorators to scan). Registration mistakes, such as a bad cron expression or two handlers for one command, fail the boot with the other wiring errors.

| A provider with | is found by |
|---|---|
| `Events(r *events.Registry)` | `events.Module()` |
| `Jobs(s *schedule.Registry)` | `schedule.Module()` |
| `CQRS(r *cqrs.Registry)` | `cqrs.Module()` |
| `Process(r *queue.Registry)` | `queue.Module(...)` |
| `Patterns(r micro.Router)` | a connected transport (`micro.NewLocal`, `nats.NewServer`) |
| `GRPC(r *grpc.Registrar)` | `grpc.NewServer` |
| `Gateway(g ws.Router)` | `ws.Module(...)` |

`dygo g listener|cron|job|gateway|rpc <name>` writes one of these providers with a test and registers it. `examples/orders` uses most of them in one app.

## Events

```go
type OrderPlaced struct{ ID string }

func (m *Mailer) Events(r *events.Registry) {
	events.On(r, m.onPlaced)                 // func(ctx, OrderPlaced) error
	events.On(r, m.audit, events.Async())    // in a goroutine; shutdown waits for it
}

err := emitter.Emit(ctx, OrderPlaced{ID: id}) // inject *events.Emitter
```

Listeners subscribe by Go type; a listener of an interface type gets every event that implements it. `Emit` returns the errors of synchronous listeners; async errors go to `events.OnError`.

## Schedule

```go
func (r *Reports) Jobs(s *schedule.Registry) {
	s.Cron("nightly", "0 3 * * *", r.nightly, schedule.TimeZone(time.UTC))
	s.Every("refresh", 5*time.Minute, r.refresh)
	s.After("warm-up", 10*time.Second, r.warmUp)
}
```

Cron takes 5 fields, or 6 with seconds first, and `@hourly`, `@daily`, `@weekly`, `@monthly`, `@yearly`. Jobs start in `OnApplicationBootstrap`; a tick that finds the job still running is skipped unless `schedule.AllowOverlap()`. Inject `*schedule.Scheduler` to list (`Jobs()`), `Stop`, `Start`, `Remove` or add jobs at runtime. Shutdown cancels the jobs' context and waits for them.

## CQRS

```go
func (h *UserHandlers) CQRS(r *cqrs.Registry) {
	cqrs.HandleCommand(r, h.create) // func(ctx, CreateUser) error
	cqrs.HandleQuery(r, h.byID)     // func(ctx, UserByID) (*User, error)
}

err := commands.Execute(ctx, CreateUser{Email: e})
u, err := cqrs.Ask[*User](ctx, queries, UserByID{ID: id})
```

`cqrs.Module(cqrs.Use(mw))` wraps every handler, for logging or a transaction per command. Publish domain events with `*events.Emitter`.

## Cache

```go
cache.Module(cache.Options{TTL: time.Minute}) // Store defaults to an in-memory LRU

u, err := cache.GetOrSet(ctx, c, "user:"+id, 0, func(ctx context.Context) (*User, error) { return repo.Find(ctx, id) })

r = r.Use(dygo.Interceptors(dygo.Ref[*cache.Interceptor]()))
r.Get("/products", dygo.Handle(c.List), cache.TTL(30*time.Second))
r.Get("/live", dygo.Handle(c.Live), cache.NoCache())

byID := cache.Wrap(c, "user", time.Minute, users.ByID) // a cached func(ctx, string) (*User, error)
```

Values are stored as JSON. Implement `cache.Store` (three methods) for Redis or another backend.

## Queue

```go
queue.Module(queue.Options{Driver: asynq.New(asynq.RedisClientOpt{Addr: "localhost:6379"})}) // or the memory default

func (m *Mailer) Process(r *queue.Registry) {
	queue.Handle(r, "welcome-email", m.send) // func(ctx, WelcomeEmail) error
}

id, err := q.Add(ctx, "welcome-email", WelcomeEmail{UserID: u.ID},
	queue.Delay(time.Minute), queue.Attempts(5), queue.ID("welcome:"+u.ID))
```

A handler error retries the job with backoff; wrap it with `queue.SkipRetry` to fail at once. `queue.JobFrom(ctx)` gives the job's ID and attempt. Set `NoWorker` in a process that only produces jobs. The memory driver loses jobs on restart; use it in development and tests (`mem.Wait(ctx)` waits until no job is pending).

## Microservices

Handlers are declared like routes, and guards, interceptors, pipes and filters run on them with `ExecutionContext.Kind() == dygo.KindRPC`:

```go
func (h *UsersHandlers) Patterns(r micro.Router) {
	r = r.Use(dygo.Guards(dygo.Ref[*ServiceTokenGuard]()))
	r.Message("users.get", micro.Handle(h.get))            // request/response
	r.Event("orders.created", micro.HandleEvent(h.onOrder)) // fire and forget, any number of handlers
}
```

Serve them over NATS in the same process as HTTP (a hybrid app):

```go
app, _ := dygo.Create(ctx, AppModule())
app.ConnectMicroservice(nats.NewServer(nats.Options{URL: nats.DefaultURL, Queue: "users"}))
app.Listen(ctx, ":9900") // starts NATS too; shutdown closes both
```

Call other services with a `*micro.Client` (`nats.ClientModule(nats.ClientOptions{URL: ...})` provides one):

```go
u, err := micro.Send[*User](ctx, client, "users.get", GetUser{ID: id})
err = client.Emit(ctx, "orders.created", OrderCreated{ID: id})
```

A handler's `*errs.HTTPError` arrives at the caller as the same `*errs.HTTPError`, so a gateway route passes a 404 on unchanged. `micro.WithHeader(ctx, k, v)` and the client's `micro.Before` hook set headers; handlers read them with `micro.MessageFrom(ctx)`. In tests, `micro.NewLocal` is both the transport and the client, with no broker.

Transports run only their own global enhancers (`ServerOptions.Global`). Set `InheritAppConfig` to also run the app's global ones; they must then handle hosts where `HTTP()` is nil.

### Redis and Kafka

The same handlers run over Redis pub/sub or Kafka; only the server and client change:

```go
app.ConnectMicroservice(redis.NewServer(redis.Options{Addr: "localhost:6379"}))
redis.ClientModule(redis.ClientOptions{Redis: goredis.Options{Addr: "localhost:6379"}}) // provides *micro.Client

app.ConnectMicroservice(kafka.NewServer(kafka.Options{Brokers: []string{"localhost:9092"}, GroupID: "users"}))
kafka.ClientModule(kafka.ClientOptions{Brokers: []string{"localhost:9092"}})
```

Redis patterns with `*`, `?` or `[` are glob subscriptions (`orders.*`). Redis pub/sub keeps nothing: an event with no server running is lost, and a request with no server fails at once with `micro.ErrNoHandler`. Kafka patterns starting with `^` are regexps over topic names (`^orders\..*`); every server in one `GroupID` shares the partitions, and each client gets its own reply topic, deleted when it closes.

## gRPC

```go
func (s *UsersService) GRPC(r *grpc.Registrar) {
	r.Register(&pb.Users_ServiceDesc, s,
		grpc.Use(dygo.Guards(dygo.Ref[*TokenGuard]())),
		grpc.Method("DeleteUser", roles.Set([]string{"admin"})))
}

app.ConnectMicroservice(grpc.NewServer(grpc.Options{Addr: ":50051"}))
```

The RPC host's `Message()` is a `*grpc.Call` with the method and incoming metadata. Errors map to real codes: `errs.NotFound` becomes `codes.NotFound`, `BadRequest` `InvalidArgument`, `Unauthorized` `Unauthenticated`, panics `Internal`. `grpc.ToHTTPError(err)` maps a client error back.

## WebSocket gateways

```go
ws.Module(ws.Options{Path: "/ws"}) // served on the app's HTTP server

func (c *Chat) Gateway(g ws.Router) {
	g.On("chat.send", ws.Handle(c.send)) // func(ctx, *ws.Client, In) (Out, error)
}

func (c *Chat) OnConnection(ctx context.Context, cl *ws.Client) error { ... } // an error refuses the client (1008)
func (c *Chat) OnDisconnect(cl *ws.Client, r ws.DisconnectReason) { ... }  // close code and reason
```

Clients send `{"event": "chat.send", "data": {...}, "id": 1}`. The result comes back with the same event and id; an error comes as `{"event": "exception", "data": {"statusCode": 400, ...}, "id": 1}`. `client.Join(room)`, `server.To(room).Emit(...)`, `.Except(ctx, sender, ...)` and `server.Broadcast(...)` send to groups. `client.Request()` is the handshake request (read a token there in `OnConnection`), and `client.Set`/`Get` keep values such as the user for guards, which get the client from `ec.WS().Client()`. Shutdown closes clients with 1001.

Gateways are singletons. By default a request scoped provider that a handler resolves with `dygo.Scoped[T].Get(ctx)` belongs to the handshake request, so it lives as long as the connection. `g.Use(ws.PerMessageScope())` (or the option on one `On`) gives every message its own scope: instances are built for that message, their `OnRequestEnd` runs after the ack is sent, `*http.Request` is the handshake request, and durable providers resolve for the client's context ID. Use it for per-message state such as a transaction; it costs a scope per message that resolves one. Other transports can do the same with `app.WithRequestScope(ctx, req)`.

## GraphQL

Write the schema in `.graphqls` files and run `gqlgen generate` (schema first). The root resolver is a provider, so it injects services:

```go
func NewResolver(users *users.Service) *graph.Resolver { return &graph.Resolver{Users: users} }

var GraphModule = &dygo.Module{
	Name:    "graph",
	Imports: []*dygo.Module{users.Module},
	Providers: []dygo.Provider{
		dygo.Provide(NewResolver),
		dygo.Provide(func(r *graph.Resolver) graphql.ExecutableSchema {
			return graph.NewExecutableSchema(graph.Config{Resolvers: r})
		}),
	},
	Controllers: []dygo.Provider{graphql.Endpoint(graphql.Options{
		Playground: true,
		Global:     []dygo.RouteOption{dygo.Guards(dygo.Ref[*AuthGuard]())},
		Fields:     map[string][]dygo.RouteOption{"Mutation.deleteUser": {roles.Set([]string{"admin"})}},
	})},
}
```

The endpoint is `/graphql` on the app's HTTP server. Guards, interceptors and filters run around each root field with `ec.Kind() == dygo.KindGraphQL`; `ec.GraphQL()` gives `Field()` (`"Mutation.deleteUser"`), `Args()` and the HTTP `Request()`, and `ec.HTTP()` is nil, so a guard written for HTTP routes needs a GraphQL branch. A misspelt `Fields` key fails at boot. Resolvers return `errs.NotFound(...)` and friends as usual; clients get `{"message": "user not found", "extensions": {"code": "NOT_FOUND", "statusCode": 404}}`. Other errors and panics show "Internal server error" and go to `Options.Logger`. Add `transport.Websocket{}` to `Options.Transports` for subscriptions, and use `Options.Configure` for complexity limits. Parsed queries are cached (`Options.QueryCacheSize`, 1000 by default) and automatic persisted queries are on.
