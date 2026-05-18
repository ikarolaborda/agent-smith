# CS Fundamentals — Concurrency, Parallelism, Memory, Mutex

Sources: context7 `/websites/go_dev_ref_spec` (Go spec), `/websites/php_net_manual_en` (PHP manual).

## Concurrency vs Parallelism

- **Concurrency** is the composition of independently executing tasks. It is about STRUCTURE: a program that handles many things at once.
- **Parallelism** is the simultaneous execution of multiple computations on multiple cores. It is about EXECUTION.

A program can be concurrent without being parallel (a single-core machine running many goroutines). It can also be parallel without being explicitly concurrent (vectorized math on a GPU). Most server software needs concurrency; parallelism is a runtime gift when cores are available.

Rob Pike: "Concurrency is not parallelism. Concurrency is a property of the program. Parallelism is a property of the execution."

## Race Conditions, Deadlock, Livelock, Starvation

- **Race condition** — two or more operations on shared state where the final outcome depends on interleaving. Always a bug.
- **Data race** — a specific kind of race: two memory accesses to the same location with at least one being a write, no synchronization between them. In Go and C++, data races are undefined behavior.
- **Deadlock** — two or more workers waiting on each other's locks; nobody progresses. Detected by Go runtime when ALL goroutines are blocked.
- **Livelock** — workers keep changing state in response to each other but make no progress (mutual retry storms without backoff).
- **Starvation** — one worker is perpetually denied access to a resource because others are constantly granted it. RWMutex with continuous reads can starve writers.

## Mutex, RWMutex, Semaphore, Atomic

- **Mutex (mutual exclusion lock)** — at most one holder at a time. Pair every Lock with a deferred Unlock.
- **RWMutex** — many readers OR one writer. Use when reads vastly outnumber writes; otherwise a plain Mutex is faster (fewer atomic ops).
- **Semaphore** — a counter; permits up to N holders. Useful for bounded concurrency (e.g. cap at 8 outbound HTTP calls).
- **Atomic** — single-word read/modify/write CPU instructions (compare-and-swap, fetch-add). Faster than mutex for counters and flags, but composes poorly: you can update one counter atomically; you can't atomically update two counters together.

Rule of thumb: reach for atomic when the protected state is one machine word; reach for Mutex when it's anything more.

## Memory Management

- **Stack** — per-goroutine / per-fiber / per-thread call frame. Cheap, no GC, lifetime tied to the call. Local variables that don't escape live here.
- **Heap** — process-wide, GC'd (Go) or refcounted (PHP). Anything whose lifetime outlives the call goes here.
- **Escape analysis** — the compiler decides for each variable whether stack or heap. In Go you can see decisions via `go build -gcflags="-m"`.
- **GC pause** — modern Go GC is concurrent and mostly non-stop-the-world; pauses are typically sub-millisecond. PHP releases per-request memory at request end; long-running PHP workers (Swoole, RoadRunner) need explicit object cleanup to avoid leaks.

# Go Best Practices

## Goroutines

A `go` statement starts a function call as an independent concurrent thread of control, or goroutine, within the same address space.

```go
go handleConn(conn)
```

The expression must be a function or method call (no parentheses around it). Program execution does not wait for the call to complete; it returns immediately and the goroutine runs independently.

Rules:
- A goroutine without a clear exit condition is a leak.
- Always pass a `context.Context` to long-running goroutines so callers can cancel them.
- Don't share mutable state across goroutines without synchronization; the Go memory model only guarantees ordering through channels, sync primitives, or `sync/atomic`.

## Channels

Channels are typed conduits for values. Unbuffered channels synchronize sender and receiver; buffered channels decouple them up to a capacity.

```go
ch := make(chan int)           // unbuffered
buf := make(chan int, 64)      // buffered

go func() { ch <- compute() }()
result := <-ch
```

Sending on `nil` blocks forever; receiving from `nil` blocks forever. Sending on a closed channel panics; receiving from a closed channel returns the zero value with `ok == false`.

```go
v, ok := <-ch
if !ok {
    // channel was closed
}
```

Only the SENDER should close a channel, and only when no more values are coming. Closing a channel from multiple senders is a bug.

## Select

A `select` statement chooses among multiple channel operations. If several can proceed, one is selected at random.

```go
select {
case v := <-incoming:
    handle(v)
case outgoing <- payload:
    log("sent")
case <-time.After(time.Second):
    log("timeout")
case <-ctx.Done():
    return ctx.Err()
}
```

A `default` case makes the select non-blocking — it fires when nothing else is ready.

## The sync Package

- `sync.Mutex` — basic mutual exclusion.
- `sync.RWMutex` — multiple readers OR one writer.
- `sync.WaitGroup` — wait for a set of goroutines to finish.
- `sync.Once` — guarantee a function runs exactly once across goroutines (lazy init).
- `sync.Pool` — temporary object pool to reduce GC pressure; never store objects you depend on (Pool can drop anything any time).
- `sync.Map` — concurrent map for read-heavy workloads where keys stabilize; otherwise prefer `map + RWMutex`.

