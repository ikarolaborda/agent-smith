# Go — Language and Standard Library Reference

Sources: context7 `/websites/go_dev_ref_spec` (language reference) and `/websites/pkg_go_dev_go1_25_3` (standard library, Go 1.25.3 snapshot).

## Source File Structure

```go
SourceFile = PackageClause ";" { ImportDecl ";" } { TopLevelDecl ";" } .
```

Every Go source file begins with a `package` clause, followed by `import` declarations, then top-level declarations (functions, types, vars, consts).

```go
package main

import (
    "fmt"
    "net/http"
)

func main() {
    fmt.Println("hello")
}
```

The `main` package must declare a `func main()` with no arguments and no return value — it is the program's entry point.

## Keywords

Reserved and cannot be used as identifiers:

```
break    case        chan     const    continue
default  defer       else     fallthrough  for
func     go          goto     if       import
interface  map      package    range    return
select   struct      switch   type     var
```

## Declarations

```go
var x int            // zero-initialized
var y = 42           // type inferred
z := 3.14            // short declaration (function scope only)

const Pi = 3.1415
const (
    KB = 1 << 10
    MB = 1 << 20
)

type Celsius float64
type Pair struct {
    Key, Value string
}
```

Visibility is by capitalization: identifiers starting with an uppercase letter are exported from the package; lowercase identifiers are package-private.

## Control Flow

```go
if x > 0 {
    // ...
} else if x == 0 {
    // ...
} else {
    // ...
}

for i := 0; i < 10; i++ { /* ... */ }
for cond { /* while */ }
for { /* infinite */ }
for i, v := range slice { _, _ = i, v }

switch x {
case 1, 2: doA()
case 3:    doB()
default:   doC()
}

switch {            // tag-less switch == if/else if chain
case x < 0: neg()
case x > 0: pos()
}
```

## Functions and Methods

```go
func add(a, b int) int { return a + b }

func divmod(a, b int) (q, r int) {       // named returns
    return a / b, a % b
}

func varargs(format string, args ...any) {
    fmt.Printf(format, args...)
}

// Method: receiver in parens before the name
type Rect struct{ W, H float64 }

func (r Rect) Area() float64        { return r.W * r.H }
func (r *Rect) Scale(k float64)     { r.W *= k; r.H *= k }   // pointer receiver
```

Use a pointer receiver when the method must mutate the receiver or when the receiver is large. Be consistent: if any method uses a pointer receiver, prefer pointer receivers for ALL methods on that type.

## Interfaces

```go
type Stringer interface {
    String() string
}

type Reader interface {
    Read(p []byte) (n int, err error)
}
```

A type implements an interface implicitly — there is no `implements` keyword. The empty interface `interface{}` (alias `any` since Go 1.18) holds any value.

### Type Assertions and Type Switches

```go
var i any = "hello"
s := i.(string)           // panics if not a string
s, ok := i.(string)       // safe form

switch v := i.(type) {
case string: fmt.Println("string", v)
case int:    fmt.Println("int", v)
default:     fmt.Println("other", v)
}
```

### The error Interface

```go
type error interface {
    Error() string
}
```

A nil value of type `error` represents no error.

## Defer, Panic, Recover

```go
func read(path string) ([]byte, error) {
    f, err := os.Open(path)
    if err != nil {
        return nil, err
    }
    defer f.Close()           // executed when read returns
    return io.ReadAll(f)
}

func safeCall() (err error) {
    defer func() {
        if r := recover(); r != nil {
            err = fmt.Errorf("panicked: %v", r)
        }
    }()
    doRiskyWork()
    return nil
}
```

`panic` aborts normal control flow up the call stack. `recover` is meaningful only inside a deferred function — it stops the panic and returns the panic value. Use panic for unrecoverable invariant violations; for normal failures return an `error`.

## Slices, Maps, Channels — Built-in Allocation

