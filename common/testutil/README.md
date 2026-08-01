# common/testutil

Shared test helpers for the one-api Go project. Anything that needs to
be reused by `_test.go` files across packages belongs here.

The helpers fall into three buckets:

## 1. In-memory database

```go
func TestWhatever(t *testing.T) {
    t.Parallel()
    db := testutil.NewMockDB(t)              // fresh sqlite file under t.TempDir()
    _ = testutil.NewMockDBForCommon(t)       // sets common.UsingSQLite + clears MySQL/Postgres flags
    ...
}
```

Each call allocates a unique on-disk SQLite file via
`gorm.io/driver/sqlite` and runs `model.AutoMigrateAll` against it, so
parallel tests do not share state and there is no risk of the shared
`file::memory:?cache=shared` mode leaking rows between subtests.

The on-disk choice is deliberate: shared in-memory SQLite caches have
caused intermittent CI failures in the past. The cost of a few
milliseconds per test is well worth the determinism.

## 2. HTTP mocking

```go
client := testutil.NewMockHTTPClient(t)   // *http.Client with the mock transport wired up
defer /* t.Cleanup */                     // (auto)

tr := testutil.NewMockTransport(t)
tr.Respond("GET", "https://api.example.test/foo", testutil.NewJSONHandler(200, []byte(`{"ok":true}`)))
tr.Match("POST", "https://api.example.test/", testutil.NewBytesHandler(204, nil, nil))

// Unhandled requests fail loudly via t.Errorf.
```

`MockTransport.RoundTrip` is concurrency-safe and never touches the
network; failing to register a handler routes through `t.Errorf`
instead of silently leaking to the internet.

## 3. Image fixtures

```go
data := testutil.JPEGBytes()    // deterministic 8x6 RGBA JPEG, generated at package init
png  := testutil.PNGBytes()
gif  := testutil.GIFBytes()
webp := testutil.WebPBytes()    // best-effort: see WebPSupported()

w, h := testutil.ImageSize()    // canonical dimensions, kept in sync with buildFixtures()
```

All fixtures are generated in-process via the standard library
(`image/jpeg`, `image/png`, `image/gif`) plus a hand-rolled WebP
stream that the bundled `golang.org/x/image/webp` decoder accepts.
They are byte-stable across architectures because the source RGBA
buffer uses a deterministic `Set(x, y, …)` pattern.

Tests should never download images from the internet: this package
exists precisely to make `common/image/image_test.go` hermetic.

## 4. Redis

```go
testutil.DisableRedis(t)
// or, when wired up:
client, ok := testutil.NewMiniredisClient(t)
```

Most one-api code paths already branch on `common.RedisEnabled`, so
disabling the feature flag is usually sufficient and avoids pulling
in `github.com/alicebob/miniredis/v2`. The miniredis hook is left as
a no-op placeholder so the project does not vendor that dependency
by accident — see `redis.go` for the opt-in path.

## Parallel-safety checklist

Every helper in this package:

- allocates a per-call tempdir / unique file / fresh handler map so
  parallel tests do not see each other;
- registers a `t.Cleanup` hook that restores any global state
  (e.g. `common.UsingSQLite`, `common.RDB`) when the test ends;
- never panics on misuse — bad inputs surface as `t.Fatal` or
  `t.Errorf` from inside the helper, with the failing test name in
  the output.

If you add a new helper, please follow the same rules:

1. `t.Helper()` as the first statement.
2. Register any global-mutation cleanup before returning.
3. Document the parallel-safety contract in a comment above the
   function.
