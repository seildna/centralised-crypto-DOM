package dom

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"dom/internal/schema"
)

type Master struct {
	mu        sync.Mutex
	perSym    map[string]*symbolAgg
	priceMul  int64
	sizeMul   int64
	priceTick map[string]int64
	sizeTick  map[string]int64
	lastSeq   map[string]int64
}

type symbolAgg struct {
	perEx      map[string]*exchangeBook
	bids       map[int64]int64
	asks       map[int64]int64
	deltaBids  map[int64]int64
	deltaAsks  map[int64]int64
	buyVol     map[int64]int64
	sellVol    map[int64]int64
	lastTrade  int64
	lastUpdate time.Time
}

type exchangeBook struct {
	bids map[int64]int64
	asks map[int64]int64
}

func NewMaster(priceMul, sizeMul int64) *Master {
	return &Master{
		perSym:    make(map[string]*symbolAgg),
		priceMul:  priceMul,
		sizeMul:   sizeMul,
		priceTick: make(map[string]int64),
		sizeTick:  make(map[string]int64),
		lastSeq:   make(map[string]int64),
	}
}

func (m *Master) SetPriceTick(exchange, symbol, tick string) {
	if tick == "" {
		return
	}
	key := strings.ToLower(exchange) + ":" + strings.ToUpper(symbol)
	m.priceTick[key] = parseFixed(tick, m.priceMul)
}

func (m *Master) SetSizeTick(exchange, symbol, tick string) {
	if tick == "" {
		return
	}
	key := strings.ToLower(exchange) + ":" + strings.ToUpper(symbol)
	m.sizeTick[key] = parseFixed(tick, m.sizeMul)
}

func (m *Master) ApplyUpdate(upd schema.L2Update) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	sym := upd.Symbol
	agg := m.ensureSymbol(sym)
	book := agg.ensureExchange(upd.Exchange)
	agg.deltaBids = make(map[int64]int64)
	agg.deltaAsks = make(map[int64]int64)
	priceTick := m.tickFor(m.priceTick, upd.Exchange, sym)
	sizeTick := m.tickFor(m.sizeTick, upd.Exchange, sym)

	if upd.Seq > 0 {
		key := strings.ToLower(upd.Exchange) + ":" + strings.ToUpper(sym)
		if last, ok := m.lastSeq[key]; ok && upd.Seq <= last {
			return false
		}
		m.lastSeq[key] = upd.Seq
	}

	if upd.IsSnapshot {
		m.applySnapshot(agg, book, upd, priceTick, sizeTick)
		agg.lastUpdate = time.Now()
		return true
	}

	m.applyDelta(agg, book, upd, priceTick, sizeTick)
	agg.lastUpdate = time.Now()
	return true
}

func (m *Master) Best(sym string) (bid, ask string, ok bool) {
	bidTicks, askTicks, ok := m.BestTicks(sym)
	if !ok {
		return "", "", false
	}
	return formatFixed(bidTicks, m.priceMul), formatFixed(askTicks, m.priceMul), true
}

func (m *Master) BestTicks(sym string) (bid, ask int64, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	agg, exists := m.perSym[sym]
	if !exists || len(agg.bids) == 0 || len(agg.asks) == 0 {
		return 0, 0, false
	}
	bid = maxKey(agg.bids)
	ask = minKey(agg.asks)
	if bid == 0 || ask == 0 {
		return 0, 0, false
	}
	return bid, ask, true
}

func (m *Master) TopN(sym string, n int) (bids, asks []schema.PriceLevel) {
	m.mu.Lock()
	defer m.mu.Unlock()
	agg, exists := m.perSym[sym]
	if !exists {
		return nil, nil
	}
	bids = topFromMap(agg.bids, n, true, m.priceMul, m.sizeMul)
	asks = topFromMap(agg.asks, n, false, m.priceMul, m.sizeMul)
	return bids, asks
}

type GroupLevel struct {
	Price string
	Size  string
	Delta string
}

type BandRow struct {
	PriceTicks int64
	Price      string
	BidSize    string
	AskSize    string
	BidDelta   string
	AskDelta   string
	BuyVol     string
	SellVol    string
}

type bandTotals struct {
	bidSize  map[int64]int64
	askSize  map[int64]int64
	bidDelta map[int64]int64
	askDelta map[int64]int64
	buyVol   map[int64]int64
	sellVol  map[int64]int64
}

