package mock

import (
	"context"
	"fmt"
	"sync"
	"time"

	"dom/internal/schema"
)

type Adapter struct {
	name    string
	symbols []string
	updates chan schema.L2Update
	errs    chan error
	seq     int64
	mu      sync.Mutex
	trades  chan schema.TradeUpdate
}

func New(name string, symbols []string) *Adapter {
	return &Adapter{
		name:    name,
		symbols: symbols,
		updates: make(chan schema.L2Update, 256),
		errs:    make(chan error, 16),
		trades:  make(chan schema.TradeUpdate, 16),
	}
}

func (a *Adapter) Name() string      { return a.name }
func (a *Adapter) Symbols() []string { return a.symbols }

func (a *Adapter) Connect(ctx context.Context) error                     { return nil }
func (a *Adapter) Subscribe(ctx context.Context, symbols []string) error { return nil }

func (a *Adapter) FetchSnapshot(ctx context.Context, symbol string) error {
	a.mu.Lock()
	a.seq++
	seq := a.seq
	a.mu.Unlock()

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	now := time.Now().UnixMilli()
	update := schema.L2Update{
		Exchange:   a.name,
		Symbol:     symbol,
		Timestamp:  now,
		RecvTime:   now,
		Seq:        seq,
		IsSnapshot: true,
		DepthCap:   20,
		Bids: []schema.PriceLevel{
			{Price: "65000.0", Size: "0.5", Count: 2},
			{Price: "64999.5", Size: "1.0", Count: 3},
		},
		Asks: []schema.PriceLevel{
			{Price: "65000.5", Size: "0.4", Count: 1},
			{Price: "65001.0", Size: "0.8", Count: 4},
		},
	}

	select {
	case a.updates <- update:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *Adapter) Updates() <-chan schema.L2Update   { return a.updates }
func (a *Adapter) Errors() <-chan error              { return a.errs }
func (a *Adapter) Trades() <-chan schema.TradeUpdate { return a.trades }

func (a *Adapter) Close() error {
	close(a.updates)
	close(a.errs)
	close(a.trades)
	return nil
}

func (a *Adapter) emitError(err error) {
	select {
	case a.errs <- fmt.Errorf("%s: %w", a.name, err):
	default:
	}
}
