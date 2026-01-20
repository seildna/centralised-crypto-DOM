# Go DOM Architecture (Agent Runbook)

This file is the canonical interface for building, validating, and troubleshooting the DOM.
Follow it every time you implement or change DOM components.

---

## Operating Principles
- Correctness before speed: a fast wrong book is useless.
- Deterministic behavior: same inputs -> same book state.
- Exchange rules are first-class: do not normalize away sequencing semantics.
- Low latency is achieved by incremental updates, not by skipping validation.

---

## System Goals (Non-Negotiable)
- Maintain accurate local L2 books per exchange + per symbol.
- Aggregate into a unified DOM with deterministic output.
- Auto-resync on gaps or checksum mismatch.
- Keep hot path allocation-free where possible.
- Provide a minimal operator surface for health, debug, and tests.

---

## High-Level Modules
```
cmd/dom/                 # main entrypoint
internal/config/         # env/config parsing
internal/schema/         # normalized types
internal/exchange/       # adapter interfaces + common utils
internal/exchange/*/     # per-exchange adapters
internal/book/           # per-exchange order book
internal/dom/            # central aggregator
internal/health/         # liveness, gap detection, resync
internal/metrics/        # optional stats
internal/testutil/       # fixtures, golden books, fuzz helpers
```

---

## Normalized Schema (L2)
All adapters emit `L2Update` into the pipeline:

```
type L2Update struct {
    Exchange   string
    Symbol     string
    Timestamp  int64         // exchange timestamp (ms)
    RecvTime   int64         // local receive time (ms)
    Seq        int64         // exchange sequence/update id if available
    IsSnapshot bool
    Bids       []PriceLevel
    Asks       []PriceLevel
    Checksum   uint32        // optional exchange-provided checksum
    DepthCap   int           // optional: subscribed depth limit, 0 = full depth
}

type PriceLevel struct {
    Price string           // exact decimal as provided
    Size  string           // exact decimal as provided
}
```

Precision:
- Keep price/size as strings in adapters.
- Convert to fixed-point int64 at book storage time.
- Never store float64 in the book.

---

## Adapter Contract (Must Implement)
```
type Adapter interface {
    Name() string
    Symbols() []string
    Connect(ctx context.Context) error
    Subscribe(ctx context.Context, symbols []string) error
    Updates() <-chan L2Update
    Errors() <-chan error
    Close() error
}
```

Adapter responsibilities (exact):
1) Establish WS connection.
2) Subscribe to order book channel for each symbol.
3) Buffer updates until snapshot is applied.
4) Enforce exchange sequencing rules (see below).
5) Emit `L2Update` in correct order.
6) Emit errors on gap/parse/sequence violations.

---

## Exchange Sequencing Rules (Do Not Deviate)

### Binance Spot/Futures
- Buffer events after WS connect.
- Fetch snapshot (REST) and get `lastUpdateId`.
- If `lastUpdateId < firstEvent.U`, refetch snapshot.
- Discard buffered events where `u <= lastUpdateId`.
- First processed event must satisfy: `U <= lastUpdateId+1 <= u`.
- Apply events in order:
  - if `u < currentUpdateId`, ignore
  - if `U > currentUpdateId+1`, gap => resnapshot
  - otherwise apply and set `currentUpdateId = u`
- Snapshot depth is limited; levels outside snapshot are unknown until updated.
- If `pu` is provided (derivatives), require `pu == previous u`; otherwise resnapshot.

### Bybit
- Use `type` field (snapshot/delta) when provided.
- If `u == 1`, treat as snapshot and overwrite local book.
- If a new `snapshot` arrives, overwrite local book (Bybit may resend snapshots).
- For delta: apply in message order; use `seq` only for cross-stream ordering.
- If repeated snapshot arrives (no change), overwrite and continue.
- `size == 0` removes a level.
- For Level 1, a snapshot may be pushed again when there is no change; `u` may be unchanged.

### Coinbase Exchange / Prime (Level2)
- First message is snapshot. Store it.
- Apply updates in arrival order; if exchange provides a monotonic sequence (Prime `sequence_num`), enforce it and resync on gaps.
- For each update, replace price level quantity directly (no delta math).
- `size == 0` removes a level.

### Hyperliquid
- If no reliable sequence: treat each update as a snapshot.
- Overwrite book on every update.
- If sequence exists later, switch to delta logic in adapter with a guard flag.

### Kraken (Checksum example)
- Apply updates in order.
- Compute CRC32 over top-10 asks (low->high) then top-10 bids (high->low) when checksum is present.
- If checksum mismatch => resnapshot immediately.
- Truncate book to subscribed depth after each update (top-N feeds may not send deletes for out-of-scope levels).

---

## Order Book Engine (Per Exchange)
Implementation rules:
- Store bids/asks as `map[int64]int64` for size by price (fixed-point).
- Maintain top-N via tree/skiplist or heap; do not rebuild on every update.
- Apply snapshot: clear all levels, then insert levels.
- Apply delta: set quantity; if zero => delete level.
- Track last sequence ID per symbol per exchange.

Performance rules:
- No allocations in hot path after warm-up (reuse slices).
- No logging in hot path (only counters/metrics).

---

## Central DOM Aggregator
- Maintain aggregated `price->size` map per symbol+side.
- Apply deltas from per-exchange books; avoid full recompute.
- Rebuild top-N view only on:
  1) explicit request,
  2) timer tick,
  3) resync event.

### Runtime Price Grouping (Configurable)
Grouping is a *view* over the raw book and must be configurable at runtime.
We always store the raw book at native exchange tick size and derive grouped DOMs on output.

