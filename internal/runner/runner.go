package runner

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"dom/internal/config"
	"dom/internal/dom"
	"dom/internal/exchange"
	"dom/internal/exchange/binance"
	"dom/internal/exchange/bybit"
	"dom/internal/exchange/coinbase"
	"dom/internal/exchange/hyperliquid"
	"dom/internal/exchange/mock"
	"dom/internal/schema"
)

type Runner struct {
	cfg      config.Config
	adapters []exchange.Adapter
	master   *dom.Master
	primary  string
	pricesMu sync.Mutex
	prices   map[string]schema.PriceUpdate
}

const baseMul = int64(100_000_000)

func New(cfg config.Config) *Runner {
	return &Runner{cfg: cfg}
}

func (r *Runner) Start(ctx context.Context) error {
	r.master = dom.NewMaster(baseMul, baseMul)
	applyTicks(r.master, r.cfg)
	r.adapters = r.buildAdapters()
	r.prices = make(map[string]schema.PriceUpdate)
	r.startRenderer(ctx)
	for _, a := range r.adapters {
		if r.primary == "" {
			r.primary = a.Name()
		}
		if err := a.Connect(ctx); err != nil {
			log.Printf("connect error: %s: %v", a.Name(), err)
		}
		if err := a.Subscribe(ctx, a.Symbols()); err != nil {
			log.Printf("subscribe error: %s: %v", a.Name(), err)
		}
		r.consumeAdapter(ctx, a)
	}

	snapTicker := time.NewTicker(r.cfg.SnapshotInterval)
	defer snapTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-snapTicker.C:
			for _, a := range r.adapters {
				for _, sym := range a.Symbols() {
					if err := a.FetchSnapshot(ctx, sym); err != nil {
						log.Printf("snapshot error: %s %s: %v", a.Name(), sym, err)
					}
				}
			}
		}
	}
}

func (r *Runner) consumeAdapter(ctx context.Context, a exchange.Adapter) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case upd, ok := <-a.Updates():
				if !ok {
					return
				}
				if r.master != nil {
					_ = r.master.ApplyUpdate(upd)
				}
			case pu, ok := <-a.Prices():
				if !ok {
					return
				}
				r.recordPrice(pu)
			case err, ok := <-a.Errors():
				if !ok {
					return
				}
				log.Printf("error %s: %v", a.Name(), err)
			}
		}
	}()
}

func (r *Runner) buildAdapters() []exchange.Adapter {
	exchanges := r.cfg.Exchanges
	if r.cfg.SingleExchange && len(exchanges) > 1 {
		exchanges = exchanges[:1]
		log.Printf("single exchange mode enabled; using %s only", exchanges[0])
	}
	adapters := make([]exchange.Adapter, 0, len(exchanges))
	for _, name := range exchanges {
		if r.cfg.DryRun {
			adapters = append(adapters, mock.New(name, r.cfg.Symbols))
			continue
		}
		switch name {
		case "binance":
			adapters = append(adapters, binance.New(r.cfg.Symbols))
		case "bybit":
			adapters = append(adapters, bybit.New(r.cfg.Symbols))
		case "coinbase":
			adapters = append(adapters, coinbase.New(r.cfg.Symbols))
		case "hyperliquid":
			adapters = append(adapters, hyperliquid.New(r.cfg.Symbols, r.cfg.HyperliquidNSigFigs, r.cfg.HyperliquidMantissa))
		default:
			adapters = append(adapters, mock.New(name, r.cfg.Symbols))
		}
	}
	return adapters
}

func logTopLevels(upd schema.L2Update, n int) {
	if n <= 0 {
		return
	}
	if len(upd.Bids) > 0 {
		limit := minInt(n, len(upd.Bids))
		for i := 0; i < limit; i++ {
			lv := upd.Bids[i]
			log.Printf("bid[%d] price=%s size=%s count=%d", i, lv.Price, lv.Size, lv.Count)
		}
	}
	if len(upd.Asks) > 0 {
		limit := minInt(n, len(upd.Asks))
		for i := 0; i < limit; i++ {
			lv := upd.Asks[i]
			log.Printf("ask[%d] price=%s size=%s count=%d", i, lv.Price, lv.Size, lv.Count)
		}
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (r *Runner) startRenderer(ctx context.Context) {
	interval := time.Duration(r.cfg.RenderIntervalMs) * time.Millisecond
	if interval <= 0 {
		interval = 200 * time.Millisecond
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.render()
			}
		}
	}()
}

