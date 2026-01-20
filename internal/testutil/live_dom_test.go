//go:build live

package testutil

import (
	"context"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"dom/internal/config"
	"dom/internal/dom"
	"dom/internal/exchange"
	"dom/internal/exchange/binance"
	"dom/internal/exchange/hyperliquid"
)

func TestLiveMasterDOM(t *testing.T) {
	cfg := config.Load()

	exchanges := splitOrDefault(os.Getenv("DOM_TEST_EXCHANGES"), cfg.Exchanges)
	symbols := splitOrDefault(os.Getenv("DOM_TEST_SYMBOLS"), cfg.Symbols)
	duration := durationOrDefault(os.Getenv("DOM_TEST_DURATION_SEC"), 10*time.Second)
	minEPS := intOrDefault(os.Getenv("DOM_TEST_MIN_EPS"), 0)

	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()

	master := dom.NewMaster(100_000_000, 100_000_000)
	applyTicks(master, cfg, exchanges, symbols)

	adapters := buildAdapters(exchanges, symbols, cfg)
	for _, a := range adapters {
		if err := a.Connect(ctx); err != nil {
			t.Fatalf("connect %s: %v", a.Name(), err)
		}
		if err := a.Subscribe(ctx, a.Symbols()); err != nil {
			t.Fatalf("subscribe %s: %v", a.Name(), err)
		}
		for _, sym := range a.Symbols() {
			_ = a.FetchSnapshot(ctx, sym)
		}
	}

	var total uint64
	go func() {
		<-ctx.Done()
		for _, a := range adapters {
			_ = a.Close()
		}
	}()

	errCh := make(chan error, 16)
	for _, a := range adapters {
		ad := a
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case upd, ok := <-ad.Updates():
					if !ok {
						return
					}
					_ = master.ApplyUpdate(upd)
					atomic.AddUint64(&total, 1)
				case err, ok := <-ad.Errors():
					if !ok {
						return
					}
					errCh <- err
					return
				}
			}
		}()
	}

	<-ctx.Done()
	if err := drainErr(errCh); err != nil {
		t.Fatalf("adapter error: %v", err)
	}

	count := atomic.LoadUint64(&total)
	eps := float64(count) / duration.Seconds()
	if minEPS > 0 && int(eps) < minEPS {
		t.Fatalf("throughput too low: %.2f eps (min %d)", eps, minEPS)
	}

	for _, sym := range symbols {
		if err := master.ValidateNonNegative(sym); err != nil {
			t.Fatalf("negative size detected: %v", err)
		}
	}
}

func buildAdapters(exchanges, symbols []string, cfg config.Config) []exchange.Adapter {
	adapters := make([]exchange.Adapter, 0, len(exchanges))
	for _, name := range exchanges {
		switch name {
		case "binance":
			adapters = append(adapters, binance.New(symbols))
		case "hyperliquid":
			adapters = append(adapters, hyperliquid.New(symbols, cfg.HyperliquidNSigFigs, cfg.HyperliquidMantissa))
		}
	}
	return adapters
}

func applyTicks(master *dom.Master, cfg config.Config, exchanges, symbols []string) {
	for _, ex := range exchanges {
		for _, sym := range symbols {
			if cfg.PriceTick != "" {
				master.SetPriceTick(ex, sym, cfg.PriceTick)
			}
			if cfg.SizeTick != "" {
				master.SetSizeTick(ex, sym, cfg.SizeTick)
			}
		}
	}
	for key, val := range cfg.PriceTickByExSym {
		parts := strings.SplitN(key, ":", 2)
		if len(parts) != 2 {
			continue
		}
		master.SetPriceTick(parts[0], parts[1], val)
	}
	for key, val := range cfg.SizeTickByExSym {
		parts := strings.SplitN(key, ":", 2)
		if len(parts) != 2 {
			continue
		}
		master.SetSizeTick(parts[0], parts[1], val)
	}
}

func drainErr(ch <-chan error) error {
	select {
	case err := <-ch:
		return err
	default:
		return nil
	}
}

func splitOrDefault(raw string, def []string) []string {
	if strings.TrimSpace(raw) == "" {
		return def
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return def
	}
	return out
}

func durationOrDefault(raw string, def time.Duration) time.Duration {
	if strings.TrimSpace(raw) == "" {
		return def
	}
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return def
	}
	return time.Duration(v) * time.Second
}

func intOrDefault(raw string, def int) int {
	if strings.TrimSpace(raw) == "" {
		return def
	}
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return def
	}
	return v
}
