package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Symbols             []string
	Exchanges           []string
	SnapshotInterval    time.Duration
	DryRun              bool
	HealthCheckInterval time.Duration
	HyperliquidNSigFigs int
	HyperliquidMantissa int
	GroupStep           string
	GroupLevels         int
	GroupStepBySymbol   map[string]string
	PriceTick           string
	PriceTickByExSym    map[string]string
	PriceDecimals       int
	SizeTick            string
	SizeTickByExSym     map[string]string
	RenderIntervalMs    int
	ValidateSum         bool
	SingleExchange      bool
}

func Load() Config {
	symbols := splitOrDefault(os.Getenv("DOM_SYMBOLS"), []string{"BTC"})
	exchanges := splitOrDefault(os.Getenv("DOM_EXCHANGES"), []string{"hyperliquid", "binance", "bybit", "coinbase"})
	return Config{
		Symbols:             symbols,
		Exchanges:           exchanges,
		SnapshotInterval:    durationOrDefault(os.Getenv("DOM_SNAPSHOT_SEC"), 5*time.Second),
		DryRun:              boolOrDefault(os.Getenv("DOM_DRY_RUN"), false),
		HealthCheckInterval: durationOrDefault(os.Getenv("DOM_HEALTH_CHECK_SEC"), 2*time.Second),
		HyperliquidNSigFigs: intOrDefault(os.Getenv("DOM_HL_NSIGFIGS"), 0),
		HyperliquidMantissa: intOrDefault(os.Getenv("DOM_HL_MANTISSA"), 0),
		GroupStep:           stringOrDefault(os.Getenv("DOM_GROUP_STEP"), "1"),
		GroupLevels:         intOrDefault(os.Getenv("DOM_GROUP_LEVELS"), 10),
		GroupStepBySymbol:   mapStringString(os.Getenv("DOM_GROUP_STEP_BY_SYMBOL")),
		PriceTick:           stringOrDefault(os.Getenv("DOM_PRICE_TICK"), "1"),
		PriceTickByExSym:    mapExSymString(os.Getenv("DOM_PRICE_TICK_BY_EXCHANGE_SYMBOL")),
		PriceDecimals:       intOrDefault(os.Getenv("DOM_PRICE_DECIMALS"), 2),
		SizeTick:            stringOrDefault(os.Getenv("DOM_SIZE_TICK"), "0.00001"),
		SizeTickByExSym:     mapExSymString(os.Getenv("DOM_SIZE_TICK_BY_EXCHANGE_SYMBOL")),
		RenderIntervalMs:    intOrDefault(os.Getenv("DOM_RENDER_MS"), 200),
		ValidateSum:         boolOrDefault(os.Getenv("DOM_VALIDATE_SUM"), false),
		SingleExchange:      boolOrDefault(os.Getenv("DOM_SINGLE_EXCHANGE"), true),
	}
}

func splitOrDefault(raw string, def []string) []string {
	if strings.TrimSpace(raw) == "" {
		return def
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		t := strings.TrimSpace(p)
		if t != "" {
			out = append(out, t)
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
	d, err := time.ParseDuration(raw + "s")
	if err != nil {
		return def
	}
	return d
}

func boolOrDefault(raw string, def bool) bool {
	if strings.TrimSpace(raw) == "" {
		return def
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "y":
		return true
	case "0", "false", "no", "n":
		return false
	default:
		return def
	}
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

func stringOrDefault(raw, def string) string {
	if strings.TrimSpace(raw) == "" {
		return def
	}
	return strings.TrimSpace(raw)
}

func mapStringString(raw string) map[string]string {
	out := make(map[string]string)
	if strings.TrimSpace(raw) == "" {
		return out
	}
	parts := strings.Split(raw, ",")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		kv := strings.SplitN(p, ":", 2)
		if len(kv) != 2 {
			continue
		}
		key := strings.TrimSpace(kv[0])
		val := strings.TrimSpace(kv[1])
		if key == "" || val == "" {
			continue
		}
		out[strings.ToUpper(key)] = val
	}
	return out
}

// Expected format: "binance:BTC:0.01,hyperliquid:BTC:1"
func mapExSymString(raw string) map[string]string {
	out := make(map[string]string)
	if strings.TrimSpace(raw) == "" {
		return out
	}
	parts := strings.Split(raw, ",")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		kv := strings.SplitN(p, ":", 3)
		if len(kv) != 3 {
			continue
		}
		ex := strings.TrimSpace(kv[0])
		sym := strings.TrimSpace(kv[1])
		val := strings.TrimSpace(kv[2])
		if ex == "" || sym == "" || val == "" {
			continue
		}
		out[strings.ToLower(ex)+":"+strings.ToUpper(sym)] = val
	}
	return out
}
