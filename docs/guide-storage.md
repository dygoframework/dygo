# Storage: keep files

The `storage` module keeps an app's files, such as uploads, avatars and
exports, in a folder on disk or in a bucket. Services take one small
interface, `storage.Store`, and the driver behind it is picked in `.env`, so
moving from a folder to a bucket later is a config change, not a code change.

`storage` is its own Go module, in beta. It uses only the standard library and
Dygo core.

| Package | Holds |
| --- | --- |
| `github.com/dygoframework/dygo/storage` | `storage.Store`, `Object`, errors, key checks, `SaveUpload`, `storage.Module` |
| `.../storage/local` | the `local` driver: files in a folder on disk |
| `.../storage/storagetest` | a temporary store for tests, and the checks every driver passes |

The drivers are `local`, `s3` (AWS S3) and `rustfs` (a RustFS server, which
speaks the S3 API). `local` is ready now; `s3` and `rustfs` come in the next
release, in their own module so that an app on local disk never downloads the
S3 client.

## Set up

```sh
go get github.com/dygoframework/dygo/storage
```

Import `storage.Module` once, in the root module, with the drivers the app may
use:

```go
import (
    "github.com/dygoframework/dygo/storage"
    "github.com/dygoframework/dygo/storage/local"
)

var AppModule = &dygo.Module{
    Name: "app",
    Imports: []*dygo.Module{
        storage.Module(local.Driver),
        avatars.Module,
    },
}
```

`storage.Module` reads its settings from the environment, `.env.local` and
`.env` (real variables win), builds the driver `STORAGE_DRIVER` names, logs
`storage: ready driver=local` once at startup, and provides `storage.Store` to
every module.

| Variable | Default | Meaning |
| --- | --- | --- |
| `STORAGE_DRIVER` | `local` | which driver runs |
| `STORAGE_LOCAL_DIR` | `storage` | the folder the `local` driver keeps files in |
| `STORAGE_PUBLIC_URL` | empty | where that folder is served from, such as a CDN; used by `URL` |

Add `storage/` (or your `STORAGE_LOCAL_DIR`) to `.gitignore`. To set the
store up from code instead, for a tool or a test, use
`storage.ModuleWith(storage.Config{...}, local.Driver)`.

## Save and read files

A service takes the store like any other dependency:

```go
type ReportService struct{ files storage.Store }

func NewReportService(s storage.Store) *ReportService { return &ReportService{files: s} }

func (s *ReportService) Save(ctx context.Context, id string, csv []byte) error {
    _, err := s.files.Put(ctx, "reports/"+id+".csv", bytes.NewReader(csv),
        storage.ContentType("text/csv"))
    return err
}

func (s *ReportService) Open(ctx context.Context, id string) (io.ReadCloser, error) {
    r, _, err := s.files.Get(ctx, "reports/"+id+".csv")
    return r, err // the caller closes r
}
```

The whole interface:

| Method | Does |
| --- | --- |
| `Put(ctx, key, r, opts...)` | writes `r` under `key`, replacing what was there |
| `Get(ctx, key)` | opens the file; returns a reader and its `Object` |
| `Stat(ctx, key)` | describes the file without reading it |
| `Delete(ctx, key)` | removes it; a missing key is not an error |
| `List(ctx, prefix)` | yields the files under `prefix`, sorted by key |
| `URL(ctx, key, ttl)` | a link a browser can open, or `storage.ErrNoURL` |

`Object` holds `Key`, `Size`, `ContentType`, `ETag` and `Modified`. `List`
returns an iterator:

```go
for obj, err := range s.files.List(ctx, "reports/") {
    if err != nil {
        return err
    }
    fmt.Println(obj.Key, obj.Size)
}
```

## Keys

A key is a path made of segments, such as `avatars/42.png`. Every driver
checks it with `storage.CheckKey` and refuses, with `storage.ErrInvalidKey`, a
key that is empty, longer than 1024 bytes, starts or ends with `/`, has an
empty, `.` or `..` segment, or holds a backslash or a control character. So a
key built from user input can never reach outside the store. Still, build keys
from your own ids (`"avatars/" + user.ID + ".png"`) rather than from file
names users send.

Give keys an extension: the `local` driver keeps no metadata and takes the
content type from it on `Get` and `Stat`.

## Errors

Match errors with `errors.Is`:

- `storage.ErrNotFound`: no file at that key.
- `storage.ErrInvalidKey`: the key failed the check above.
- `storage.ErrNoURL`: the store cannot make a link.

Returned from a handler, a not-found error answers 404 and an invalid key 400
on its own, so most handlers just return the error.

## Uploads

`storage.SaveUpload` stores a file from a form. Bind the file with a
`file:"..."` field and pass limits:

```go
type AvatarIn struct {
    ID   string                `path:"id"`
    File *multipart.FileHeader `file:"file" required:"true"`
}

func (c *AvatarController) Routes(r dygo.Router) {
    // The app's body limit is 1 MiB by default; raise it for this route.
    r.Post("/{id}/avatar", dygo.Handle(c.Upload), dygo.BodyLimit(3<<20))
}

func (c *AvatarController) Upload(ctx context.Context, in AvatarIn) (storage.Object, error) {
    return storage.SaveUpload(ctx, c.files, in.File, "avatars/"+in.ID+".png", storage.Limits{
        MaxBytes: 2 << 20, // 2 MiB
        Types:    []string{"image/png", "image/jpeg"},
    })
}
```

- `MaxBytes` is checked against the size in the form and against the bytes
  actually read, so a client can't get around it by lying about the size. A
  file over the limit answers 413 and nothing is kept.
- `Types` are checked against the content type read from the file's first
  bytes, not the file name or the browser's header. `image/*` accepts any
  image. A file of another type answers 415.
- The detected content type is stored with the file.
- Keep the route's `dygo.BodyLimit` a little above `MaxBytes`: the body also
  holds the form's other fields and boundaries.

## Links

`URL(ctx, key, ttl)` returns a link a browser can open. With the `local`
driver it is `STORAGE_PUBLIC_URL` + the key, when that is set, and
`storage.ErrNoURL` otherwise; serve the folder from a CDN or a static file
server at that address. Links from the `local` driver don't expire. The `s3`
and `rustfs` drivers will return signed links that last `ttl`.

## The local driver

`local` keeps files in `STORAGE_LOCAL_DIR`, creating the folder at startup.

- Every file operation goes through an `os.Root`, so nothing outside the
  folder can be read or written, not even through a symbolic link inside it.
- A write goes to a temporary file and is renamed into place, so a reader
  never sees half a file, and a failed or cancelled write leaves nothing.
- The folder is closed when the app shuts down.

It suits development, tests and single-server apps. When the app runs on more
than one server, use a bucket.

## Health check

With the health module imported, the store shows up in `GET /health/ready` as
`storage`. For the `local` driver the check writes and removes a small
temporary file, so a full disk or a read-only folder turns the app not ready.

## Testing

`storagetest.Local(t)` returns a store in a temporary folder that is removed
when the test ends. Put it in place of the app's store:

```go
func TestUpload(t *testing.T) {
    files := storagetest.Local(t)
    srv := dygotest.New(app.AppModule).
        OverrideValue(dygo.TypeOf[storage.Store](), files).
        CompileHTTP(t)
    // ... POST a file, then check files.Stat(ctx, "avatars/1.png")
}
```

A service test can call `storagetest.Local(t)` and pass it to the
constructor directly. `storagetest.Run(t, store)` is the set of checks every
driver passes; a custom driver can run it from its own tests.
