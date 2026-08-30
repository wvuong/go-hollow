# Porting Netflix Hollow (Java) to Go — Discussion Notes

Context: exploring what it would take to port [Netflix/hollow](https://github.com/Netflix/hollow)
— a Java library for disseminating large, periodically-updated, read-only in-memory
datasets — to Go. These are working notes from a research conversation, meant to seed
a Claude Code session that continues the design work.

## 1. Overview of issue categories

Porting isn't a line-for-line translation because a lot of Hollow's design is built
around JVM-specific behavior. Broad categories of friction:

- **GC model mismatch** — see §2–5 below, the deepest topic so far.
- **Bit-packing / low-level memory code** — `FixedLengthElementArray`-style bit-packed
  longs and ordinal-based references port conceptually, but need careful re-verification
  (Java's `>>>` unsigned shift has no Go equivalent; Go slices aren't capped at int32
  length the way Java arrays are, so some of Hollow's array-segmentation workarounds for
  >2GB structures can likely be simplified away).
- **Code generation instead of reflection** — Hollow derives schemas from POJOs via
  runtime reflection and emits typed accessor classes via an annotation-processor +
  JavaPoet pipeline (`HollowAPIGenerator`). Go's reflection is weaker/slower and
  idiomatic Go avoids leaning on it for hot paths — this needs to become a
  `go:generate`-based static generator (closer to protobuf-go/sqlc) rather than a port.
- **Type system / class hierarchy** — Hollow's internal state-engine classes use Java
  abstract classes, inheritance, and erased generics. Go has no inheritance — needs
  redesign around interfaces + struct embedding, not a class-for-class mapping.
- **Concurrency primitives** — `java.util.concurrent` (`AtomicReference` for lock-free
  hot-swap of the current state engine, thread pools) maps reasonably well to
  `atomic.Pointer[T]` + goroutines, but locking granularity/visibility needs
  re-verification, not assumption of equivalence.
- **String encoding** — real and non-trivial; see §6.
- **Error handling** — Hollow's API uses exceptions; idiomatic Go wants
  `(value, error)` returns throughout (producer-cycle failures, listener callbacks, etc).
- **Wire-format compatibility** — if Go and Java should interoperate on the same blobs,
  the binary format (varints, ordinal/delta encoding, hashing) must be replicated
  bit-for-bit — a real correctness/testing burden, best handled with differential
  testing against the Java implementation.
- **Surrounding ecosystem** — Explorer UI, History server, diff tooling, blob-storage
  plugins (S3), Netflix-internal integrations (metrics, announcement watchers) all need
  Go equivalents or are out of scope for a first cut.

## 2. Why Hollow fights the JVM's GC in the first place

Modeling each record as a Java object would create hundreds of millions/billions of
heap objects — each with object-header overhead, boxed primitives for nullable fields,
and (most importantly) a huge pointer graph for the collector to trace on every mark
phase.

Hollow's core trick: store the actual dataset in a handful of giant bit-packed
primitive arrays (`long[]`, segmented to work around Java's int32 array-index limit),
and identify records by **ordinal** (an integer index into those arrays) instead of by
object reference. The typed accessor objects application code touches (generated
`Movie`, `Actor`, etc. classes) are **flyweights**: cheap, short-lived wrapper objects
holding just an ordinal + a reference to the backing state engine, created on the fly
and immediately garbage. This plays directly into generational-GC assumptions: the
"real" data is a few huge, mostly-static arrays (cheap to trace — see §3); the churn is
a stream of tiny objects that die in young-gen, which generational collectors handle
almost for free.

**Object Longevity / "double snapshot"** is a separate mechanism layered on top, mostly
about *consistency* during a refresh rather than pause avoidance per se: when a
consumer applies a new delta or falls back to a full snapshot, in-flight requests
against the old state should keep seeing a coherent view. Hollow does this by pinning
the old state engine's arrays in memory for a grace period instead of dropping them
immediately — which is why that scenario roughly doubles heap usage for the transition
window (two full copies of the dataset, not two Java object graphs).

## 3. Pointer-free arrays and the GC "invisibility" nuance

Both JVM and Go collectors skip scanning the *contents* of primitive arrays
(`long[]` / `[]uint64`, `[]byte`) for pointers — that's the mechanism behind Hollow's
whole design, and it's not Go-specific: Java's arrays are typed at the class/array-kind
level (`long[]` vs `Object[]`), so the collector knows from that type info alone that
there's nothing to trace inside. Go's runtime does the analogous thing via "noscan"
memory spans.

But "invisible" overstates it for **both** runtimes:
- The array is still a tracked, live allocation — something must hold a reachable
  reference to it, or it gets reclaimed like anything else.
- Its size still counts toward the accounting that paces GC: Go's `GOGC` target formula
  uses live heap bytes (including noscan arrays); Java's G1 tracks old-gen occupancy
  against the Initiating Heap Occupancy threshold the same way.
- **Java-specific wrinkle: G1 "humongous objects."** Any object ≥ half a G1 region size
  (region size defaults up to 32MB, so objects in the tens-of-MB+ range) — which
  Hollow's backing arrays would routinely be — gets allocated directly into contiguous
  old-gen regions. Downside: the *allocation event itself* can force a GC cycle to start
  prematurely, independent of pointer-scanning. Upside: G1 generally does **not**
  relocate humongous objects during normal collection (they're only reclaimed as a
  whole, with real copying reserved for a rare last-resort full-GC case) — so for this
  specific allocation shape, G1 ends up behaving a lot like Go's collector in practice
  (mostly non-moving), though it's a special-cased optimization, not a blanket guarantee
  the way Go's non-moving design is. Note this is G1-specific — ZGC/Shenandoah are
  fully relocating/concurrent-compacting and would move even large objects.

## 4. Go's GC model vs the JVM's

Go's collector: **non-generational** (traces the whole live heap every cycle — no
cheap young-gen "nursery" the way HotSpot has), **non-moving** (mark-sweep, never
relocates/compacts — this structurally eliminates the "long pause compacting a huge
old-gen object graph" failure mode Hollow was originally designed around), and
**mostly concurrent**.

Implications for a port:
- The pointer-free/noscan optimization is a very direct match for Hollow's
  ordinal-array design — arguably a more natural fit than in Java, since it doesn't
  depend on JIT-level tricks. Go's own GC guide states plainly that "data structures
  that rely on indices over pointer values... may perform better," for exactly this
  reason.
- The flyweight/wrapper-object churn pattern does **not** transfer for free — Go has no
  cheap generational nursery to absorb it. A literal port of "construct a wrapper
  object per field access" could generate *more* GC pressure in Go than in Java. See §7
  for the idiomatic Go alternative (value types + escape-analysis discipline).
- Tuning model is different: Go's pacer targets heap size as roughly
  `live heap × (1 + GOGC/100)`, plus an optional hard `GOMEMLIMIT` ceiling — no
  equivalent to separately sizing young/old generations. Since Hollow's entire premise
  is a huge *live* (non-garbage) dataset, GOGC/GOMEMLIMIT need deliberate tuning from
  day one, not an afterthought. Over-aggressive GOMEMLIMIT risks GC "thrashing" (the
  runtime caps GC CPU at ~50% rather than guaranteeing the limit, which can stall
  progress instead of erroring).
- Go gives an option Java can't cleanly offer: since the bulk data is pointer-free
  anyway, it could be backed directly by `mmap`'d file memory / `unsafe` regions so it
  never counts as a Go heap allocation at all — sidestepping the "double heap during
  snapshot fallback" cost more cheaply than Java's Object Longevity mechanism, at the
  cost of manual lifetime management.
- Go 1.24 added a `weak` package (`weak.Pointer[T]`) — a possible low-level primitive
  for stale-state cleanup tracking, though much more limited than a generational
  collector's automatic reclamation.

## 5. Green Tea GC (Go 1.25 experimental → Go 1.26 default)

Real, currently-relevant development: **Green Tea** restructures Go's mark phase
around whole memory pages (8 KiB) instead of individual objects, processing objects
within a page sequentially (better cache locality) and using vector instructions
(AVX-512 on newer Intel/AMD) for batch pointer detection. Roughly 90% of Go GC cost is
in marking, and a large chunk of that is cache-miss stalls from scattered pointer-chasing
— Green Tea targets that directly. Claimed 10–40% reduction in GC CPU overhead, +~10%
more from vectorization on modern hardware. Opt-in via `GOEXPERIMENT=greenteagc` in Go
1.25; **enabled by default in Go 1.26** (already shipped as of this conversation);
opt-out (`GOEXPERIMENT=nogreenteagc`) expected to be removed in Go 1.27.

Doesn't change the fundamental algorithm (still non-generational, still non-moving,
GOGC/GOMEMLIMIT pacing unchanged) and doesn't help the noscan bulk arrays (already
skipped). It specifically narrows the gap on the flyweight-churn pain point (§4): if
some wrapper objects do escape to the heap, Green Tea meaningfully lowers the CPU cost
of marking through that kind of small-object graph. Worth targeting Go 1.25+/1.26 for
benchmarking; doesn't make "avoid unnecessary heap escapes" moot, but lowers the
penalty when it happens.