```go
s := make([]int, 0, 16)         // slice with len=0 cap=16
m := make(map[string]int)       // empty map
ch := make(chan int, 8)         // buffered channel

p := new(Rect)                  // *Rect to a zero-valued Rect
```

`make` initializes the underlying header; `new(T)` returns `*T` to a zero-valued T.

### Slice Pitfalls

- A slice shares storage with the array it was sliced from. Mutating the slice mutates the original.
- `append` may reuse the underlying array (no allocation) OR allocate a new one when capacity is exceeded. Don't assume the resulting slice aliases the input.

```go
a := []int{1, 2, 3}
b := a[:2]            // shares backing array with a
b[0] = 99             // a[0] is now 99 too

c := append(a, 4)     // may or may not alias a
```

## Goroutines and Channels

```go
go handleRequest(req)         // start a goroutine

ch := make(chan int)
go func() { ch <- compute() }()
result := <-ch
```

Send on a nil channel blocks forever; receive from a nil channel blocks forever. Send on a closed channel panics. Receive from a closed channel returns the zero value with `ok == false`:

```go
v, ok := <-ch
```

Only the sender should close a channel — and only when no more values will be sent.

### select

```go
select {
case v := <-incoming:
    process(v)
case outgoing <- payload:
    log("sent")
case <-time.After(time.Second):
    log("timeout")
case <-ctx.Done():
    return ctx.Err()
default:
    nonBlockingFallback()
}
```

# Standard Library Packages

## context

Server entry points create a `Context`; outgoing calls accept one. Pass the context as the first parameter named `ctx`.

```go
import "context"

func slowOp(ctx context.Context) (Result, error) {
    ctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
    defer cancel()
    return doWork(ctx)
}

select {
case <-ctx.Done():
    return ctx.Err()        // context.Canceled or context.DeadlineExceeded
case r := <-resultsCh:
    return r, nil
}
```

- `context.WithCancel(parent)` — manual cancel.
- `context.WithTimeout(parent, d)` — auto cancel after `d`.
- `context.WithDeadline(parent, t)` — auto cancel at time `t`.
- `context.WithValue(parent, key, val)` — request-scoped values. Use sparingly; prefer explicit parameters.

Always call the returned `cancel` (typically via `defer`) — otherwise the parent retains the child timer.

## errors

```go
import "errors"

var ErrNotFound = errors.New("not found")

func find(id int) (*Item, error) {
    if id == 0 {
        return nil, ErrNotFound
    }
    // ...
}

// Wrapping
func loadItem(id int) (*Item, error) {
    it, err := find(id)
    if err != nil {
        return nil, fmt.Errorf("load item %d: %w", id, err)
    }
    return it, nil
}

// Inspection
if errors.Is(err, ErrNotFound) {
    // ...
}

var pe *PathError
if errors.As(err, &pe) {
    log.Println("path:", pe.Path)
}
```

Use `%w` in `fmt.Errorf` to wrap an error so `errors.Is` / `errors.As` walk the chain.

## fmt

```go
fmt.Println("a:", a, "b:", b)        // Println adds spaces and newline
fmt.Printf("%-10s %5d\n", name, age) // Printf takes a format
s := fmt.Sprintf("%v", x)
err := fmt.Errorf("decode %q: %w", path, baseErr)

// Common verbs:
// %v  default
// %+v default with struct field names
// %#v Go syntax
// %T  type
// %s  string
// %q  Go-quoted string
// %d  int
// %x %X hex
// %f  float
// %e  scientific
// %p  pointer
// %w  wrap an error (Errorf only)
```

## io

```go
type Reader interface {
    Read(p []byte) (n int, err error)
}
type Writer interface {
    Write(p []byte) (n int, err error)
}
type Closer interface {
    Close() error
}

n, err := io.Copy(dst, src)        // stream copy
data, err := io.ReadAll(r)         // read until EOF
r := io.LimitReader(src, 1024)     // cap to 1024 bytes
```

