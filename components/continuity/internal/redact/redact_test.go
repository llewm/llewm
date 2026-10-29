package redact

import (
	"fmt"
	"strings"
	"testing"
)

func TestStringRemovesCommonSecrets(t *testing.T) {
	input := "Authorization: Bearer abc123 token=supersecret https://user:pass@example.test/path"
	got := String(input)
	for _, secret := range []string{"abc123", "supersecret", "pass"} {
		if strings.Contains(got, secret) {
			t.Fatalf("redacted output still contains %q: %s", secret, got)
		}
	}
}

func referenceString(value string) string {
	for _, pattern := range patterns {
		value = pattern.ReplaceAllString(value, `${1}[REDACTED]`)
	}
	return value
}

func FuzzStringMatchesReplacementRules(f *testing.F) {
	for _, value := range []string{
		"ordinary project history; token rotation remains planned",
		`"api_key": "secret with spaces"`,
		"'PaSsWoRd' = 'one two' token=x",
		"ſecret=x API_KEY='x' httpſ://user:pass@example.test/path",
		"AUTHORIZATION:\tBEARER\tx\nsecret=next",
		"https://user:pass@example.test token=\"quoted\"",
		"token= secret='\npassword=\rkey=x",
	} {
		f.Add(value)
	}
	f.Fuzz(func(t *testing.T, value string) {
		if got, want := String(value), referenceString(value); got != want {
			t.Fatalf("redaction differs for %q: got %q want %q", value, got, want)
		}
	})
}

func BenchmarkHistoricalText(b *testing.B) {
	for _, redacted := range []bool{false, true} {
		text := strings.Repeat("Status: completed canonical history validation; token rotation remains planned. Preserve attribution, repair and fallback.\n", 100)
		if redacted {
			text += " secret='synthetic benchmark credential'"
		}
		for _, reference := range []bool{false, true} {
			b.Run(fmt.Sprintf("secret_%t/reference_%t", redacted, reference), func(b *testing.B) {
				b.SetBytes(int64(len(text)))
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					if reference {
						_ = referenceString(text)
					} else {
						_ = String(text)
					}
				}
			})
		}
	}
}
