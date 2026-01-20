package bybit

import (
	"context"
	"errors"

	"dom/internal/schema"
)

type Adapter struct {
	symbols []string
	updates chan schema.L2Update
	prices  chan schema.PriceUpdate
	errs    chan error
}

func New(symbols []string) *Adapter {
	return &Adapter{
		symbols: symbols,
		updates: make(chan schema.L2Update, 256),
		prices:  make(chan schema.PriceUpdate, 256),
		errs:    make(chan error, 16),
	}
}

func (a *Adapter) Name() string      { return "bybit" }
func (a *Adapter) Symbols() []string { return a.symbols }

func (a *Adapter) Connect(ctx context.Context) error {
	return errors.New("bybit adapter not implemented")
}

func (a *Adapter) Subscribe(ctx context.Context, symbols []string) error {
	return errors.New("bybit adapter not implemented")
}

func (a *Adapter) FetchSnapshot(ctx context.Context, symbol string) error {
	return errors.New("bybit adapter not implemented")
}

func (a *Adapter) Updates() <-chan schema.L2Update   { return a.updates }
func (a *Adapter) Prices() <-chan schema.PriceUpdate { return a.prices }
func (a *Adapter) Errors() <-chan error              { return a.errs }

func (a *Adapter) Close() error {
	close(a.updates)
	close(a.prices)
	close(a.errs)
	return nil
}