func (r *Runner) render() {
	if r.master == nil {
		return
	}
	if len(r.cfg.Symbols) == 0 {
		return
	}
	sym := r.cfg.Symbols[0]
	step := r.cfg.GroupStep
	if v, ok := r.cfg.GroupStepBySymbol[strings.ToUpper(sym)]; ok {
		step = v
	}
	groupTicks := dom.ParseFixedTicks(step, baseMul)
	priceTickTicks := dom.ParseFixedTicks(r.cfg.PriceTick, baseMul)
	if priceTickTicks > 0 && (groupTicks <= 0 || groupTicks < priceTickTicks) {
		groupTicks = priceTickTicks
	}
	if groupTicks <= 0 {
		return
	}
	rows := r.master.CenteredBands(sym, groupTicks, r.cfg.GroupLevels)
	if len(rows) == 0 {
		return
	}
	clearScreen()
	stats := r.master.Stats(sym)
	lastAge := "n/a"
	if !stats.LastUpdate.IsZero() {
		lastAge = time.Since(stats.LastUpdate).Truncate(time.Millisecond).String()
	}
	exName := r.primary
	if exName == "" && len(r.cfg.Exchanges) > 0 {
		exName = r.cfg.Exchanges[0]
	}
	log.Printf("EXCHANGE DOM %s (%s) step=%s levels=%d bids=%d asks=%d last=%s neg=%d", sym, exName, step, r.cfg.GroupLevels, stats.BidLevels, stats.AskLevels, lastAge, stats.NegResidues)
	if refLine := r.renderRefLine(sym); refLine != "" {
		log.Printf("%s", refLine)
	}
	log.Printf("PRICE | BID | ASK | dBid | dAsk | Buy | Sell | cBid | cAsk | Top5")
	for _, row := range rows {
		top := strings.Join(row.TopTrades, ",")
		price := formatPrice(row, r.cfg.PriceDecimals)
		log.Printf("%s | %s | %s | %s | %s | %s | %s | %s | %s | %s",
			price, row.BidSize, row.AskSize, row.BidDelta, row.AskDelta, row.BuyVol, row.SellVol, row.BidCancel, row.AskCancel, top)
	}
	if r.cfg.ValidateSum {
		if err := r.master.ValidateAggregate(sym); err != nil {
			log.Printf("VALIDATE_SUM ERROR: %v", err)
		}
		if err := r.master.ValidateNonNegative(sym); err != nil {
			log.Printf("VALIDATE_NONNEG ERROR: %v", err)
		}
	}
}

func clearScreen() {
	print("\033[H\033[2J")
}

func applyTicks(master *dom.Master, cfg config.Config) {
	for _, ex := range cfg.Exchanges {
		for _, sym := range cfg.Symbols {
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
		ex := parts[0]
		sym := parts[1]
		master.SetPriceTick(ex, sym, val)
	}
	for key, val := range cfg.SizeTickByExSym {
		parts := strings.SplitN(key, ":", 2)
		if len(parts) != 2 {
			continue
		}
		ex := parts[0]
		sym := parts[1]
		master.SetSizeTick(ex, sym, val)
	}
}

func (r *Runner) recordPrice(pu schema.PriceUpdate) {
	key := strings.ToLower(pu.Exchange) + ":" + strings.ToUpper(pu.Symbol)
	r.pricesMu.Lock()
	r.prices[key] = pu
	r.pricesMu.Unlock()
}

func (r *Runner) renderRefLine(sym string) string {
	ex := r.primary
	if ex == "" {
		return ""
	}
	key := strings.ToLower(ex) + ":" + strings.ToUpper(sym)
	r.pricesMu.Lock()
	pu, ok := r.prices[key]
	r.pricesMu.Unlock()
	if !ok {
		return ""
	}
	refTicks := refPriceTicks(pu)
	if refTicks == 0 {
		return ""
	}
	refStr := dom.FormatFixedTicks(refTicks, baseMul)
	bid, ask, ok := r.master.BestTicks(sym)
	if !ok {
		return "REF " + pu.Source + "=" + refStr + " (domMid=n/a)"
	}
	domMid := (bid + ask) / 2
	domStr := dom.FormatFixedTicks(domMid, baseMul)
	diff := refTicks - domMid
	diffBps := 0.0
	if domMid != 0 {
		diffBps = (float64(diff) / float64(domMid)) * 10000
	}
	return fmt.Sprintf("REF %s=%s domMid=%s diff=%s (%0.2fbps)", pu.Source, refStr, domStr, dom.FormatFixedTicks(diff, baseMul), diffBps)
}

func refPriceTicks(pu schema.PriceUpdate) int64 {
	if pu.Price != "" {
		return dom.ParseFixedTicks(pu.Price, baseMul)
	}
	if pu.Bid != "" && pu.Ask != "" {
		bid := dom.ParseFixedTicks(pu.Bid, baseMul)
		ask := dom.ParseFixedTicks(pu.Ask, baseMul)
		if bid > 0 && ask > 0 {
			return (bid + ask) / 2
		}
	}
	return 0
}

func formatPrice(row dom.BandRow, decimals int) string {
	if decimals < 0 {
		return row.Price
	}
	displayMul := pow10(decimals)
	if displayMul <= 0 {
		return row.Price
	}
	scaled := convertTicks(row.PriceTicks, baseMul, displayMul)
	return dom.FormatFixedTicks(scaled, displayMul)
}

func convertTicks(v, fromMul, toMul int64) int64 {
	if fromMul == toMul || fromMul == 0 || toMul == 0 {
		return v
	}
	if fromMul > toMul {
		factor := fromMul / toMul
		if factor <= 1 {
			return v
		}
		return (v + factor/2) / factor
	}
	factor := toMul / fromMul
	if factor <= 1 {
		return v
	}
	return v * factor
}

func pow10(n int) int64 {
	if n <= 0 {
		return 1
	}
	out := int64(1)
	for i := 0; i < n; i++ {
		out *= 10
	}
	return out
}
