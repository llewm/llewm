package usage

import (
	"fmt"
	"strings"
	"time"
)

// Price is a model's rate in US dollars per million tokens. CacheWrite is the
// short-lifetime write rate and CacheWriteLong the long (1 hour) one.
type Price struct {
	Input          float64 `json:"input" toml:"input"`
	CacheWrite     float64 `json:"cache_write" toml:"cache_write"`
	CacheWriteLong float64 `json:"cache_write_long" toml:"cache_write_long"`
	CacheRead      float64 `json:"cache_read" toml:"cache_read"`
	Output         float64 `json:"output" toml:"output"`
	// Source, AsOf, and Basis make the estimate's rate card auditable.
	Source string `json:"source" toml:"source"`
	AsOf   string `json:"as_of" toml:"as_of"`
	Basis  string `json:"basis" toml:"basis"`
}

// PriceSource names the built-in table's origin.
const (
	PriceSource = "Anthropic Claude API pricing: https://platform.claude.com/docs/en/about-claude/pricing"
	PriceAsOf   = "2026-10-05"
	PriceBasis  = "standard global API rate; cache writes use 5-minute and 1-hour rates"
)

// anthropic builds a Claude price from input and output rates and the cache
// read rate.
func anthropic(input, output, cacheRead float64) Price {
	return Price{Input: input, CacheWrite: input * 1.25, CacheWriteLong: input * 2, CacheRead: cacheRead, Output: output, Source: PriceSource, AsOf: PriceAsOf, Basis: PriceBasis}
}

// DefaultPrices are built-in rates for models whose list prices are known.
// Other models (including every Codex model) are unpriced until
// config.toml [usage.prices."MODEL"] names a rate.
var DefaultPrices = map[string]Price{
	"claude-fable-5-1":  anthropic(10, 50, 0.25),
	"claude-fable-5":    anthropic(10, 50, 1),
	"claude-opus-5-5":   anthropic(4, 20, 0.20),
	"claude-opus-5":     anthropic(5, 25, 0.5),
	"claude-opus-4-8":   anthropic(5, 25, 0.5),
	"claude-opus-4-7":   anthropic(5, 25, 0.5),
	"claude-opus-4-6":   anthropic(5, 25, 0.5),
	"claude-sonnet-5-5": anthropic(2, 10, 0.20),
	"claude-sonnet-5":   anthropic(2, 10, 0.2),
	"claude-sonnet-4-6": anthropic(3, 15, 0.3),
	"claude-haiku-4-5":  anthropic(1, 5, 0.1),
}

// Prices resolves a model's rate: configured overrides first, then the
// built-in table, matching the exact model name or a dated variant of it.
type Prices struct {
	Overrides map[string]Price
}

// AppliedPrice is the exact rate card selected for a reported model.
type AppliedPrice struct {
	Model     string `json:"model"`
	RateModel string `json:"rate_model"`
	Price
}

// Lookup returns the model's price and whether one is known.
func (p Prices) Lookup(model string) (Price, bool) {
	resolved, ok := p.Resolve(model)
	return resolved.Price, ok
}

// Resolve returns the model's price and its auditable provenance. Overrides
// take precedence over built-ins. Within either table, an exact match wins,
// then the longest dated-variant prefix wins deterministically.
func (p Prices) Resolve(model string) (AppliedPrice, bool) {
	for _, table := range []map[string]Price{p.Overrides, DefaultPrices} {
		if price, ok := table[model]; ok {
			return AppliedPrice{Model: model, RateModel: model, Price: price}, true
		}
	}
	for _, table := range []map[string]Price{p.Overrides, DefaultPrices} {
		best := ""
		for name := range table {
			if (strings.HasPrefix(model, name+"-") || strings.HasPrefix(model, name+"@")) && (len(name) > len(best) || len(name) == len(best) && name < best) {
				best = name
			}
		}
		if best != "" {
			return AppliedPrice{Model: model, RateModel: best, Price: table[best]}, true
		}
	}
	return AppliedPrice{}, false
}

// Validate rejects negative or empty configured rates.
func (p Prices) Validate() error {
	for model, price := range p.Overrides {
		if price.Input < 0 || price.CacheWrite < 0 || price.CacheWriteLong < 0 || price.CacheRead < 0 || price.Output < 0 {
			return fmt.Errorf("usage price for %q must not be negative", model)
		}
		if price.Input == 0 || price.Output == 0 {
			return fmt.Errorf("usage price for %q needs non-zero input and output rates", model)
		}
		if strings.TrimSpace(price.Source) == "" || strings.TrimSpace(price.AsOf) == "" || strings.TrimSpace(price.Basis) == "" {
			return fmt.Errorf("usage price for %q needs source, as_of, and basis", model)
		}
		if _, err := time.Parse("2006-01-02", price.AsOf); err != nil {
			return fmt.Errorf("usage price for %q as_of must be YYYY-MM-DD", model)
		}
	}
	return nil
}

// Cost estimates a request's cost in US dollars.
func (price Price) Cost(input, cacheWrite, cacheWriteLong, cacheRead, output int64) float64 {
	short := cacheWrite - cacheWriteLong
	longRate := price.CacheWriteLong
	if longRate == 0 {
		longRate = price.CacheWrite
	}
	return (float64(input)*price.Input + float64(short)*price.CacheWrite + float64(cacheWriteLong)*longRate + float64(cacheRead)*price.CacheRead + float64(output)*price.Output) / 1e6
}
