package exchange

import (
	"context"

	"dom/internal/schema"
)

type Adapter interface {
	Name() string
	Symbols() []string
	Connect(ctx context.Context) error
	Subscribe(ctx context.Context, symbols []string) error
	FetchSnapshot(ctx context.Context, symbol string) error
	Updates() <-chan schema.L2Update
	Errors() <-chan error
	Close() error
}