Rules (exact):
- Each symbol has a `group_step` that can be changed at runtime.
- Grouping uses fixed-point integer ticks; no floats.
- For bids: `bucket = floor(price / groupStep) * groupStep`.
- For asks: `bucket = ceil(price / groupStep) * groupStep`.
- Sum sizes for all levels in the same bucket.

Precision enforcement:
- Each exchange publishes min price tick per symbol.
- If `group_step` is *finer* than min tick, reject or round up to the nearest valid multiple.
- If `group_step` is *coarser*, grouping is safe and exact.

Implementation notes:
- Store raw prices in `int64` ticks (per-symbol tick size).
- Convert `group_step` to `groupStepTicks` using the same tick size.
- Output grouped prices as decimal strings.

Output:
```
type AggregatedLevel struct {
    Price string
    Size  string
}

type DomSnapshot struct {
    Symbol   string
    Bids     []AggregatedLevel
    Asks     []AggregatedLevel
    Updated  int64
}
```

---

## Concurrency Model
- One goroutine per exchange adapter.
- One goroutine per symbol per exchange to apply updates (or shard by exchange if symbols are many).
- Book updates are serialized per symbol per exchange.
- Aggregation is serialized per symbol.

Channels:
- `updates chan L2Update` (bounded)
- `errors chan error` (bounded)

Backpressure rules:
- If updates queue is full, drop oldest and trigger resync for that symbol.

---

## Startup Flow (Exact)
1) Load config + symbol map.
2) For each exchange:
   a) Connect WS.
   b) Start reading updates into buffer.
   c) Fetch REST snapshot for each symbol.
   d) Apply snapshot.
   e) Apply buffered updates per exchange sequencing rules.
3) Start aggregator loop.
4) Start health loop.

---

## Resync + Health (Exact)
Resync triggers:
- Sequence gap detected.
- Checksum mismatch (if provided).
- Stale data for N seconds.
- Update backlog exceeded.

Actions:
- Stop updates for symbol.
- Clear book.
- Re-run snapshot flow.

Health outputs (per exchange + symbol):
- `last_update_ms`
- `last_seq`
- `book_depth`
- `checksum_status` (ok/mismatch/unknown)

---

## Configuration (Env)
```
DOM_SYMBOLS=BTC,ETH,SOL
DOM_DEPTH=50
DOM_EXCHANGES=hyperliquid,binance,bybit,coinbase
DOM_WS_RECONNECT_SEC=5
DOM_HEALTH_CHECK_SEC=2
DOM_MAX_SYMBOLS_PER_CONN=20
DOM_SUBSCRIBE_RATE_LIMIT_MS=100
DOM_GROUP_STEP=0.5            # default grouping step
DOM_GROUP_STEP_BY_SYMBOL=BTC:0.5,ETH:0.1,SOL:1
DOM_PRICE_TICK=1              # default min price tick (BTC=1)
DOM_PRICE_TICK_BY_EXCHANGE_SYMBOL=binance:BTC:1,hyperliquid:BTC:1
DOM_PRICE_DECIMALS=2          # display precision for price (render only)
DOM_SIZE_TICK=0.00001         # default min size tick
DOM_RENDER_MS=200             # screen refresh cadence
DOM_VALIDATE_SUM=false        # validate master == sum of per-exchange books
DOM_SINGLE_EXCHANGE=true      # enforce one exchange per process (separate instances)
```

---

## Build Checklist (Agent Interface)
Use this as your step-by-step interface when coding:

1) Schema
- Define `L2Update` and fixed-point conversion helpers.
- Add `DepthCap`, `Checksum` support.

2) Book Engine
- Implement `ApplySnapshot`, `ApplyDelta`, `TopN`.
- Add `Seq` tracking + gap detection.
- Add optional checksum validator hook.

3) Adapter (per exchange)
- Implement: connect, subscribe, snapshot fetch, buffer, apply rules.
- Provide strict sequence rules per exchange (see above).
- Emit `L2Update` only when valid.

4) Aggregator
- Implement aggregated map + top-N view.
- Update by delta (no full recompute).

5) Health + Resync
- Implement resync triggers and state machine.
- Expose health metrics or logs.

6) Benchmarks
- Benchmark: per-update latency, top-N rebuild, resync time.

---

## Testing and Validation (Must Do)

### Unit Tests
- Book apply snapshot/delta.
- Zero-size removes level.
- Seq gap triggers resync.
- Checksum mismatch triggers resync (when enabled).
- DepthCap truncation behavior.

### Adapter Tests (Per Exchange)
- Snapshot + buffered updates flow.
- Out-of-order update handling.
- Gap detection logic using recorded fixtures.

### Integration Tests
- Simulate each exchange with recorded WS streams.
- Compare against known-good book states at checkpoints.
- Validate aggregator output equals sum of per-exchange books.

### Property Tests
- Reapplying same updates yields same book.
- Apply snapshot then deltas equals direct snapshot of final state.

---

## Troubleshooting Playbook (Exact)

If book diverges:
1) Check `checksum_status` (if available). If mismatch => resync.
2) Validate sequence continuity for last 100 updates.
3) Confirm snapshot applied before deltas.
4) Check DepthCap truncation logic for that exchange.
5) Compare local top-10 vs exchange top-10.

If latency spikes:
1) Inspect allocations in hot path.
2) Check backlog size and drop rate.
3) Ensure top-N rebuild is not per-update.
4) Disable logging in hot path.

If missing updates:
1) Check WS reconnects.
2) Verify buffer + snapshot flow.
3) Confirm symbol subscription limits per connection.

---

## Low Latency Rules
- Preallocate slices for depth.
- Avoid map allocations per update.
- Use fixed-point math.
- No JSON unmarshal into map in hot path.

---

## CEX Compatibility Summary
- Enforce exchange-specific sequencing rules.
- Support checksum verification where provided.
- Respect top-N depth truncation semantics.
- Avoid float rounding in book state.
