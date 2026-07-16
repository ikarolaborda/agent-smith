# Computer Science Fundamentals: Algorithms and Core Systems

This manual connects the abstractions used in application code to the
mechanisms underneath them. The central habit is to make costs, invariants,
failure modes, and concurrency assumptions explicit.

## 1. Reasoning About Algorithms

### Correctness before speed

An algorithm is correct when it meets its postcondition for every input
allowed by its precondition. Useful proof techniques include:

- **loop invariant**: a statement true before and after each iteration;
- **induction**: prove a base case and that one case implies the next;
- **exchange argument**: show that transforming an optimal solution toward the
  algorithm's choice does not make it worse;
- **cut or cycle property**: establish safe choices in graph algorithms;
- **reduction**: transform a known problem into another while preserving the
  answer.

Tests sample behavior; a proof explains why all valid cases work. In practice,
use both: reason about the invariant, then test boundaries and generated cases.

### Asymptotic analysis

Big-O gives an upper growth bound, Big-Omega a lower bound, and Big-Theta a
tight bound, ignoring constant factors and lower-order terms. Always state
what grows:

- n may be records, vertices, bytes, digits, or dimensions;
- time and space are separate;
- worst-case, average-case, expected, and amortized costs differ;
- an O(n) sequential scan can beat O(log n) pointer chasing for small n due to
  locality and constants.

**Amortized analysis** bounds a sequence of operations. Dynamic-array append is
usually O(1) amortized even though occasional resize operations are O(n).
Expected O(1) hash lookup depends on a suitable hash function and controlled
load; adversarial collisions can make it O(n).

### Common design techniques

- **Divide and conquer** splits independent subproblems, solves them, and
  combines results. Examples: merge sort and balanced search.
- **Dynamic programming** stores solutions to overlapping subproblems. Define
  state, recurrence, base cases, evaluation order, and reconstruction.
- **Greedy algorithms** make locally optimal choices. They require a proof of
  the greedy-choice property; intuition is not enough.
- **Backtracking** explores a search tree and prunes invalid partial solutions.
- **Branch and bound** also prunes solutions whose best possible outcome
  cannot beat the current best.
- **Randomization** can improve expected performance or avoid adversarial
  structure; make reproducibility and cryptographic requirements explicit.

## 2. Data Structures

Choose a representation from operations and access patterns, not familiarity.

### Arrays and dynamic arrays

- contiguous storage gives constant-time indexing and strong cache locality;
- insertion or deletion in the middle costs O(n);
- growth can invalidate references and temporarily require extra memory;
- a slice or view usually shares backing storage, so mutation and lifetime must
  be understood.

### Linked structures

Linked lists provide constant-time insertion or removal when the node is
already known, but locating it remains O(n). Pointer overhead and poor locality
often make lists slower than arrays. Intrusive lists trade encapsulation for
fewer allocations.

Stacks model last-in-first-out work such as parsing and depth-first search.
Queues model first-in-first-out work such as breadth-first search and task
processing. A deque supports efficient operations at both ends. A bounded
queue is also a backpressure mechanism.

### Hash tables

A hash table maps a key to a bucket. Correct design requires:

- equality and hashing that agree;
- stable keys while stored;
- a load-factor and resize policy;
- collision handling by chaining or open addressing;
- defense against attacker-controlled collision patterns where relevant.

Hash iteration order should not be treated as stable unless the contract says
so. Hash tables do not naturally support range scans or ordered traversal.

### Trees

- A binary search tree orders keys; without balancing it can degrade to a
  linked list.
- AVL and red-black trees maintain logarithmic height with different
  rebalancing trade-offs.
- B-trees and B+ trees use high fan-out to minimize storage-page accesses and
  underpin many database indexes.
- Tries index by key prefixes and trade memory for prefix operations.
- Interval and segment trees answer range or overlap queries.

### Heaps

A binary heap provides O(1) access to the minimum or maximum and O(log n)
insertion/removal. It is ideal for priority queues but not arbitrary search.
Heap order is weaker than full sorting.

### Graphs

Represent a sparse graph with adjacency lists and a dense graph, when
appropriate, with a matrix. Important graph distinctions are:

- directed versus undirected;
- weighted versus unweighted;
- cyclic versus acyclic;
- connected versus disconnected;
- simple graph versus multiple edges.

Breadth-first search finds shortest paths in unweighted graphs. Dijkstra
requires non-negative edge weights. Bellman-Ford handles negative weights and
detects reachable negative cycles. Topological ordering exists only for a
directed acyclic graph. Minimum spanning trees connect all vertices cheaply;
they do not solve shortest paths between arbitrary pairs.

### Probabilistic structures

A Bloom filter can say “definitely absent” or “possibly present.” It has false
positives but no false negatives if entries are never removed. It is useful as
a prefilter, never as the authoritative authorization or existence check.

