# Exchange API Notes + Data Conflicts (L2/L3/Other)

This file summarizes *key* API behaviors for exchanges in scope and highlights
conflicts/edge cases that can break a unified DOM if not handled explicitly.
It is not a full doc mirror. Always verify against official docs before changes.

Exchanges covered:
- Binance (Spot + Futures)
- Bybit (V5)
- Coinbase Exchange (WebSocket Feed)
- Hyperliquid
- Kraken (book checksum behavior reference)

---

## Binance (Spot + Futures)

### L2 (order book depth)
- WS diff depth stream: updates include U (firstUpdateId), u (finalUpdateId).
- Correct local book procedure requires:
  - buffer updates
  - REST snapshot
  - discard updates with u <= lastUpdateId
  - first kept update must satisfy U <= lastUpdateId+1 <= u
  - apply in order; gap if U > currentUpdateId+1 => resync
- For futures/options, a previous update id `pu` must match previous `u`.
- REST snapshot depth is limited; levels outside snapshot are unknown until updated.

### L3
- Not provided via public WS; only L2 depth streams are available.

### Conflicts / Pitfalls
- Depth snapshots are limited (max 1000 or 5000 depending on endpoint), so book is
  incomplete beyond that depth until changes arrive.
- RPI orders are excluded from some endpoints (spot/futures notes).
- Update ordering and gap rules are strict; ignoring them causes divergence.

---

## Bybit (V5)

### L2 (order book)
- WS orderbook topic: orderbook.{depth}.{symbol}
- Depths are fixed per market type (e.g., 1/50/200/1000 for linear/inverse/spot).
- Initial message is `snapshot`; subsequent updates are `delta`.
- If a new snapshot arrives, you must reset local book (Bybit can resend snapshots).
- If size is 0, remove level; otherwise insert/update.
- Occasional `u == 1` indicates a snapshot; overwrite local book.
- Level 1 feeds may resend snapshot with unchanged `u` if no changes for ~3s.

### L3
- Not provided on public WS.

### Conflicts / Pitfalls
- Depth levels are discrete; requesting unsupported depth fails.
- Resent snapshots and `u == 1` snapshots must overwrite local state.
- `seq` is a cross-sequence for comparing different depth feeds, not a strict per-book
  sequence for delta ordering.

---

## Coinbase Exchange (WebSocket Feed)

### L2
- `level2` channel provides full snapshot and `l2update` messages.
- Snapshot includes full `bids`/`asks` arrays.
- `l2update` has `changes` entries [side, price, size].
- `size` is absolute, not delta; `0` means remove level.
- `level2_batch` batches updates every 50ms (same schema).

### L3
- `full` channel provides order-level events to reconstruct L3 book.
- `level3` channel is a compact version of `full` (same data, smaller format).
- Recommended procedure: queue WS messages, get REST snapshot, replay messages
  after snapshot sequence, then process live.

### Conflicts / Pitfalls
- `level2` is not order-level; it cannot be reconciled directly with L3 state.
- For `full`, not all `done`/`change` messages affect the book; misapplying them
  causes divergence (per docs).
- `level2_batch` reduces latency but changes message timing; must still preserve order.

---

## Hyperliquid

### L2 (l2Book)
- WS subscription: { "type": "l2Book", "coin": "<symbol>" }
- Data format: `WsBook { coin, levels: [bids, asks], time }`
- The feed is a snapshot-style update (not documented as diffs).
- Optional parameters: nSigFigs, mantissa (affects price rounding / formatting).

### L3
- Not exposed as public WS in docs.

### Conflicts / Pitfalls
- If updates are snapshots, you must overwrite book each update (no deltas).
- Optional rounding parameters can change price precision; must be reconciled
  with grouping/tick conversion.

---

## Kraken (Checksum reference)

### L2 (book)
- Book updates include a CRC32 checksum for top 10 asks/bids.
- Checksum order: top 10 asks low->high, then top 10 bids high->low.

### L3
- Not relevant for our current scope.

### Conflicts / Pitfalls
- Checksum mismatch indicates local book drift; must resync.
- Do not use floats; string formatting affects checksum.

---

## Cross-Exchange Conflicts to Handle Explicitly

### 1) Snapshot vs Delta Semantics
- Binance/Bybit: snapshots + deltas with strict sequencing rules.
- Coinbase L2: snapshot + absolute size updates (not deltas).
- Hyperliquid: snapshot-style updates (no seq in docs).

### 2) Depth Limits
- Binance/Bybit snapshots are depth-limited; deeper levels unknown until changed.
- Bybit supports only specific depth sizes.

### 3) Sequencing / Gap Detection
- Binance: U/u + optional pu must be enforced; gaps require resync.
- Bybit: `seq` not strictly per-book; use stream ordering and snapshots.
- Coinbase L2: ordering based on WS arrival; L3 uses sequence from full channel.

### 4) Precision / Rounding
- All exchanges send decimal strings. Use fixed-point internally.
- Hyperliquid optional rounding (nSigFigs/mantissa) can change precision.

### 5) Order-Level vs Price-Level
- Coinbase `full`/`level3` is order-level (L3).
- Binance/Bybit/Hyperliquid public feeds are price-level (L2).

---

## References (official docs)
- Binance Spot WS streams (depth + local book rules)
  https://developers.binance.com/docs/binance-spot-api-docs/testnet/web-socket-streams
- Binance Futures (local book rules + pu requirement)
  https://developers.binance.com/docs/derivatives/usds-margined-futures/websocket-market-streams/How-to-manage-a-local-order-book-correctly
- Bybit V5 WS orderbook
  https://bybit-exchange.github.io/docs/v5/websocket/public/orderbook
- Coinbase Exchange WS channels (level2, level3, full)
  https://docs.cdp.coinbase.com/exchange/websocket-feed/channels
- Hyperliquid WS subscriptions
  https://hyperliquid.gitbook.io/hyperliquid-docs/for-developers/api/websocket/subscriptions
- Kraken book checksum notes
  https://docs.kraken.com/websockets-beta/
