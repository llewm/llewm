package redact

import (
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