## 3. Numerical and Representation Hazards

- Integers have finite ranges; define overflow behavior and validate size
  calculations before allocation.
- Floating-point values approximate real numbers. Do not compare derived
  floats for exact equality; choose a domain-appropriate tolerance.
- Decimal currency should use integer minor units or an exact decimal type,
  with explicit rounding rules.
- Text is encoded bytes. Unicode code points, grapheme clusters, and bytes are
  different units; indexing a string may not index a user-perceived character.
- Endianness, alignment, and padding matter at binary, network, and foreign
  function boundaries.
- Serialization formats need versioning, length limits, recursion limits, and
  validation before allocation or use.

## 4. Processes, Threads, and Scheduling

A **program** is executable code; a **process** is an executing instance with a
virtual address space and OS-managed resources. A **thread** is a schedulable
execution stream within a process. User-space tasks, fibers, and coroutines are
scheduled by a language runtime and may share fewer kernel threads.

### Process boundaries

Processes provide fault and address-space isolation, but communication crosses
an IPC boundary. Common mechanisms include pipes, sockets, shared memory,
signals, and files. Shared memory is fast but requires explicit synchronization
and lifetime management.

A system call crosses from user mode into the kernel. It is more expensive
than an ordinary call and can block, copy data, or schedule another thread.
Batch I/O and avoid unnecessary crossings, but measure before optimizing.

### Scheduling and concurrency

Schedulers balance fairness, responsiveness, throughput, and priority.
Oversubscribing CPU-bound threads increases context switching without adding
capacity. I/O-bound concurrency can be much higher, but still needs bounds on
memory, file descriptors, sockets, and downstream load.

Concurrency defects:

- **race condition**: correctness depends on an uncontrolled ordering;
- **data race**: unsynchronized conflicting memory accesses;
- **deadlock**: participants wait in a cycle and cannot progress;
- **livelock**: participants run but continually prevent progress;
- **starvation**: a participant is indefinitely denied service.

Prevent deadlock with a global lock order, short critical sections, no
unbounded external calls while holding a lock, and cancellation/timeouts where
semantics allow. Prefer ownership and message passing when they simplify state,
not because locks are inherently wrong.

### Memory models

Compilers and CPUs may reorder operations while preserving single-thread
behavior. Synchronization establishes happens-before relationships that make
writes visible across threads. Volatile is not a general substitute for locks
or atomics. Use the language's documented memory model and synchronization
primitives.

## 5. Virtual Memory and Resource Use

Each process sees virtual addresses that the OS maps to physical pages or
backing storage. This enables isolation, sharing, memory-mapped files, sparse
address spaces, and copy-on-write.

### Address translation

- Page tables map virtual pages to physical frames.
- A translation lookaside buffer caches recent translations.
- A page fault occurs when a translation needs OS handling; it may be cheap,
  or may require storage I/O.
- Large pages reduce translation overhead but can waste memory and complicate
  allocation.
- Copy-on-write initially shares pages, then copies a page when one party
  writes.

Resident set size is the physical memory currently resident for a process.
Virtual size is not the same as committed or resident memory. Memory-mapped
model files can consume address space without all pages being resident.

### Stack, heap, and lifetime

Stacks hold call frames and usually have structured lifetimes. Heaps hold
dynamically sized or longer-lived objects. Allocation cost includes allocator
metadata, fragmentation, cache behavior, and garbage-collection work.

Memory safety defects include out-of-bounds access, use after free, double
free, uninitialized use, and invalid lifetime sharing. Garbage collection
prevents some of these but does not prevent leaks, races, unbounded retention,
or resource exhaustion.

### Locality and caches

CPU caches operate in cache-line units. Sequential access usually performs
better than pointer chasing. Two threads modifying independent values on the
same cache line can suffer false sharing. Optimize data layout only with
profiles and realistic workloads.

### I/O

Buffered I/O reduces syscall frequency. Direct or asynchronous I/O can help
specialized workloads but increases alignment and completion complexity.
Successful write calls do not necessarily mean durable storage; durability may
require filesystem and device synchronization, and rename guarantees depend
on the filesystem.

## 6. Files, Sockets, and Protocols

A file descriptor or handle is a bounded kernel resource. Close it on all
paths. Guard against:

- path traversal and symlink races;
- unbounded file or archive expansion;
- partial reads and writes;
- unexpected blocking;
- filesystem-specific case, rename, and locking behavior.

TCP provides an ordered byte stream, not messages. One send does not correspond
to one receive. Applications need framing, size limits, deadlines, and
backpressure. UDP provides datagrams without delivery, ordering, or duplicate
suppression.

Higher-level protocols define more than wire syntax. HTTP includes caching,
method semantics, intermediaries, authentication, and connection reuse.
Implement against the protocol specification and tested libraries rather than
assuming a happy-path exchange.

