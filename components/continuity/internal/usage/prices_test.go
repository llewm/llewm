package usage

import "testing"

func TestPricesResolveTheMostSpecificDatedVariant(t *testing.T) {
	for range 100 {
		resolved, ok := (Prices{}).Resolve("claude-opus-5-5-20261005")
		if !ok || resolved.RateModel != "claude-opus-5-5" || resolved.Input != 4 || resolved.Output != 20 {
			t.Fatalf("resolved = %+v, %t", resolved, ok)
		}
	}
	override := Price{Input: 7, Output: 9, Source: "reviewed", AsOf: "2026-10-05", Basis: "custom API tier"}
	resolved, ok := (Prices{Overrides: map[string]Price{"claude-opus-5": override}}).Resolve("claude-opus-5-5-20261005")
	if !ok || resolved.RateModel != "claude-opus-5" || resolved.Input != 7 {
		t.Fatalf("override = %+v, %t", resolved, ok)
	}
}

func TestConfiguredPricesRequireAuditableProvenance(t *testing.T) {
	valid := Price{Input: 1, Output: 2, Source: "official", AsOf: "2026-10-05", Basis: "standard API"}
	if err := (Prices{Overrides: map[string]Price{"model": valid}}).Validate(); err != nil {
		t.Fatal(err)
	}
	for name, price := range map[string]Price{
		"source": {Input: 1, Output: 2, AsOf: valid.AsOf, Basis: valid.Basis},
		"as_of":  {Input: 1, Output: 2, Source: valid.Source, Basis: valid.Basis},
		"basis":  {Input: 1, Output: 2, Source: valid.Source, AsOf: valid.AsOf},
		"date":   {Input: 1, Output: 2, Source: valid.Source, AsOf: "October 5", Basis: valid.Basis},
	} {
		if err := (Prices{Overrides: map[string]Price{"model": price}}).Validate(); err == nil {
			t.Errorf("missing or invalid %s accepted", name)
		}
	}
}