`io.EOF` is the conventional end-of-stream signal — NOT an error condition, just a marker that no more data will arrive.

## os

```go
f, err := os.Open(path)
defer f.Close()

f2, err := os.Create(path)         // O_WRONLY|O_CREATE|O_TRUNC
f3, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)

args := os.Args                     // []string; Args[0] is the program name
env := os.Getenv("HOME")
os.Setenv("KEY", "value")
host, _ := os.Hostname()
os.Exit(1)
```

`os.Open` returns `*os.File`, which implements `io.Reader`, `io.Writer`, `io.Closer`, etc.

## strings, strconv, bytes

```go
strings.Contains(s, "needle")
strings.Split("a,b,c", ",")        // ["a","b","c"]
strings.TrimSpace(s)
strings.ReplaceAll(s, "old", "new")
strings.EqualFold(a, b)            // case-insensitive equality

strconv.Itoa(42)                   // "42"
strconv.Atoi("42")                 // 42, nil
strconv.FormatFloat(3.14, 'f', 2, 64)  // "3.14"

bytes.Buffer                       // an io.Writer-backed []byte builder
strings.Builder                    // same idea, for strings
```

`strings.Builder` is the idiomatic way to concatenate strings in a loop — it avoids the O(n²) cost of repeated `+`.

## encoding/json

```go
type Person struct {
    Name string `json:"name"`
    Age  int    `json:"age,omitempty"`
}

// Marshal: value -> []byte
buf, err := json.Marshal(p)

// Unmarshal: []byte -> value
var p Person
err := json.Unmarshal(buf, &p)

// Streaming
dec := json.NewDecoder(reader)
for dec.More() {
    var rec Record
    if err := dec.Decode(&rec); err != nil { /* ... */ }
}

enc := json.NewEncoder(writer)
enc.SetIndent("", "  ")
enc.Encode(record)
```

Struct tags: `json:"name"` renames the field; `omitempty` skips zero values; `-` excludes the field entirely.

## net/http

```go
// Server
http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
    fmt.Fprintln(w, "ok")
})
log.Fatal(http.ListenAndServe(":8080", nil))

// Custom mux
mux := http.NewServeMux()
mux.Handle("/users/", http.HandlerFunc(usersHandler))
srv := &http.Server{Addr: ":8080", Handler: mux}
go srv.ListenAndServe()

// Client
resp, err := http.Get("https://example.com/")
if err != nil { return err }
defer resp.Body.Close()
body, _ := io.ReadAll(resp.Body)

// POST JSON
b, _ := json.Marshal(payload)
resp, err := http.Post(url, "application/json", bytes.NewReader(b))
```

The `Handler` interface is:

```go
type Handler interface {
    ServeHTTP(ResponseWriter, *Request)
}
```

`HandlerFunc` is an adapter that lets a plain `func(w, r)` satisfy `Handler`. Panics inside `ServeHTTP` are recovered by the server, logged, and translated to a `500`. Use `panic(http.ErrAbortHandler)` to abort without logging.

ALWAYS `defer resp.Body.Close()` after a successful `http.Get`/`http.Post`/`Client.Do` to release the connection back to the pool.

## time

```go
now := time.Now()
deadline := now.Add(5 * time.Minute)
time.Sleep(200 * time.Millisecond)

d := 30 * time.Second               // time.Duration; int64 of nanoseconds

after := time.After(time.Second)    // returns <-chan Time
select {
case <-after: timedOut()
case <-done:  ok()
}

t, err := time.Parse(time.RFC3339, "2026-05-16T13:00:00Z")
fmt.Println(t.Format(time.RFC3339))
```

`time.Duration` is a typed int64 — multiply by `time.Second`, `time.Millisecond`, etc.

## log/slog

```go
import "log/slog"

slog.Info("user signed in", "user_id", userID, "ip", ip)
slog.Error("db query failed", "err", err, "query", q)

logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
    Level: slog.LevelInfo,
}))
slog.SetDefault(logger)
```