func (m *Master) GroupedView(sym string, groupStepTicks int64, levels int) (bids, asks []GroupLevel) {
	if groupStepTicks <= 0 || levels <= 0 {
		return nil, nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	agg, exists := m.perSym[sym]
	if !exists || len(agg.bids) == 0 || len(agg.asks) == 0 {
		return nil, nil
	}

	bestBid := maxKey(agg.bids)
	bestAsk := minKey(agg.asks)

	bidBase := (bestBid / groupStepTicks) * groupStepTicks
	askBase := ((bestAsk + groupStepTicks - 1) / groupStepTicks) * groupStepTicks

	bidBuckets := bucketize(agg.bids, agg.deltaBids, groupStepTicks, true)
	askBuckets := bucketize(agg.asks, agg.deltaAsks, groupStepTicks, false)

	bids = make([]GroupLevel, 0, levels)
	for i := 0; i < levels; i++ {
		price := bidBase - int64(i)*groupStepTicks
		size := bidBuckets.levels[price]
		delta := bidBuckets.delta[price]
		bids = append(bids, GroupLevel{
			Price: formatFixed(price, m.priceMul),
			Size:  formatFixed(size, m.sizeMul),
			Delta: formatFixed(delta, m.sizeMul),
		})
	}

	asks = make([]GroupLevel, 0, levels)
	for i := 0; i < levels; i++ {
		price := askBase + int64(i)*groupStepTicks
		size := askBuckets.levels[price]
		delta := askBuckets.delta[price]
		asks = append(asks, GroupLevel{
			Price: formatFixed(price, m.priceMul),
			Size:  formatFixed(size, m.sizeMul),
			Delta: formatFixed(delta, m.sizeMul),
		})
	}

	return bids, asks
}

func (m *Master) CenteredBands(sym string, groupStepTicks int64, levels int) []BandRow {
	if groupStepTicks <= 0 || levels <= 0 {
		return nil
	}

	m.mu.Lock()
	agg, exists := m.perSym[sym]
	if !exists {
		m.mu.Unlock()
		return nil
	}

	anchor := m.anchorPriceLocked(agg, groupStepTicks)
	if anchor == 0 {
		m.mu.Unlock()
		return nil
	}

	bucketed := m.bucketAllLocked(agg, groupStepTicks)
	rows := make([]BandRow, 0, levels*2+1)
	start := anchor + int64(levels)*groupStepTicks
	end := anchor - int64(levels)*groupStepTicks
	for price := start; price >= end; price -= groupStepTicks {
		rows = append(rows, m.makeBandRow(price, bucketed))
	}
	m.resetAccumulatorsLocked(agg)
	m.mu.Unlock()
	return rows
}

type Stats struct {
	BidLevels  int
	AskLevels  int
	LastUpdate time.Time
}

func (m *Master) Stats(sym string) Stats {
	m.mu.Lock()
	defer m.mu.Unlock()
	agg, exists := m.perSym[sym]
	if !exists {
		return Stats{}
	}
	return Stats{
		BidLevels:  len(agg.bids),
		AskLevels:  len(agg.asks),
		LastUpdate: agg.lastUpdate,
	}
}

func (m *Master) ValidateNonNegative(sym string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	agg, exists := m.perSym[sym]
	if !exists {
		return nil
	}
	for price, size := range agg.bids {
		if size < 0 {
			return fmt.Errorf("negative bid size at %s: %d", formatFixed(price, m.priceMul), size)
		}
	}
	for price, size := range agg.asks {
		if size < 0 {
			return fmt.Errorf("negative ask size at %s: %d", formatFixed(price, m.priceMul), size)
		}
	}
	return nil
}

func (m *Master) ValidateAggregate(sym string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	agg, exists := m.perSym[sym]
	if !exists {
		return nil
	}

	bidSum := make(map[int64]int64)
	askSum := make(map[int64]int64)
	for _, ex := range agg.perEx {
		for price, size := range ex.bids {
			bidSum[price] += size
		}
		for price, size := range ex.asks {
			askSum[price] += size
		}
	}

	for price, size := range bidSum {
		if agg.bids[price] != size {
			return fmt.Errorf("aggregate bid mismatch %s: master=%d sum=%d", formatFixed(price, m.priceMul), agg.bids[price], size)
		}
	}
	for price, size := range agg.bids {
		if bidSum[price] != size {
			return fmt.Errorf("aggregate bid mismatch %s: master=%d sum=%d", formatFixed(price, m.priceMul), size, bidSum[price])
		}
	}
	for price, size := range askSum {
		if agg.asks[price] != size {
			return fmt.Errorf("aggregate ask mismatch %s: master=%d sum=%d", formatFixed(price, m.priceMul), agg.asks[price], size)
		}
	}
	for price, size := range agg.asks {
		if askSum[price] != size {
			return fmt.Errorf("aggregate ask mismatch %s: master=%d sum=%d", formatFixed(price, m.priceMul), size, askSum[price])
		}
	}
	return nil
}

func (m *Master) applySnapshot(agg *symbolAgg, book *exchangeBook, upd schema.L2Update, priceTick, sizeTick int64) {
	newBids := levelsToMap(upd.Bids, m.priceMul, m.sizeMul, priceTick, sizeTick)
	newAsks := levelsToMap(upd.Asks, m.priceMul, m.sizeMul, priceTick, sizeTick)

	// Apply deltas for bids
	applySnapshotDelta(agg.bids, book.bids, newBids, agg.deltaBids, agg, true)
	applySnapshotDelta(agg.asks, book.asks, newAsks, agg.deltaAsks, agg, false)

	book.bids = newBids
	book.asks = newAsks
}

func (m *Master) applyDelta(agg *symbolAgg, book *exchangeBook, upd schema.L2Update, priceTick, sizeTick int64) {
	applyDeltaLevels(agg.bids, book.bids, upd.Bids, m.priceMul, m.sizeMul, priceTick, sizeTick, agg.deltaBids, agg, true)
	applyDeltaLevels(agg.asks, book.asks, upd.Asks, m.priceMul, m.sizeMul, priceTick, sizeTick, agg.deltaAsks, agg, false)
}

func (m *Master) ensureSymbol(sym string) *symbolAgg {
	agg, ok := m.perSym[sym]
	if !ok {
		agg = &symbolAgg{
			perEx:     make(map[string]*exchangeBook),
			bids:      make(map[int64]int64),
			asks:      make(map[int64]int64),
			deltaBids: make(map[int64]int64),
			deltaAsks: make(map[int64]int64),
			buyVol:    make(map[int64]int64),
			sellVol:   make(map[int64]int64),
		}
		m.perSym[sym] = agg
	}
	return agg
}

func (a *symbolAgg) ensureExchange(ex string) *exchangeBook {
	book, ok := a.perEx[ex]
	if !ok {
		book = &exchangeBook{bids: make(map[int64]int64), asks: make(map[int64]int64)}
		a.perEx[ex] = book
	}
	return book
}

func applySnapshotDelta(master map[int64]int64, old map[int64]int64, newer map[int64]int64, deltaMap map[int64]int64, agg *symbolAgg, isBid bool) {
	for price, oldSize := range old {
		newSize, ok := newer[price]
		if !ok {
			applyAggDelta(master, deltaMap, price, -oldSize, agg, isBid)
		} else if newSize != oldSize {
			applyAggDelta(master, deltaMap, price, newSize-oldSize, agg, isBid)
		}
	}
	for price, newSize := range newer {
		if _, ok := old[price]; !ok {
			applyAggDelta(master, deltaMap, price, newSize, agg, isBid)
		}
	}
}

func applyDeltaLevels(master map[int64]int64, book map[int64]int64, levels []schema.PriceLevel, priceMul, sizeMul, priceTick, sizeTick int64, deltaMap map[int64]int64, agg *symbolAgg, isBid bool) {
	for _, lv := range levels {
		price := snapTick(parseFixed(lv.Price, priceMul), priceTick)
		newSize := snapTick(parseFixed(lv.Size, sizeMul), sizeTick)
		oldSize := book[price]
		if newSize == 0 {
			if oldSize != 0 {
				applyAggDelta(master, deltaMap, price, -oldSize, agg, isBid)
				delete(book, price)
			}
			continue
		}
		if oldSize != newSize {
			applyAggDelta(master, deltaMap, price, newSize-oldSize, agg, isBid)
			book[price] = newSize
		}
	}
}

func applyAggDelta(master map[int64]int64, deltaMap map[int64]int64, price, delta int64, agg *symbolAgg, isBid bool) {
	if delta == 0 {
		return
	}
	cur := master[price]
	next := cur + delta
	if next <= 0 {
		if cur != 0 {
			deltaApplied := -cur
			deltaMap[price] += deltaApplied
			delete(master, price)
		}
		return
	}
	master[price] = next
	deltaMap[price] += delta
}

func levelsToMap(levels []schema.PriceLevel, priceMul, sizeMul, priceTick, sizeTick int64) map[int64]int64 {
	m := make(map[int64]int64, len(levels))
	for _, lv := range levels {
		price := snapTick(parseFixed(lv.Price, priceMul), priceTick)
		size := snapTick(parseFixed(lv.Size, sizeMul), sizeTick)
		if size == 0 {
			continue
		}
		m[price] = size
	}
	return m
}

func parseFixed(s string, mul int64) int64 {
	var neg bool
	if len(s) == 0 || mul <= 0 {
		return 0
	}
	if s[0] == '-' {
		neg = true
		s = s[1:]
	}

	intPartStr := s
	fracPartStr := ""
	if dot := strings.IndexByte(s, '.'); dot >= 0 {
		intPartStr = s[:dot]
		if dot+1 < len(s) {
			fracPartStr = s[dot+1:]
		}
	}

	var intPart int64
	for i := 0; i < len(intPartStr); i++ {
		c := intPartStr[i]
		if c < '0' || c > '9' {
			continue
		}
		intPart = intPart*10 + int64(c-'0')
	}

	mulDigits := digits(mul) - 1
	var fracPart int64
	var i int
	for i = 0; i < len(fracPartStr) && i < mulDigits; i++ {
		c := fracPartStr[i]
		if c < '0' || c > '9' {
			break
		}
		fracPart = fracPart*10 + int64(c-'0')
	}
	for i < mulDigits {
		fracPart *= 10
		i++
	}

	if len(fracPartStr) > mulDigits {
		c := fracPartStr[mulDigits]
		if c >= '5' && c <= '9' {
			fracPart++
			if fracPart >= mul {
				intPart++
				fracPart -= mul
			}
		}
	}

	val := intPart*mul + fracPart
	return applySign(val, neg)
}

func applySign(v int64, neg bool) int64 {
	if neg {
		return -v
	}
	return v
}

func formatFixed(v int64, mul int64) string {
	if mul == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	intPart := v / mul
	frac := v % mul
	width := digits(mul) - 1
	if width < 0 {
		width = 0
	}
	out := fmt.Sprintf("%d", intPart)
	if width > 0 {
		out = fmt.Sprintf("%s.%0*d", out, width, frac)
	}
	if neg {
		return "-" + out
	}
	return out
}

func ParseFixedTicks(s string, mul int64) int64 {
	return parseFixed(s, mul)
}

func FormatFixedTicks(v int64, mul int64) string {
	return formatFixed(v, mul)
}

func (m *Master) tickFor(mv map[string]int64, exchange, symbol string) int64 {
	if mv == nil {
		return 0
	}
	key := strings.ToLower(exchange) + ":" + strings.ToUpper(symbol)
	if v, ok := mv[key]; ok {
		return v
	}
	return 0
}

func snapTick(v, tick int64) int64 {
	if tick <= 0 {
		return v
	}
	return ((v + tick/2) / tick) * tick
}

func (m *Master) ApplyTrade(exchange, symbol, price, size string, takerBuy bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	agg := m.ensureSymbol(symbol)
	priceTick := m.tickFor(m.priceTick, exchange, symbol)
	sizeTick := m.tickFor(m.sizeTick, exchange, symbol)
	p := snapTick(parseFixed(price, m.priceMul), priceTick)
	sz := snapTick(parseFixed(size, m.sizeMul), sizeTick)
	if p == 0 || sz == 0 {
		return
	}
	agg.lastTrade = p
	if takerBuy {
		agg.buyVol[p] += sz
	} else {
		agg.sellVol[p] += sz
	}
}

func (m *Master) anchorPriceLocked(agg *symbolAgg, groupStepTicks int64) int64 {
	if agg.lastTrade > 0 {
		return snapTick(agg.lastTrade, groupStepTicks)
	}
	if len(agg.bids) == 0 || len(agg.asks) == 0 {
		return 0
	}
	bestBid := maxKey(agg.bids)
	bestAsk := minKey(agg.asks)
	if bestBid == 0 || bestAsk == 0 {
		return 0
	}
	mid := (bestBid + bestAsk) / 2
	return snapTick(mid, groupStepTicks)
}

func (m *Master) bucketAllLocked(agg *symbolAgg, step int64) bandTotals {
	out := bandTotals{
		bidSize:  make(map[int64]int64),
		askSize:  make(map[int64]int64),
		bidDelta: make(map[int64]int64),
		askDelta: make(map[int64]int64),
		buyVol:   make(map[int64]int64),
		sellVol:  make(map[int64]int64),
	}
	for price, size := range agg.bids {
		bucket := bucketPrice(price, step, true)
		out.bidSize[bucket] += size
	}
	for price, size := range agg.asks {
		bucket := bucketPrice(price, step, false)
		out.askSize[bucket] += size
	}
	for price, delta := range agg.deltaBids {
		bucket := bucketPrice(price, step, true)
		out.bidDelta[bucket] += delta
	}
	for price, delta := range agg.deltaAsks {
		bucket := bucketPrice(price, step, false)
		out.askDelta[bucket] += delta
	}
	for price, vol := range agg.buyVol {
		bucket := bucketPrice(price, step, true)
		out.buyVol[bucket] += vol
	}
	for price, vol := range agg.sellVol {
		bucket := bucketPrice(price, step, false)
		out.sellVol[bucket] += vol
	}
	return out
}

func (m *Master) makeBandRow(price int64, totals bandTotals) BandRow {
	return BandRow{
		PriceTicks: price,
		Price:      formatFixed(price, m.priceMul),
		BidSize:    formatFixed(totals.bidSize[price], m.sizeMul),
		AskSize:    formatFixed(totals.askSize[price], m.sizeMul),
		BidDelta:   formatFixed(totals.bidDelta[price], m.sizeMul),
		AskDelta:   formatFixed(totals.askDelta[price], m.sizeMul),
		BuyVol:     formatFixed(totals.buyVol[price], m.sizeMul),
		SellVol:    formatFixed(totals.sellVol[price], m.sizeMul),
	}
}

func (m *Master) resetAccumulatorsLocked(agg *symbolAgg) {
	agg.deltaBids = make(map[int64]int64)
	agg.deltaAsks = make(map[int64]int64)
	agg.buyVol = make(map[int64]int64)
	agg.sellVol = make(map[int64]int64)
}

func digits(v int64) int {
	n := 0
	for v > 0 {
		n++
		v /= 10
	}
	return n
}

func topFromMap(m map[int64]int64, n int, desc bool, priceMul, sizeMul int64) []schema.PriceLevel {
	if n <= 0 || len(m) == 0 {
		return nil
	}
	keys := make([]int64, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if desc {
			return keys[i] > keys[j]
		}
		return keys[i] < keys[j]
	})
	if len(keys) > n {
		keys = keys[:n]
	}
	out := make([]schema.PriceLevel, 0, len(keys))
	for _, k := range keys {
		out = append(out, schema.PriceLevel{
			Price: formatFixed(k, priceMul),
			Size:  formatFixed(m[k], sizeMul),
		})
	}
	return out
}

func maxKey(m map[int64]int64) int64 {
	var max int64
	first := true
	for k := range m {
		if first || k > max {
			max = k
			first = false
		}
	}
	return max
}

func minKey(m map[int64]int64) int64 {
	var min int64
	first := true
	for k := range m {
		if first || k < min {
			min = k
			first = false
		}
	}
	return min
}

type bucketMap struct {
	levels map[int64]int64
	delta  map[int64]int64
}

func bucketize(levels map[int64]int64, deltas map[int64]int64, step int64, isBid bool) bucketMap {
	out := bucketMap{
		levels: make(map[int64]int64),
		delta:  make(map[int64]int64),
	}
	for price, size := range levels {
		bucket := bucketPrice(price, step, isBid)
		out.levels[bucket] += size
	}
	for price, delta := range deltas {
		bucket := bucketPrice(price, step, isBid)
		out.delta[bucket] += delta
	}
	return out
}

func bucketPrice(price, step int64, isBid bool) int64 {
	if step <= 0 {
		return price
	}
	if isBid {
		return (price / step) * step
	}
	return ((price + step - 1) / step) * step
}