## 6. String encoding: Java UTF-16 vs Go UTF-8

Checked directly against Hollow's source
([`HollowObjectWriteRecord.java`](https://github.com/Netflix/hollow/blob/master/hollow/src/main/java/com/netflix/hollow/core/write/HollowObjectWriteRecord.java)):

```java
public void setString(String fieldName, String value) {
    if(value == null)  return;
    int fieldIndex = getSchema().getPosition(fieldName);
    validateFieldType(fieldIndex, fieldName, FieldType.STRING);
    ByteDataArray buf = getFieldBuffer(fieldIndex);
    for(int i=0;i<value.length();i++) {
        VarInt.writeVInt(buf, value.charAt(i));
    }
}
```

Hollow does **not** convert strings to UTF-8 bytes on the wire. It walks the Java
`String` one `char` at a time (`charAt` = a raw UTF-16 code unit) and VarInt-encodes
each code unit individually. `value.length()` is a UTF-16 code-unit count, not a
codepoint or byte count. For any character above the Basic Multilingual Plane (most
emoji, some CJK extensions, code point ≥ U+10000), Java represents it as a **surrogate
pair** — two 16-bit code units — so the loop emits two separate VarInts for one visual
character.

Go strings are UTF-8 byte slices with no code-unit indexing concept; a Go `rune`
(`int32`) holds a full code point directly, no surrogate splitting needed.

Design fork for the port:
- **Wire-compatible with existing Java-written blobs**: must reproduce Java's exact
  code-unit sequence — decode the Go string into runes, re-encode to UTF-16 code units
  (splitting anything above U+FFFF into a surrogate pair) via `unicode/utf16.Encode`/
  `Decode` (Go stdlib does the surrogate math, don't hand-roll it), *then* VarInt-encode
  each `uint16`. Can't just VarInt-encode raw UTF-8 bytes — different integer sequence
  entirely.
- **Clean-room, no interop needed**: free to VarInt-encode UTF-8 bytes directly —
  simpler, more idiomatic, but a genuinely different/incompatible wire format from
  upstream Hollow.
- **Known fidelity edge case**: Java doesn't validate well-formed surrogate pairs (rare
  malformed data is technically possible); `utf16.Decode` maps unpaired surrogates to
  U+FFFD rather than round-tripping exactly — a narrow place a Go consumer could diverge
  from a Java one on the same blob.

## 7. Flyweight objects in Go — design pattern

Goal: typed, ergonomic per-record access without materializing full objects, without
relying on a generational nursery to make wrapper-object churn free.

**Value types, not pointers/interfaces, returned from methods on the shared dataset:**

```go
type Movie struct {
    ordinal int32
    ds      *movieDataset // shared, long-lived — not allocated per call
}

func (d *movieDataset) Movie(ordinal int32) Movie {
    return Movie{ordinal: ordinal, ds: d}
}

func (m Movie) Title() string { return m.ds.readTitle(m.ordinal) }
func (m Movie) Year() int32   { return m.ds.readYear(m.ordinal) }
```

If the caller doesn't let it escape (no interface storage, no slice storage, no
`&m`, no goroutine handoff), Go's escape analysis can keep this entirely on the stack —
zero heap allocation. This is a *stronger* guarantee than Java gets in practice (HotSpot
EA is fragile across method/virtual-call boundaries, so Java Hollow's flyweights almost
always do heap-allocate and just rely on cheap young-gen collection).

Key rules to get that guarantee in practice:
- **Avoid interfaces on the hot path.** A common `HollowRecord` interface across record
  types will typically box values onto the heap. Prefer concrete generated types; use
  Go generics (`func Get[T any](ds *Dataset[T], ordinal int32) T`) for schema-generic
  code instead of interface dispatch.
- **Read fields lazily**, decoding straight from the bit-packed arrays per call (mirrors
  Java Hollow) — keeps the flyweight's size fixed regardless of schema width.
- **For hot loops, use a reusable cursor instead of a slice of flyweights** — a `[]Movie`
  of any real size will allocate. A mutable cursor (`bufio.Scanner`-style) reused across
  the whole loop has zero allocations for the traversal:

```go
type MovieCursor struct {
    ds      *movieDataset
    ordinal int32
}

func (c *MovieCursor) Next() bool    { c.ordinal++; return c.ordinal < c.ds.count }
func (c *MovieCursor) Title() string { return c.ds.readTitle(c.ordinal) }
```

- **Extend the pattern to nested/reference fields** (LIST/SET fields pointing at another
  type's ordinals) — return a cursor over ordinals, not a `[]Actor` of fully-built
  flyweights, so the no-allocation property holds recursively.
- **Keep indexes ordinal-typed**, not object-typed — `(ordinal int32, found bool)` from
  a primary-key/hash lookup, so misses (or found-but-unused hits) never pay for a
  flyweight construction.
- **Honest exception: strings.** Decoding a STRING field into a Go string is a real
  allocation every time (immutable, no in-place construction) — same fundamental cost
  Java has, just less visible under generational GC. Worth explicit caching for
  repeatedly-read string fields in hot loops; no escape-analysis trick fixes this one.
- Since the port needs its own code generator anyway (replacing Java's
  annotation-processor/JavaPoet pipeline), bake this shape (value-type accessors +
  cursor + ordinal-returning indexes) into the generator's templates once, rather than
  relying on each caller getting it right by hand. Verify actual escape behavior with
  `go build -gcflags="-m"` against generated code rather than assuming.

## Open threads / good next steps

- Decide early: **wire compatibility with Java Hollow blobs, or clean-room reimplementation?**
  This single decision cascades into the string-encoding approach (§6), the varint/hash
  algorithm work, and how much differential testing against the Java implementation is
  needed.
- Design the code-generator's input format (Go struct + tags via `go:generate`, versus
  a schema DSL) — this replaces Hollow's POJO-reflection approach entirely.
- Prototype the state-engine swap path (`atomic.Pointer[StateEngine]`) and decide whether
  something like Object Longevity is needed at all in Go, or whether a cheaper
  mmap-backed approach (§4) covers the same consistency requirement.
- Benchmark on Go 1.26 (Green Tea default) early, since some of the "avoid heap escapes
  at all costs" pressure is measurably lower than it would've been pre-Green Tea.
- Scope call on ecosystem tooling (Explorer/History UI, blob storage plugins) — probably
  out of scope for a v1 core engine port.

## Sources referenced during this discussion

- https://github.com/Netflix/hollow
- https://hollow.how/advanced-topics/
- https://github.com/Netflix/hollow/blob/master/hollow/src/main/java/com/netflix/hollow/core/write/HollowObjectWriteRecord.java
- https://github.com/Netflix/hollow/blob/master/hollow/src/main/java/com/netflix/hollow/core/memory/encoding/VarInt.java
- https://go.dev/doc/gc-guide
- https://go.dev/blog/greenteagc
- https://go.dev/doc/go1.26
- https://go.dev/doc/go1.24
- https://go.dev/blog/cleanups-and-weak
- https://docs.oracle.com/en/java/javase/25/gctuning/garbage-first-g1-garbage-collector1.html
