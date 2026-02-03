package binance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"dom/internal/schema"

	"github.com/gorilla/websocket"
)

type Adapter struct {
	symbols []string
	updates chan schema.L2Update
	errs    chan error
	ws      *websocket.Conn
	writeMu sync.Mutex
	stateMu sync.Mutex
	state   map[string]*bookState
	ctx     context.Context
}

func New(symbols []string) *Adapter {
	return &Adapter{
		symbols: symbols,
		updates: make(chan schema.L2Update, 256),
		errs:    make(chan error, 16),
		state:   make(map[string]*bookState),
	}
}

func (a *Adapter) Name() string      { return "binance" }
func (a *Adapter) Symbols() []string { return a.symbols }

func (a *Adapter) Connect(ctx context.Context) error {
	dialer := websocket.DefaultDialer
	conn, _, err := dialer.DialContext(ctx, "wss://stream.binance.com:9443/ws", nil)
	if err != nil {
		return err
	}
	a.ws = conn
	a.ctx = ctx
	go a.readLoop(ctx)
	return nil
}

func (a *Adapter) Subscribe(ctx context.Context, symbols []string) error {
	if a.ws == nil {
		return errors.New("binance websocket not connected")
	}
	params := make([]string, 0, len(symbols))
	for _, sym := range symbols {
		params = append(params, binanceStreamName(sym))
	}
	req := map[string]any{
		"method": "SUBSCRIBE",
		"params": params,
		"id":     time.Now().UnixNano(),
	}
	return a.writeJSON(req)
}

func (a *Adapter) FetchSnapshot(ctx context.Context, symbol string) error {
	sym := binanceSymbol(symbol)
	endpoint := "https://api.binance.com/api/v3/depth"
	q := url.Values{}
	q.Set("symbol", sym)
	q.Set("limit", "1000")
	reqURL := endpoint + "?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("snapshot status %d", resp.StatusCode)
	}

	var snap depthSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		return err
	}

	lastUpdateID := snap.LastUpdateID
	now := time.Now().UnixMilli()
	update := schema.L2Update{
		Exchange:   a.Name(),
		Symbol:     symbol,
		Timestamp:  now,
		RecvTime:   now,
		Seq:        lastUpdateID,
		IsSnapshot: true,
		DepthCap:   1000,
		Bids:       levelsFromStrings(snap.Bids),
		Asks:       levelsFromStrings(snap.Asks),
	}

	a.stateMu.Lock()
	st := a.ensureState(symbol)
	st.lastUpdateID = lastUpdateID
	st.hasSnapshot = true
	buffered := st.buffer
	st.buffer = nil
	a.stateMu.Unlock()

	a.emitUpdate(ctx, update)
	a.applyBuffered(symbol, buffered, lastUpdateID)
	return nil
}

func (a *Adapter) Updates() <-chan schema.L2Update { return a.updates }
func (a *Adapter) Errors() <-chan error            { return a.errs }

func (a *Adapter) Close() error {
	if a.ws != nil {
		_ = a.ws.Close()
	}
	close(a.updates)
	close(a.errs)
	return nil
}

type depthUpdate struct {
	EventType string     `json:"e"`
	EventTime int64      `json:"E"`
	Symbol    string     `json:"s"`
	FirstID   int64      `json:"U"`
	FinalID   int64      `json:"u"`
	Bids      [][]string `json:"b"`
	Asks      [][]string `json:"a"`
}

type depthSnapshot struct {
	LastUpdateID int64      `json:"lastUpdateId"`
	Bids         [][]string `json:"bids"`
	Asks         [][]string `json:"asks"`
}

type streamEnvelope struct {
	Stream string          `json:"stream"`
	Data   json.RawMessage `json:"data"`
}

type bookState struct {
	lastUpdateID int64
	hasSnapshot  bool
	buffer       []depthUpdate
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

		if isSubAck(msg) {
			continue
		}

		var env streamEnvelope
		if err := json.Unmarshal(msg, &env); err == nil && env.Stream != "" {
			a.handleDepthMsg(ctx, env.Data)
			continue
		}

		a.handleDepthMsg(ctx, msg)
	}
}

func (a *Adapter) handleDepthMsg(ctx context.Context, raw []byte) {
	var ev depthUpdate
	if err := json.Unmarshal(raw, &ev); err != nil {
		a.pushErr(fmt.Errorf("decode depth: %w", err))
		return
	}
	if ev.EventType != "depthUpdate" {
		return
	}

	symbol := fromBinanceSymbol(ev.Symbol)

	a.stateMu.Lock()
	st := a.ensureState(symbol)
	if !st.hasSnapshot {
		st.buffer = append(st.buffer, ev)
		a.stateMu.Unlock()
		return
	}
	lastID := st.lastUpdateID
	a.stateMu.Unlock()

	// Apply sequencing rules
	if ev.FinalID <= lastID {
		return
	}
	if ev.FirstID > lastID+1 {
		a.pushErr(fmt.Errorf("gap detected %s: last=%d U=%d u=%d", symbol, lastID, ev.FirstID, ev.FinalID))
		a.invalidateSnapshot(symbol)
		a.triggerResync(symbol)
		return
	}

	update := schema.L2Update{
		Exchange:   a.Name(),
		Symbol:     symbol,
		Timestamp:  ev.EventTime,
		RecvTime:   time.Now().UnixMilli(),
		Seq:        ev.FinalID,
		IsSnapshot: false,
		DepthCap:   1000,
		Bids:       levelsFromStrings(ev.Bids),
		Asks:       levelsFromStrings(ev.Asks),
	}
	a.emitUpdate(ctx, update)

	a.stateMu.Lock()
	st = a.ensureState(symbol)
	st.lastUpdateID = ev.FinalID
	a.stateMu.Unlock()
}