`slog` (Go 1.21+) is structured logging in the stdlib. Pass key-value pairs, not formatted strings. Prefer it over `log.Println` for new code.

## sync

```go
var mu sync.Mutex
var counter int

func inc() {
    mu.Lock()
    defer mu.Unlock()
    counter++
}

var wg sync.WaitGroup
for _, url := range urls {
    wg.Add(1)
    go func(u string) {
        defer wg.Done()
        fetch(u)
    }(url)
}
wg.Wait()

var once sync.Once
once.Do(initialize)                 // runs exactly once across goroutines
```

`sync.Mutex` for mutual exclusion; `sync.RWMutex` when reads vastly outnumber writes; `sync.WaitGroup` to wait for a set of goroutines; `sync.Once` for one-shot initialization.

## testing

```go
// foo_test.go (same package as foo.go)
import "testing"

func TestAdd(t *testing.T) {
    if got := Add(2, 3); got != 5 {
        t.Errorf("Add(2,3) = %d, want 5", got)
    }
}

// Table-driven
func TestParse(t *testing.T) {
    cases := []struct{ in, want string }{
        {"a=1", "a=1"},
        {"  b=2  ", "b=2"},
    }
    for _, tc := range cases {
        t.Run(tc.in, func(t *testing.T) {
            t.Helper()
            if got := Parse(tc.in); got != tc.want {
                t.Errorf("Parse(%q) = %q, want %q", tc.in, got, tc.want)
            }
        })
    }
}

// Benchmark
func BenchmarkAdd(b *testing.B) {
    for i := 0; i < b.N; i++ { _ = Add(1, 2) }
}
```

Run with `go test ./...`; race detection with `go test -race ./...`; coverage with `go test -cover`.

# Tooling

```sh
go mod init example.com/myapp        # start a module
go mod tidy                          # sync go.mod/go.sum with imports

go build ./...                       # build everything
go run ./cmd/myapp                   # build + run main package
go test -race -count=1 ./...         # race-detected tests
go vet ./...                         # lightweight static analysis
go fmt ./...                         # gofmt the tree
go install ./cmd/myapp               # install binary to $GOBIN
go doc fmt.Println                   # show documentation
```

`gofmt` is canonical — there is no debate about Go formatting. Run it on save. `go vet` catches a small set of common mistakes the compiler doesn't.

# Idioms

- **Errors are values.** Return `(T, error)` and check the error explicitly. Don't ignore errors.
- **Composition over inheritance.** Embed structs to inherit fields and methods; embed interfaces to compose behavior.
- **Accept interfaces, return concrete types.** Function parameters should be the smallest interface that satisfies the need; return values should be concrete so callers can use the full API.
- **Don't communicate by sharing memory; share memory by communicating.** Prefer channels for goroutine coordination when ownership transfers; use `sync` primitives when the shared state legitimately stays shared.
- **A nil slice/map is usable for reads but not for writes.** Read range/len on a nil slice returns 0; appending to nil works; indexing into a nil map panics on write.
- **`make` for slice/map/channel; `new` for `*T` of a zero value; struct literals for everything else.**
- **`defer` runs in LIFO order at function return — including on panic.**
- **Concurrency is not parallelism.** Concurrency structures a program; parallelism is one possible runtime behavior. See `cs-fundamentals` for the deep dive on goroutine/channel patterns and the Go memory model.

# Go Memory Model — Key Rules

- A `go` statement happens-before the goroutine's first action.
- Sending on a channel happens-before the corresponding receive completes.
- Closing a channel happens-before the receive that returns the zero value.
- `sync.Mutex.Lock()` happens-after the previous `Unlock()`.
- The exit of a goroutine is NOT guaranteed to happen-before any subsequent event in the program — use `sync.WaitGroup` or channels to synchronize completion.

(See `cs-fundamentals` for examples and patterns.)