```go
var mu sync.Mutex
var count int

func inc() {
    mu.Lock()
    defer mu.Unlock()
    count++
}
```

Always `defer mu.Unlock()` immediately after `mu.Lock()`. Skipping the defer because "the function is small" is the #1 source of forgotten unlocks during later edits.

## sync/atomic

For single-word counters and flags, `sync/atomic` beats Mutex by 10-100×.

```go
var requests atomic.Int64

func observe() {
    requests.Add(1)
}

func snapshot() int64 {
    return requests.Load()
}
```

`atomic.Value` lets you publish a pointer atomically; readers see either the old or new value, never a torn one. Useful for hot-reload config.

## GOMAXPROCS

`runtime.GOMAXPROCS(n)` sets the maximum number of OS threads that can execute user-level Go code simultaneously. The default since Go 1.5 is the number of logical CPUs. In containers, since Go 1.25+ the runtime auto-detects the cgroup CPU quota.

## The Race Detector

```sh
go test -race ./...
go run -race main.go
go build -race -o myapp
```

The race detector instruments memory accesses at compile time and reports any data race observed at runtime. It catches real concurrency bugs you can't easily reason about. Always run tests under `-race` in CI for any non-trivial concurrent code.

Performance hit is ~2-20× during the run, so don't ship race-instrumented binaries.

## Go Memory Model Summary

- A `go` statement happens-before the start of the goroutine's execution.
- The exit of a goroutine is NOT guaranteed to happen-before any event in the program. Use `sync.WaitGroup` or channels to synchronize.
- Sending on a channel happens-before the corresponding receive completes.
- The k-th receive on a channel of capacity C happens-before the (k+C)-th send completes.
- The closing of a channel happens-before a receive that returns the zero value.
- `sync.Mutex.Lock()` happens-after the previous `Unlock()`.
- A successful `sync/atomic` operation happens-after all preceding operations on the same address.

## Common Go Concurrency Patterns

- **Pipeline** — chain goroutines via channels; each stage transforms and forwards.
- **Fan-out / fan-in** — many workers read from one input channel; a single goroutine merges their outputs into one output channel.
- **Worker pool** — N goroutines reading jobs from a buffered channel; close the jobs channel to signal "no more work".
- **Bounded concurrency** — buffered channel of capacity N used as a semaphore: `sem <- struct{}{}` to acquire, `<-sem` to release.
- **Context cancellation** — every blocking goroutine selects on `ctx.Done()` so callers can abort.

```go
sem := make(chan struct{}, 8) // up to 8 concurrent
for _, u := range urls {
    sem <- struct{}{}
    go func(u string) {
        defer func() { <-sem }()
        fetch(u)
    }(u)
}
```

# PHP Best Practices

## PHP's Concurrency Model

PHP is, by default, a SINGLE-THREADED, SHARED-NOTHING request runtime. Each request runs in isolation; nothing is shared with other requests. This makes PHP trivially safe against most concurrency bugs at the request level — and the corollary is that you cannot do in-process parallelism the way Go does.

For concurrency inside one request, PHP 8.1+ offers **Fibers** (cooperative, single-threaded). For cross-request parallelism, PHP uses processes (`pcntl_fork`) or extensions like **parallel** (true threads), **Swoole** (event loop + coroutines), and **RoadRunner** (long-running workers via an external Go-based supervisor).

## Fibers (PHP 8.1+)

A Fiber is a piece of code that can suspend itself and be resumed later from outside. It's cooperative — Fibers do not preempt; you must call `Fiber::suspend()` explicitly.

```php
<?php
$fiber = new Fiber(function (): void {
    $param = Fiber::suspend('fiber');
    echo 'Value used to resume fiber: ' . $param . PHP_EOL;
});

$res = $fiber->start();
echo 'Value from fiber suspending: ' . $res . PHP_EOL;
$fiber->resume('test');
```

Lifecycle:
- `$fiber->start(...$args)` — begin execution; returns the first suspended value.
- `Fiber::suspend($value)` — pause and yield `$value` to whoever started or resumed us.
- `$fiber->resume($value)` — continue the fiber; the value becomes the return value of `Fiber::suspend()`.
- `$fiber->throw($exception)` — resume by throwing inside the fiber.
- `$fiber->getReturn()` — final return value after the fiber terminates.

Fibers are the foundation that lets PHP have async libraries like Revolt, AmPHP v3, and ReactPHP v3 expose synchronous-looking code over an event loop.

## Processes — `pcntl_fork()`

For true parallelism on POSIX systems, `pcntl_fork()` clones the current process. The child gets a return value of `0`; the parent gets the child's PID.

