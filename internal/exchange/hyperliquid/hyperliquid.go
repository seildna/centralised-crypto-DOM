package hyperliquid

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"dom/internal/schema"

	"github.com/gorilla/websocket"
)

type Adapter struct {
	symbols  []string
	updates  chan schema.L2Update
	errs     chan error
	trades   chan schema.TradeUpdate
	ws       *websocket.Conn
	writeMu  sync.Mutex
	nSigFigs int
	mantissa int
}

func New(symbols []string, nSigFigs, mantissa int) *Adapter {
	return &Adapter{
		symbols:  symbols,
		updates:  make(chan schema.L2Update, 256),
		errs:     make(chan error, 16),
		trades:   make(chan schema.TradeUpdate, 256),
		nSigFigs: nSigFigs,
		mantissa: mantissa,
	}
}

func (a *Adapter) Name() string      { return "hyperliquid" }
func (a *Adapter) Symbols() []string { return a.symbols }

func (a *Adapter) Connect(ctx context.Context) error {
	dialer := websocket.DefaultDialer
	conn, _, err := dialer.DialContext(ctx, "wss://api.hyperliquid.xyz/ws", nil)
	if err != nil {
		return err
	}
	a.ws = conn

	go a.readLoop(ctx)
	return nil
}

func (a *Adapter) Subscribe(ctx context.Context, symbols []string) error {
	if a.ws == nil {
		return errors.New("hyperliquid websocket not connected")
	}
	for _, sym := range symbols {
		msg := map[string]any{
			"method": "subscribe",
			"subscription": map[string]any{
				"type": "l2Book",
				"coin": sym,
			},
		}
		sub := msg["subscription"].(map[string]any)
		if a.nSigFigs > 0 {
			sub["nSigFigs"] = a.nSigFigs
		}
		if a.mantissa > 0 {
			sub["mantissa"] = a.mantissa
		}
		if err := a.writeJSON(msg); err != nil {
			return err
		}
		trades := map[string]any{
			"method": "subscribe",
			"subscription": map[string]any{
				"type": "trades",
				"coin": sym,
			},
		}
		if err := a.writeJSON(trades); err != nil {
			return err
		}
	}
	return nil
}

func (a *Adapter) FetchSnapshot(ctx context.Context, symbol string) error {
	// Hyperliquid l2Book is snapshot-style; no REST snapshot documented.
	return nil
}

func (a *Adapter) Updates() <-chan schema.L2Update   { return a.updates }
func (a *Adapter) Errors() <-chan error              { return a.errs }
func (a *Adapter) Trades() <-chan schema.TradeUpdate { return a.trades }

func (a *Adapter) Close() error {
	if a.ws != nil {
		_ = a.ws.Close()
	}
	close(a.updates)
	close(a.errs)
	close(a.trades)
	return nil
}

func (a *Adapter) writeJSON(v any) error {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	return a.ws.WriteJSON(v)
}

type wsEnvelope struct {
	Channel string          `json:"channel"`
	Data    json.RawMessage `json:"data"`
}

type wsBook struct {
	Coin   string      `json:"coin"`
	Levels [][]wsLevel `json:"levels"`
	Time   int64       `json:"time"`
}

type wsLevel struct {
	Px string `json:"px"`
	Sz string `json:"sz"`
	N  int64  `json:"n"`
}

func (a *Adapter) readLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		_, msg, err := a.ws.ReadMessage()
		if err != nil {
			a.pushErr(fmt.Errorf("read: %w", err))
			return
		}

		var env wsEnvelope
		if err := json.Unmarshal(msg, &env); err != nil {
			a.pushErr(fmt.Errorf("decode envelope: %w", err))
			continue
		}

		switch env.Channel {
		case "l2Book":
			var book wsBook
			if err := json.Unmarshal(env.Data, &book); err != nil {
				a.pushErr(fmt.Errorf("decode book: %w", err))
				continue
			}

			recv := time.Now().UnixMilli()
			update := schema.L2Update{
				Exchange:   a.Name(),
				Symbol:     book.Coin,
				Timestamp:  book.Time,
				RecvTime:   recv,
				IsSnapshot: true,
				DepthCap:   20,
				Bids:       levelsToPriceLevels(book.Levels, 0),
				Asks:       levelsToPriceLevels(book.Levels, 1),
			}

			select {
			case a.updates <- update:
			case <-ctx.Done():
				return
			}
		case "trades":
			a.handleTrades(ctx, env.Data)
		default:
			continue
		}
	}
}

type wsTrade struct {
	Coin string `json:"coin"`
	Px   string `json:"px"`
	Sz   string `json:"sz"`
	Side string `json:"side"`
	Time int64  `json:"time"`
}

type wsTradesWrap struct {
	Coin   string    `json:"coin"`
	Trades []wsTrade `json:"trades"`
}

func (a *Adapter) handleTrades(ctx context.Context, raw json.RawMessage) {
	var trades []wsTrade
	if err := json.Unmarshal(raw, &trades); err == nil && len(trades) > 0 {
		for _, tr := range trades {
			a.emitTrade(ctx, tr, tr.Coin)
		}
		return
	}

	var wrap wsTradesWrap
	if err := json.Unmarshal(raw, &wrap); err == nil && len(wrap.Trades) > 0 {
		for _, tr := range wrap.Trades {
			coin := tr.Coin
			if coin == "" {
				coin = wrap.Coin
			}
			a.emitTrade(ctx, tr, coin)
		}
	}
}

func (a *Adapter) emitTrade(ctx context.Context, tr wsTrade, coin string) {
	if coin == "" || tr.Px == "" || tr.Sz == "" {
		return
	}
	now := time.Now().UnixMilli()
	ts := tr.Time
	if ts == 0 {
		ts = now
	}
	update := schema.TradeUpdate{
		Exchange:  a.Name(),
		Symbol:    coin,
		Timestamp: ts,
		RecvTime:  now,
		Price:     tr.Px,
		Size:      tr.Sz,
		TakerBuy:  strings.ToLower(tr.Side) == "buy",
	}
	select {
	case a.trades <- update:
	case <-ctx.Done():
		return
	}
}

func levelsToPriceLevels(levels [][]wsLevel, idx int) []schema.PriceLevel {
	if len(levels) <= idx {
		return nil
	}
	in := levels[idx]
	out := make([]schema.PriceLevel, 0, len(in))
	for _, lv := range in {
		out = append(out, schema.PriceLevel{
			Price: lv.Px,
			Size:  lv.Sz,
			Count: lv.N,
		})
	}
	return out
}

func (a *Adapter) pushErr(err error) {
	select {
	case a.errs <- err:
	default:
	}
}
