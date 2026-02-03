# DOM Commands and Runtime Parameters

This file documents how to start the DOM, what parameters it accepts, and what the output means.

---

## Start Commands

Run a single exchange instance (recommended):
```bash
GOCACHE=/tmp/go-build \
DOM_EXCHANGES=hyperliquid \
DOM_SYMBOLS=BTC \
DOM_GROUP_STEP=1 \
DOM_PRICE_TICK=1 \
DOM_PRICE_DECIMALS=2 \
go run ./cmd/dom
```

Run Binance only:
```bash
GOCACHE=/tmp/go-build \
DOM_EXCHANGES=binance \
DOM_SYMBOLS=BTC \
DOM_GROUP_STEP=1 \
DOM_PRICE_TICK=1 \
DOM_PRICE_DECIMALS=2 \
go run ./cmd/dom
```

Show USD notional for all size columns:
```bash
DOM_VALUE_MODE=usd DOM_VALUE_DECIMALS=2 DOM_EXCHANGES=binance DOM_SYMBOLS=BTC go run ./cmd/dom
```

Run in dry-run mode (mocked data, no exchange access):
```bash
DOM_DRY_RUN=true DOM_EXCHANGES=mock DOM_SYMBOLS=BTC go run ./cmd/dom
```

If you do not need `GOCACHE`, you can omit it. It is used to avoid permission issues in some environments.

---

## Environment Variables

Core:
- `DOM_SYMBOLS` (default: `BTC`)  
  Comma-separated symbols: `BTC,ETH`.
- `DOM_EXCHANGES` (default: `hyperliquid,binance,bybit,coinbase`)  
  Comma-separated exchange adapters to run. With `DOM_SINGLE_EXCHANGE=true`, only the first is used.
- `DOM_SINGLE_EXCHANGE` (default: `true`)  
  Enforces one exchange per process. Start separate processes for each exchange.
- `DOM_DRY_RUN` (default: `false`)  
  Uses mock adapter instead of live exchanges.

Timing:
- `DOM_SNAPSHOT_SEC` (default: `5`)  
  Snapshot refresh interval (seconds).
- `DOM_HEALTH_CHECK_SEC` (default: `2`)  
  Health-check interval (seconds).
- `DOM_RENDER_MS` (default: `200`)  
  Screen refresh rate in milliseconds.

Grouping and precision:
- `DOM_GROUP_STEP` (default: `1`)  
  Price band size for grouping (e.g., `0.1`, `0.5`, `1`, `10`).
- `DOM_GROUP_LEVELS` (default: `10`)  
  How many bands above/below the anchor price.
- `DOM_GROUP_STEP_BY_SYMBOL` (default: empty)  
  Per-symbol override: `BTC:1,ETH:0.1`.
- `DOM_PRICE_TICK` (default: `1`)  
  Min price tick applied to all exchanges.
- `DOM_PRICE_TICK_BY_EXCHANGE_SYMBOL` (default: empty)  
  Per-exchange override: `binance:BTC:1,hyperliquid:BTC:1`.
- `DOM_PRICE_DECIMALS` (default: `2`)  
  Price display precision (render-only).
- `DOM_SIZE_TICK` (default: `0.00001`)  
  Min size tick applied to all exchanges.
- `DOM_SIZE_TICK_BY_EXCHANGE_SYMBOL` (default: empty)  
  Per-exchange override: `binance:BTC:0.00001`.
- `DOM_VALUE_MODE` (default: `size`)  
  `size` shows raw size; `usd` converts all size columns to USD notional.
- `DOM_VALUE_DECIMALS` (default: `2`)  
  Decimal places for USD notional when `DOM_VALUE_MODE=usd`.

Validation:
- `DOM_VALIDATE_SUM` (default: `false`)  
  Validates master book equals sum of per-exchange books (logs errors if mismatch).

Exchange-specific:
- `DOM_HL_NSIGFIGS` (default: `0`)  
  Hyperliquid price formatting control (optional).
- `DOM_HL_MANTISSA` (default: `0`)  
  Hyperliquid price formatting control (optional).

---

## Output Format (Example)

Header:
```
EXCHANGE DOM BTC (binance) step=1 levels=10
PRICE | BID | ASK | DELTA | BUY | SELL | VOL
```

Row example:
```
95451.00 | 3.01905 | 7.28615 | 0.30000 | 0.00000 | 0.00000 | 0.00000
```

USD notional example (with `DOM_VALUE_MODE=usd`):
```
95451.00 | 288237.89 | 695585.40 | 28622.99 | 0.00 | 0.00 | 0.00
```

Field meanings:
- `PRICE` = price band center (grouped by `DOM_GROUP_STEP`).
- `BID` / `ASK` = aggregated size at that band (or USD notional when `DOM_VALUE_MODE=usd`).
- `DELTA` = bid delta + ask delta since last render (size or USD).
- `BUY` / `SELL` = trade volume per band (size or USD).
- `VOL` = buy + sell volume per band (size or USD).

---

## Recommended Usage

Run one exchange per process for accuracy and clarity:
```bash
DOM_EXCHANGES=binance DOM_SYMBOLS=BTC go run ./cmd/dom
```
Then run another terminal for Hyperliquid:
```bash
DOM_EXCHANGES=hyperliquid DOM_SYMBOLS=BTC go run ./cmd/dom
```