## 7. Database Foundations

### Data modeling

Identify entities, relationships, cardinality, identifiers, invariants, and
access patterns. Normalization reduces update anomalies:

- first normal form gives attributes atomic values for the chosen model;
- second and third normal forms remove dependencies on only part of a key and
  on non-key attributes;
- denormalization can improve a measured read path but creates synchronization
  responsibility.

Use database constraints for invariants the database can authoritatively
enforce: non-null, uniqueness, foreign keys, and check constraints. Application
validation improves feedback but does not replace concurrency-safe constraints.

### Transactions and ACID

- **Atomicity**: all transaction effects commit or none do.
- **Consistency**: transactions preserve declared invariants.
- **Isolation**: concurrent execution has defined visibility and interference.
- **Durability**: committed effects survive the system's promised failure
  model.

Isolation is not simply on or off. Phenomena include dirty reads, non-repeatable
reads, phantoms, lost updates, write skew, and serialization failures. Database
names for levels are less useful than the precise guarantees in that engine's
documentation.

MVCC keeps multiple row versions so readers and writers can overlap. It still
requires cleanup and may allow anomalies below serializable isolation. Locks,
optimistic version checks, and serializable retries are complementary tools.

### Logging and recovery

A write-ahead log records recovery information before modified data pages are
considered durable. Checkpoints bound recovery work. Replication does not
replace backups: accidental deletion and logical corruption can replicate too.
Test point-in-time recovery, not only backup creation.

### Indexes

An index trades write cost and storage for faster access.

- B-tree indexes support equality, ordered ranges, prefixes of composite keys,
  and ordered scans.
- Hash indexes specialize in equality.
- Inverted indexes map terms to documents for text and containment queries.
- Spatial indexes accelerate geometric relationships.
- LSM trees buffer writes and compact sorted runs, favoring write throughput at
  the cost of compaction and read amplification.

Composite index column order follows access patterns. An index may be ignored
when selectivity is low, statistics are stale, an expression changes the
indexed value, or the estimated scan is cheaper. Inspect the query plan and
measure with production-like distributions. More indexes slow writes and
increase maintenance.

### Query execution

The optimizer chooses scans, join orders, and join algorithms from statistics
and a cost model. Nested-loop, hash, and merge joins fit different input sizes,
ordering, memory, and index conditions. Avoid per-row application queries when
one set-oriented query or bounded batch expresses the work.

## 8. Compilers and Language Runtimes

### Compilation pipeline

A typical compiler performs:

1. lexical analysis into tokens;
2. parsing into a syntax tree;
3. name resolution and semantic/type checking;
4. lowering into one or more intermediate representations;
5. optimization while preserving observable semantics;
6. code generation;
7. linking and loading.

Real implementations may interleave or repeat stages. A parser recognizes
grammar; semantic analysis determines whether the program is meaningful under
the language rules.

### Linking and loading

Static linking copies required code into the executable. Dynamic linking
resolves shared-library symbols at load or runtime. Application binary
interfaces specify calling conventions, object layout, symbol naming, and
binary types. Source compatibility does not imply ABI compatibility.

### AOT, interpretation, and JIT

Ahead-of-time compilation moves work before execution and produces predictable
startup. Interpreters execute a representation directly. Just-in-time
compilers observe runtime behavior and compile hot paths, trading warm-up and
memory for specialization. Many runtimes combine all three approaches.

### Runtime memory management

- Reference counting reclaims promptly when counts reach zero but needs a
  strategy for cycles and adds update overhead.
- Tracing collectors discover reachable objects and reclaim the rest.
- Generational collectors optimize for short-lived objects.
- Concurrent collectors reduce pauses but consume CPU and require barriers.

Latency, throughput, memory overhead, and predictability are trade-offs. A
managed runtime still requires closing non-memory resources explicitly.

### Type systems

Static versus dynamic and strong versus weak typing describe different axes.
Types can document contracts, rule out states, guide optimization, and improve
tooling. Escape hatches such as reflection, casts, foreign interfaces, and
dynamic input reintroduce runtime validation requirements.

## 9. Distributed Systems

A distributed system must remain understandable when messages are delayed,
duplicated, reordered, or lost; nodes pause or restart; clocks disagree; and
partitions divide the network.

### Safety and liveness

- **Safety** means something bad never happens, such as two leaders committing
  conflicting values for the same log position.
- **Liveness** means something good eventually happens, such as a valid request
  completing when required conditions recover.

A timeout is evidence of uncertainty, not proof that the remote operation did
not happen. Retrying a non-idempotent request can duplicate effects. Use
idempotency keys, operation identifiers, deduplication state, or a naturally
idempotent design.

### Time and ordering

Wall clocks can jump or drift. Use monotonic time for durations. Logical clocks
represent causal ordering without claiming real time. A total order can be
constructed for coordination but does not mean events physically occurred in
that order.