```php
<?php
$pid = pcntl_fork();
if ($pid === -1) {
    exit('fork failed' . PHP_EOL);
}
if ($pid === 0) {
    /* child */
    doWork();
    exit(0);
}
/* parent */
pcntl_waitpid($pid, $status);
```

Use cases: long batch jobs, CLI tools, supervisor-style workers. NOT for typical web requests behind FPM (FPM already runs multiple processes).

Cross-process state requires shared memory (`shmop_*`, APCu) or a real queue/database. Forked processes share file descriptors at the moment of the fork but have independent memory after.

## Bounded Parallelism Pattern with `pcntl_fork`

Cap the number of concurrent children using `pcntl_wait()` to drain when the cap is reached.

```php
<?php
declare(strict_types=1);

$maxParallel = 5;
$running = 0;
$tasks = range(1, 50);

foreach ($tasks as $task) {
    if ($running >= $maxParallel) {
        pcntl_wait($status);
        $running--;
    }
    $pid = pcntl_fork();
    if ($pid === -1) {
        exit('fork failed');
    }
    if ($pid === 0) {
        executeTask($task);
        exit(0);
    }
    $running++;
}
while (pcntl_waitpid(0, $status) !== -1) {
    /* drain remaining children */
}
```

## Shared Memory and APCu

PHP processes do not share heap memory. Cross-process state lives in one of:
- `shmop_*` — raw shared memory blocks identified by IPC keys.
- `apcu_store` / `apcu_fetch` — userland cache, fast in-process.
- A real store: Redis, Memcached, the database.

APCu is the easiest stand-in for cross-fiber-cross-process state inside one FPM pool. Across machines, Redis or Memcached.

```php
<?php
apcu_store('user:42:hits', 0);
apcu_inc('user:42:hits');
$hits = apcu_fetch('user:42:hits');
```

## Opcache

`opcache` is mandatory in production. It caches compiled bytecode in shared memory across FPM workers so each request reuses the parsed/compiled forms.

`php.ini` highlights:
- `opcache.enable=1`
- `opcache.memory_consumption=256`
- `opcache.max_accelerated_files=20000`
- `opcache.validate_timestamps=0` in production (and explicit `opcache_reset()` on deploy).

## Mutex-like Patterns in PHP

PHP itself has no Mutex primitive in the standard library (because each request is independent). When you need cross-request mutual exclusion, options are:
- **Database row lock** — `SELECT ... FOR UPDATE` inside a transaction.
- **Redis SETNX / SET NX EX** — atomic "set if not exists" with TTL for distributed locks; pair with a fencing token to survive process death.
- **File lock** — `flock($fp, LOCK_EX)`. Fine for single-machine CLI tools, awful for clustered web apps.
- **Symfony Lock component** — abstraction over Redis, Postgres, Zookeeper, etc.

## Swoole and Async Frameworks

`Swoole` is a PECL extension that turns PHP into an event-driven runtime with coroutines and true multi-threaded workers.

```php
<?php
Swoole\Runtime::enableCoroutine();

\Co\run(function () {
    go(function () { fetchUrl('https://a.example'); });
    go(function () { fetchUrl('https://b.example'); });
});
```

Swoole coroutines are cooperative and run on Swoole's event loop. They look synchronous, scale to tens of thousands per process, and integrate with PDO/cURL via the runtime hook.

ReactPHP and AmPHP build on Fibers (in v3) to provide similar async-looking sync code without requiring a PECL extension.

## The `parallel` Extension

`ext-parallel` provides actual OS threads with copy-by-value isolation between them — closer to Go's model than any other PHP option. Only available on ZTS (Zend Thread Safe) builds.

```php
<?php
$runtime = new \parallel\Runtime();
$future  = $runtime->run(function () {
    return expensiveComputation();
});

$result = $future->value(); // blocks until done
```

Use when you genuinely need parallel CPU work in PHP and `pcntl_fork` is too heavy.

# Cross-Language Best Practices

- **Always have a cancellation story.** Go: `context.Context`. PHP-async: pass a `Cancellation` token through every awaitable.
- **Avoid global mutable state.** It's the root cause of nearly every data race.
- **Prefer message passing over shared memory.** Go: channels. PHP-Swoole: channels (`Co\Channel`). When you must share state, document the lock that protects it.
- **Make every concurrent handler idempotent.** Retries, restarts, and at-least-once delivery WILL deliver duplicate work in production.
- **Bound your concurrency.** Unbounded goroutine or fork explosions are the most common production outage. Use semaphores, worker pools, rate limiters.
- **Test under load + race-detector / opcache.** Tier-1 verifiers catch the bugs that careful reasoning misses.
- **Measure before optimizing.** Atomic-vs-mutex, channels-vs-locks, fibers-vs-processes — the right choice depends on the actual workload.
