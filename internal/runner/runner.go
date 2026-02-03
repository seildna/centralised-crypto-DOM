package runner

import (
	"context"
	"log"
	"math/big"
	"os"
	"strings"
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
	outFile  string
}

const baseMul = int64(100_000_000)

func New(cfg config.Config) *Runner {
	return &Runner{cfg: cfg}
}

func (r *Runner) Start(ctx context.Context) error {
	r.master = dom.NewMaster(baseMul, baseMul)
	applyTicks(r.master, r.cfg)
	r.adapters = r.buildAdapters()
	r.startRenderer(ctx)
	r.initSnapshotFile()
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
			case tr, ok := <-a.Trades():
				if !ok {
					return
				}
				if r.master != nil {
					r.master.ApplyTrade(tr.Exchange, tr.Symbol, tr.Price, tr.Size, tr.TakerBuy)
				}
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
	clearScreen()
	exName := r.primary
	if exName == "" && len(r.cfg.Exchanges) > 0 {
		exName = r.cfg.Exchanges[0]
	}
	log.Printf("EXCHANGE DOM %s (%s) step=%s levels=%d", sym, exName, step, r.cfg.GroupLevels)
	log.Printf("%s", r.renderHeaderLine())
	r.writeSnapshot(sym, exName, step, rows)
	if len(rows) == 0 {
		log.Printf("waiting for trades...")
		return
	}
	for _, row := range rows {
		price := formatPrice(row, r.cfg.PriceDecimals)
		bid := r.formatValue(row, row.BidSize)
		ask := r.formatValue(row, row.AskSize)
		delta := r.formatSumValue(row, row.BidDelta, row.AskDelta)
		buy := r.formatValue(row, row.BuyVol)
		sell := r.formatValue(row, row.SellVol)
		vol := r.formatSumValue(row, row.BuyVol, row.SellVol)
		log.Printf("%s", r.renderRowLine(price, bid, ask, delta, buy, sell, vol))
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

func (r *Runner) initSnapshotFile() {
	path := strings.TrimSpace(r.cfg.SnapshotFile)
	if path == "" {
		return
	}
	file, err := os.Create(path)
	if err != nil {
		log.Printf("snapshot file init error: %v", err)
		return
	}
	_ = file.Close()
	r.outFile = path
}

func (r *Runner) writeSnapshot(sym, exName, step string, rows []dom.BandRow) {
	if r.outFile == "" {
		return
	}
	file, err := os.Create(r.outFile)
	if err != nil {
		log.Printf("snapshot file write error: %v", err)
		return
	}
	defer file.Close()

	_, _ = file.WriteString("EXCHANGE DOM " + sym + " (" + exName + ") step=" + step + " levels=" + itoa(r.cfg.GroupLevels) + "\n")
	_, _ = file.WriteString(r.renderHeaderLine() + "\n")
	for _, row := range rows {
		price := formatPrice(row, r.cfg.PriceDecimals)
		bid := r.formatValue(row, row.BidSize)
		ask := r.formatValue(row, row.AskSize)
		delta := r.formatSumValue(row, row.BidDelta, row.AskDelta)
		buy := r.formatValue(row, row.BuyVol)
		sell := r.formatValue(row, row.SellVol)
		vol := r.formatSumValue(row, row.BuyVol, row.SellVol)
		line := r.renderRowLine(price, bid, ask, delta, buy, sell, vol) + "\n"
		_, _ = file.WriteString(line)
	}
	if len(rows) == 0 {
		_, _ = file.WriteString("waiting for trades...\n")
	}
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	buf := make([]byte, 0, 12)
	for v > 0 {
		d := v % 10
		buf = append(buf, byte('0'+d))
		v /= 10
	}
	if neg {
		buf = append(buf, '-')
	}
	for i, j := 0, len(buf)-1; i < j; i, j = i+1, j-1 {
		buf[i], buf[j] = buf[j], buf[i]
	}
	return string(buf)
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

func (r *Runner) formatValue(row dom.BandRow, value string) string {
	if strings.ToLower(strings.TrimSpace(r.cfg.ValueMode)) != "usd" {
		return formatCompact(value)
	}
	sizeTicks := dom.ParseFixedTicks(value, baseMul)
	if sizeTicks == 0 || row.PriceTicks == 0 {
		return "0"
	}
	return formatCompact(formatNotional(row.PriceTicks, sizeTicks, baseMul, baseMul, r.cfg.ValueDecimals))
}

func (r *Runner) formatSumValue(row dom.BandRow, a, b string) string {
	sum := dom.ParseFixedTicks(a, baseMul) + dom.ParseFixedTicks(b, baseMul)
	if strings.ToLower(strings.TrimSpace(r.cfg.ValueMode)) != "usd" {
		return formatCompact(dom.FormatFixedTicks(sum, baseMul))
	}
	if sum == 0 || row.PriceTicks == 0 {
		return "0"
	}
	return formatCompact(formatNotional(row.PriceTicks, sum, baseMul, baseMul, r.cfg.ValueDecimals))
}

func (r *Runner) renderHeaderLine() string {
	return padCenter("PRICE", 12) + " | " +
		padCenter("BID", 12) + " | " +
		padCenter("ASK", 12) + " | " +
		padCenter("DELTA", 12) + " | " +
		padCenter("BUY", 12) + " | " +
		padCenter("SELL", 12) + " | " +
		padCenter("VOL", 12)
}

func (r *Runner) renderRowLine(price, bid, ask, delta, buy, sell, vol string) string {
	return padLeft(price, 12) + " | " +
		padLeft(bid, 12) + " | " +
		padLeft(ask, 12) + " | " +
		padLeft(delta, 12) + " | " +
		padLeft(buy, 12) + " | " +
		padLeft(sell, 12) + " | " +
		padLeft(vol, 12)
}

func padLeft(s string, width int) string {
	if width <= 0 {
		return s
	}
	if len(s) >= width {
		return s
	}
	return strings.Repeat(" ", width-len(s)) + s
}

func padCenter(s string, width int) string {
	if width <= 0 {
		return s
	}
	if len(s) >= width {
		return s
	}
	pad := width - len(s)
	left := pad / 2
	right := pad - left
	return strings.Repeat(" ", left) + s + strings.Repeat(" ", right)
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

func formatNotional(priceTicks, sizeTicks, priceMul, sizeMul int64, decimals int) string {
	if decimals < 0 {
		decimals = 0
	}
	if priceMul <= 0 || sizeMul <= 0 {
		return "0"
	}
	if priceTicks == 0 || sizeTicks == 0 {
		return "0"
	}
	num := new(big.Int).Mul(big.NewInt(priceTicks), big.NewInt(sizeTicks))
	den := new(big.Int).Mul(big.NewInt(priceMul), big.NewInt(sizeMul))
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	num.Mul(num, scale)
	quo, rem := new(big.Int).QuoRem(num, den, new(big.Int))
	intPart := quo.String()
	if decimals == 0 {
		return intPart
	}
	frac := rem.String()
	for len(frac) < decimals {
		frac = "0" + frac
	}
	return intPart + "." + frac
}

func formatCompact(value string) string {
	v := strings.TrimSpace(value)
	if v == "" || v == "0" || v == "0.0" || v == "0.00" {
		return "0"
	}
	neg := false
	if strings.HasPrefix(v, "-") {
		neg = true
		v = strings.TrimPrefix(v, "-")
	}
	intPart := v
	fracPart := ""
	if dot := strings.IndexByte(v, '.'); dot >= 0 {
		intPart = v[:dot]
		if dot+1 < len(v) {
			fracPart = v[dot+1:]
		}
	}
	intPart = strings.TrimLeft(intPart, "0")
	if intPart == "" {
		intPart = "0"
	}
	mag := len(intPart) - 1
	unit := ""
	shift := 0
	switch {
	case mag >= 9:
		unit = "b"
		shift = 9
	case mag >= 6:
		unit = "m"
		shift = 6
	case mag >= 3:
		unit = "k"
		shift = 3
	default:
		shift = 0
	}
	if shift == 0 {
		out := intPart
		if fracPart != "" {
			out += "." + trimRightZeros(fracPart)
		}
		if neg {
			return "-" + out
		}
		return out
	}
	if len(intPart) <= shift {
		intPart = strings.Repeat("0", shift-len(intPart)+1) + intPart
	}
	cut := len(intPart) - shift
	whole := intPart[:cut]
	dec := intPart[cut:]
	if fracPart != "" {
		dec += fracPart
	}
	dec = trimRightZeros(dec)
	if len(dec) > 2 {
		dec = dec[:2]
	}
	out := whole
	if dec != "" {
		out += "." + dec
	}
	out += unit
	if neg {
		return "-" + out
	}
	return out
}

func trimRightZeros(s string) string {
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	return s
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