### CAP precisely

CAP concerns behavior during a network partition: a system cannot guarantee
both linearizable consistency and availability for every request across the
partition. Outside partitions, systems still trade latency, consistency,
durability, and cost. “CA database” without stating the partition behavior and
consistency model is not a useful design description.

### Replication and consensus

Replication improves read capacity and fault tolerance but creates lag and
conflict questions. Leader-follower systems serialize writes through a leader;
multi-leader and leaderless systems accept more concurrency and require
conflict handling.

Consensus protocols let non-faulty nodes agree despite failures under stated
assumptions. They do not make the network reliable or remove application-level
transactions. Quorum arithmetic alone is insufficient; protocol rules,
membership changes, persistence, and failure assumptions matter.

### Partitioning

Sharding distributes data by range, hash, directory, or domain. A good key
balances load while supporting queries and tenant isolation. Hot keys, global
indexes, cross-shard transactions, rebalancing, and backup consistency are
first-class design concerns.

### Messaging semantics

“Exactly once” is always scoped to a protocol and failure model. End-to-end
business effects usually require idempotent consumers or transactions tying
message progress to state change.

- At-most-once may lose messages but avoids broker redelivery.
- At-least-once redelivers and requires duplicate-safe handling.
- Ordering is normally guaranteed only within a partition or key.
- A dead-letter queue stores failures; it is not a resolution strategy.

Bound queues and reject, shed, or slow work before memory is exhausted.
Backpressure must propagate toward the producer.

### Distributed data changes

Use a local transaction whenever one authoritative database can own an
invariant. Two-phase commit provides atomic coordination but can reduce
availability and complicate recovery. Sagas coordinate compensatable local
transactions; compensation is a new business action, not time reversal. The
transactional outbox prevents a local state change from diverging from its
event publication.

## 10. Systems Review Checklist

- What are the input bounds and asymptotic costs?
- Which invariant proves correctness?
- What representation matches the dominant operations and locality?
- Who owns each resource and its lifetime?
- What happens on partial read, partial write, cancellation, or retry?
- Which concurrency ordering is required and how is it established?
- Which database isolation anomalies would violate the business invariant?
- What is authoritative and what is derived or cached?
- What consistency and failure model is promised across nodes?
- How are overload, backpressure, recovery, and observability handled?
- Which assumptions have been measured on realistic data and hardware?

## Stable and Canonical References

- **[Canonical algorithms text]** Cormen, Leiserson, Rivest, and Stein,
  Introduction to Algorithms, [MIT Press](https://mitpress.mit.edu/9780262046305/introduction-to-algorithms/).
- **[Operating-system interface standard]** The Open Group,
  [POSIX.1-2024](https://pubs.opengroup.org/onlinepubs/9799919799/).
- **[Primary language memory model]** The Go Project,
  [The Go Memory Model](https://go.dev/ref/mem).
- **[Primary transport standard]** IETF,
  [RFC 9293: Transmission Control Protocol](https://www.rfc-editor.org/rfc/rfc9293).
- **[Primary application protocol standard]** IETF,
  [RFC 9110: HTTP Semantics](https://www.rfc-editor.org/rfc/rfc9110).
- **[Official database documentation]** PostgreSQL,
  [Concurrency Control](https://www.postgresql.org/docs/current/mvcc.html) and
  [Indexes](https://www.postgresql.org/docs/current/indexes.html).
- **[Official database documentation]** SQLite,
  [Isolation In SQLite](https://www.sqlite.org/isolation.html).
- **[Primary compiler IR specification]** LLVM Project,
  [LLVM Language Reference Manual](https://llvm.org/docs/LangRef.html).
- **[Primary paper]** Leslie Lamport,
  [Time, Clocks, and the Ordering of Events in a Distributed
  System](https://lamport.azurewebsites.net/pubs/time-clocks.pdf).
- **[Primary paper]** Leslie Lamport,
  [Paxos Made Simple](https://lamport.azurewebsites.net/pubs/paxos-simple.pdf).
- **[Primary paper]** Diego Ongaro and John Ousterhout,
  [In Search of an Understandable Consensus
  Algorithm](https://raft.github.io/raft.pdf).
- **[Primary paper]** Seth Gilbert and Nancy Lynch,
  [Brewer's Conjecture and the Feasibility of Consistent, Available,
  Partition-Tolerant Web Services](https://groups.csail.mit.edu/tds/papers/Gilbert/Brewer2.pdf).
- **[Primary production design paper]** Amazon,
  [Dynamo: Amazon's Highly Available Key-value
  Store](https://www.allthingsdistributed.com/files/amazon-dynamo-sosp2007.pdf).

Primary specifications define exact semantics. Textbooks and papers provide
models; implementation documentation defines the behavior of a particular
runtime, operating system, database, or protocol version.
