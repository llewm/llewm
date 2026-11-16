package testguard

import (
	"testing"
	"time"
)

func TestInvocationPolicy(t *testing.T) {
	for _, tt := range []struct {
		name, os, run, list string
		count, allow        bool
		timeout             time.Duration
		wantError           bool
	}{
		{name: "defaults fail", os: "windows", timeout: 10 * time.Minute, wantError: true},
		{name: "timeout alone still caches", os: "windows", timeout: 40 * time.Minute, wantError: true},
		{name: "count alone still times out", os: "windows", count: true, timeout: 10 * time.Minute, wantError: true},
		{name: "canonical", os: "windows", count: true, timeout: 40 * time.Minute},
		{name: "focused", os: "windows", count: true, run: "^TestOne$", timeout: time.Minute},
		{name: "focused still needs count", os: "windows", run: "^TestOne$", timeout: time.Minute, wantError: true},
		{name: "broad regex", os: "windows", count: true, run: ".*", timeout: time.Minute, wantError: true},
		{name: "unlimited", os: "windows", count: true},
		{name: "listing", os: "windows", list: "."},
		{name: "explicit diagnostic", os: "windows", allow: true, timeout: time.Second},
		{name: "other platforms", os: "linux", timeout: time.Minute},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := check(tt.os, tt.count, tt.run, tt.list, tt.timeout, tt.allow)
			if (err != nil) != tt.wantError {
				t.Fatalf("check returned %v, wantError=%v", err, tt.wantError)
			}
		})
	}
}
