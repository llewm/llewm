package bootstrap

import (
	"testing"
	"time"
)

func TestDeliveredUnchanged(t *testing.T) {
	at := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	mine := Delivery{Client: "claude", SessionID: "a", DeliveredAt: at, PacketSHA256: "h2"}
	older := Delivery{Client: "claude", SessionID: "a", DeliveredAt: at.Add(-time.Hour), PacketSHA256: "h1"}
	other := Delivery{Client: "claude", SessionID: "b", DeliveredAt: at.Add(-time.Minute), PacketSHA256: "h2"}
	for _, test := range []struct {
		name       string
		deliveries []Delivery
		hash       string
		want       bool
	}{
		{"no hook delivery", nil, "h2", false},
		{"unchanged", []Delivery{mine, older}, "h2", true},
		{"changed since delivery", []Delivery{mine, older}, "h3", false},
		{"older match only", []Delivery{mine, older}, "h1", false},
		{"several sessions", []Delivery{mine, other}, "h2", false},
	} {
		if _, got := DeliveredUnchanged(test.deliveries, test.hash); got != test.want {
			t.Errorf("%s: got %v, want %v", test.name, got, test.want)
		}
	}
}