func (a *Adapter) applyBuffered(symbol string, buffered []depthUpdate, lastUpdateID int64) {
	if len(buffered) == 0 {
		return
	}

	// Discard events with u <= lastUpdateId
	kept := buffered[:0]
	for _, ev := range buffered {
		if ev.FinalID > lastUpdateID {
			kept = append(kept, ev)
		}
	}
	buffered = kept
	if len(buffered) == 0 {
		return
	}

	// First event must satisfy U <= lastUpdateId+1 <= u
	first := buffered[0]
	if !(first.FirstID <= lastUpdateID+1 && first.FinalID >= lastUpdateID+1) {
		a.pushErr(fmt.Errorf("buffer range mismatch %s: last=%d U=%d u=%d", symbol, lastUpdateID, first.FirstID, first.FinalID))
		a.invalidateSnapshot(symbol)
		a.triggerResync(symbol)
		return
	}

	for _, ev := range buffered {
		if ev.FinalID <= lastUpdateID {
			continue
		}
		if ev.FirstID > lastUpdateID+1 {
			a.pushErr(fmt.Errorf("buffer gap %s: last=%d U=%d u=%d", symbol, lastUpdateID, ev.FirstID, ev.FinalID))
			a.invalidateSnapshot(symbol)
			a.triggerResync(symbol)
			return
		}
		update := schema.L2Update{
			Exchange:   a.Name(),
			Symbol:     symbol,
			Timestamp:  ev.EventTime,
			RecvTime:   time.Now().UnixMilli(),
			Seq:        ev.FinalID,
			IsSnapshot: false,
			DepthCap:   1000,
			Bids:       levelsFromStrings(ev.Bids),
			Asks:       levelsFromStrings(ev.Asks),
		}
		a.emitUpdate(a.ctx, update)
		lastUpdateID = ev.FinalID
	}

	a.stateMu.Lock()
	st := a.ensureState(symbol)
	st.lastUpdateID = lastUpdateID
	a.stateMu.Unlock()
}

func (a *Adapter) emitUpdate(ctx context.Context, upd schema.L2Update) {
	select {
	case a.updates <- upd:
	case <-ctx.Done():
		return
	}
}

func (a *Adapter) ensureState(symbol string) *bookState {
	st, ok := a.state[symbol]
	if !ok {
		st = &bookState{}
		a.state[symbol] = st
	}
	return st
}

func (a *Adapter) invalidateSnapshot(symbol string) {
	a.stateMu.Lock()
	st := a.ensureState(symbol)
	st.hasSnapshot = false
	st.buffer = nil
	a.stateMu.Unlock()
}

func (a *Adapter) triggerResync(symbol string) {
	if a.ctx == nil {
		return
	}
	go func() {
		_ = a.FetchSnapshot(a.ctx, symbol)
	}()
}

func (a *Adapter) writeJSON(v any) error {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	return a.ws.WriteJSON(v)
}

func (a *Adapter) pushErr(err error) {
	select {
	case a.errs <- err:
	default:
	}
}

func levelsFromStrings(levels [][]string) []schema.PriceLevel {
	if len(levels) == 0 {
		return nil
	}
	out := make([]schema.PriceLevel, 0, len(levels))
	for _, lv := range levels {
		if len(lv) < 2 {
			continue
		}
		out = append(out, schema.PriceLevel{
			Price: lv[0],
			Size:  lv[1],
		})
	}
	return out
}

func binanceSymbol(sym string) string {
	switch strings.ToUpper(sym) {
	case "BTC":
		return "BTCUSDT"
	case "ETH":
		return "ETHUSDT"
	default:
		if strings.Contains(sym, "USDT") {
			return strings.ToUpper(sym)
		}
		return strings.ToUpper(sym) + "USDT"
	}
}

func fromBinanceSymbol(sym string) string {
	if strings.HasSuffix(sym, "USDT") {
		base := strings.TrimSuffix(sym, "USDT")
		if base != "" {
			return base
		}
	}
	return sym
}

func binanceStreamName(symbol string) string {
	return strings.ToLower(binanceSymbol(symbol)) + "@depth@100ms"
}

func isSubAck(msg []byte) bool {
	var ack struct {
		Result any   `json:"result"`
		ID     int64 `json:"id"`
	}
	if err := json.Unmarshal(msg, &ack); err != nil {
		return false
	}
	return ack.ID != 0 && ack.Result == nil
}
