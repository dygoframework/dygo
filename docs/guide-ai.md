# AI: call a model

The `ai` module lets a Dygo app ask a large language model a question, stream
the answer to a browser and get typed Go structs back. The vendor is picked in
`.env`, so switching from OpenAI to Anthropic, or to a model running on your
machine, is a config change and not a code change.

`ai` is its own Go module, in beta (`ai/v0.1.0-beta.1`). The `ai` package uses
only the standard library and Dygo core; each vendor is a small package next
to it, also on the standard library.

| Package | Holds |
| --- | --- |
| `github.com/dygoframework/dygo/ai` | `ai.Model`, requests and answers, errors, middleware, `ai.Generate[T]`, `ai.Module` |
| `.../ai/openai` | OpenAI, and servers that speak its chat API: Ollama, vLLM, LM Studio |
| `.../ai/anthropic` | Anthropic |
| `.../ai/fake` | a scripted model for tests: no network, no key |

A complete app is in [`examples/chat`](https://github.com/dygoframework/dygo/tree/main/examples/chat):
a chat page that streams answers, and a route that turns an email into a
typed support ticket.

## Set up

In a project made with `dygo new`, one command does it all:

```sh
dygo ai init                      # asks for the provider
dygo ai init -provider anthropic  # or say it
dygo ai init -provider ollama     # a local model, no key
```

It writes the `AI_*` settings to `.env` and `.env.example`, imports
`ai.Module` in the root module, swaps in the fake model in the app test, and
adds the `ai` module to `go.mod`. Then put your key in `.env`, which is
git-ignored.

By hand, the same is:

```sh
go get github.com/dygoframework/dygo/ai
```

```go
import (
    "github.com/dygoframework/dygo/ai"
    "github.com/dygoframework/dygo/ai/anthropic"
    "github.com/dygoframework/dygo/ai/openai"
)

var AppModule = &dygo.Module{
    Name: "app",
    Imports: []*dygo.Module{
        ai.Module(openai.Provider, anthropic.Provider),
        chat.Module,
    },
}
```

`ai.Module` reads its settings from the environment, `.env.local` and `.env`
(real variables win), builds the provider `AI_PROVIDER` names and provides it
as `ai.Model` to every module. It is global: import it once, in the root
module.

```sh
# .env
AI_PROVIDER=anthropic       # or openai
AI_MODEL=claude-sonnet-5-5  # the vendor's model id
AI_API_KEY=...
```

| Setting | Default | What it does |
| --- | --- | --- |
| `AI_PROVIDER` | required | one of the providers passed to `ai.Module`, by name |
| `AI_MODEL` | required | the model id used when a request does not set one |
| `AI_API_KEY` | | the vendor's key; never logged or printed |
| `AI_BASE_URL` | the vendor's | another server, such as `http://localhost:11434/v1` for Ollama |
| `AI_TIMEOUT` | `60s` | the longest a call may take |
| `AI_MAX_RETRIES` | `2` | retries of a failed call, 0 to 5 |

A missing setting or an unknown provider stops the boot with a message that
says which one. At startup the app logs one line, `ai: provider ready` with
the provider and the model; the key never shows. `AI_BASE_URL` must use https,
unless it points at this machine, so the key never travels in clear text.

For a local model, run Ollama and point the openai provider at it:

```sh
AI_PROVIDER=openai
AI_MODEL=llama3.2
AI_BASE_URL=http://localhost:11434/v1
```

To build the model from code instead of `.env`, use
`ai.ModuleWith(ai.Config{...}, openai.Provider)`.

## Ask and wait

Inject `ai.Model` like any other dependency:

```go
type Service struct{ model ai.Model }

func NewService(m ai.Model) *Service { return &Service{model: m} }

func (s *Service) Summary(ctx context.Context, text string) (string, error) {
    return ai.Ask(ctx, s.model, "Summarize in two sentences: "+text)
}
```

`ai.Ask` sends one user message and returns the text. For more control, build
an `ai.Request`:

```go
res, err := s.model.Chat(ctx, ai.Request{
    System:    "You answer in French.",
    Messages:  []ai.Message{ai.User("Hello"), ai.Assistant("Bonjour !"), ai.User("How are you?")},
    MaxTokens: 300,
})
fmt.Println(res.Text(), res.Usage.InputTokens, res.Usage.OutputTokens)
```

`Model` empty means `AI_MODEL`; set `Request.Model` to use another model for
one call. `ai.Prompt("...")` is a request with one user message.

## Stream to the browser

`Model.Stream` yields the answer in chunks as the vendor writes it.
`ai.SSE` sends them to the browser as server-sent events:

```go
type AskIn struct {
    Body struct {
        Question string `json:"question"`
    } `body:"json"`
}

func (c *Controller) Routes(r dygo.Router) {
    r.Post("/ask", dygo.Handle(c.ask))
}

func (c *Controller) ask(ctx context.Context, in AskIn) (dygo.Responder, error) {
    return ai.SSE(c.model.Stream(ctx, ai.Prompt(in.Body.Question))), nil
}
```

The browser gets one event per chunk with its text, then an `end` event. If
the call fails part way, the last event is `error` with a short message that
is safe to show (see [Errors](#errors)). When the browser goes away, the
request context is cancelled and so is the call to the vendor, so no tokens
are wasted.

`EventSource` only sends GET requests, so a page that posts a question reads
the stream with `fetch`. The page in
[`examples/chat`](https://github.com/dygoframework/dygo/tree/main/examples/chat)
does it in about twenty lines of plain JavaScript.

To read a whole stream in Go, `ai.Collect(stream)` returns an `ai.Response`.
It stops at `ai.MaxAnswerBytes` (8 MiB) with `ai.ErrTooLarge`.

### Over a WebSocket

In a `ws` gateway, `ai.Emit` sends the same chunks through the client:

```go
ws.HandleNoReply(func(ctx context.Context, c *ws.Client, in AskInput) error {
    return ai.Emit(ctx, g.model.Stream(ctx, ai.Prompt(in.Question)), c.Emit, "answer")
})
```

The client gets `answer` events with the text, then `answer.end`, or
`answer.error` with the safe message. `ai` does not import `ws`; it only needs
the `Emit` function.

## Typed output

`ai.Generate[T]` asks for an answer shaped like a struct and decodes it:

```go
type Ticket struct {
    Title    string `json:"title" description:"A short title"`
    Priority string `json:"priority" validate:"oneof=low medium high"`
}

func (t *Ticket) Validate(ctx context.Context) error {
    var v validate.Errors
    if t.Title == "" || len(t.Title) > 80 {
        v.Add("body", "title", "must be 1 to 80 characters")
    }
    return v.Err()
}

t, err := ai.Generate[Ticket](ctx, s.model, ai.Prompt("Make a ticket from: "+email))
```

1. A JSON Schema is built from the struct's `json`, `validate` and
   `description` tags, with the same code as `openapi` (`ai.SchemaFor[T]`
   shows it). Limits such as `max=80` are left out of the schema, because
   vendors' JSON modes refuse most of them; they are checked after decoding.
2. The provider asks the vendor for JSON of that shape, with the vendor's own
   JSON mode.
3. The answer is decoded and checked with the type's `Validate` method
   (`validate.Methods`). Pass `ai.CheckWith(playground.New())` to check
   `validate:"..."` tags instead.
4. When decoding or the check fails, the problem goes back to the model once to
   fix. `ai.Fixes(0)` turns that off, and `ai.Fixes(3)` allows up to three fixes. After
   that the call fails with an error wrapping `ai.ErrBadOutput`.

## Errors

Every provider turns vendor errors into `*ai.Error`, so the same checks work
whatever the vendor:

| Error | When |
| --- | --- |
| `ai.ErrAuth` | the key is wrong or missing |
| `ai.ErrRateLimited` | the vendor says slow down (retried) |
| `ai.ErrUnavailable` | the vendor is down, or a network error (retried) |
| `ai.ErrTooLong` | the prompt does not fit the model |
| `ai.ErrRefused` | the model refused to answer |
| `ai.ErrInvalidRequest` | the request is malformed; nothing was sent |
| `ai.ErrBadOutput` | `Generate` could not get a valid answer |
| `ai.ErrTooLarge` | the answer is over the size limit |

```go
if errors.Is(err, ai.ErrRateLimited) { ... }
```

The vendor's own message is in `*ai.Error` for logs. It is never meant for end
users: `ai.PublicMessage(err)` gives a short text that is safe to show, and
`ai.SSE` and `ai.Emit` use it. In a handler:

```go
if err != nil {
    return Ticket{}, errs.ServiceUnavailable(ai.PublicMessage(err), errs.Cause(err))
}
```

## Retries, timeouts and logs

The model `ai.Module` provides is wrapped in three middleware, set from `.env`:

- `ai.Retry(AI_MAX_RETRIES)` sends a call again when it failed with a rate
  limit, a 5xx or a network error, with backoff and jitter. It honours the
  vendor's retry-after wait up to 30 seconds. A stream is never retried once
  text was sent.
- `ai.Timeout(AI_TIMEOUT)` ends a call that takes too long.
- `ai.Log(logger)` logs one line per call: the model, how long it took, the
  token counts and the error. Prompts and answers are never logged.

A middleware is a `func(ai.Model) ai.Model`. To use a model you built
yourself, with your own middleware, provide it with `ai.Provide`:

```go
ai.Provide(myModel, ai.Log(log), ai.Retry(2), ai.Timeout(30*time.Second))
```

## Testing

Tests use `ai/fake`, a scripted model: no network, no key, no cost.

```go
m := fake.New(
    fake.Reply("Paris"),                 // Chat or Stream answers this
    fake.Chunks("Hel", "lo"),            // Stream yields two chunks
    fake.Fail(ai.ErrRateLimited),        // the call fails
    fake.Broken(ai.ErrUnavailable, "a"), // a stream cut in the middle
)
got, _ := ai.Ask(ctx, m, "Capital of France?") // "Paris"
m.Requests()                                   // every request, to check the prompts
```

In an app test, override the model the module would build from `.env`:

```go
srv := dygotest.New(AppModule()).
    OverrideValue(dygo.TypeOf[ai.Model](), ai.Model(fake.New(fake.Reply("hi")))).
    CompileHTTP(t)
```

`dygo ai init` writes that line into the app test for you.
