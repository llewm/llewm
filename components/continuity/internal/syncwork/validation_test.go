package syncwork

import (
	"context"
	"strings"
	"testing"
)

func TestFreshValidationKeysAreScopedAndPreserveBaseBinding(t *testing.T) {
	base := strings.Repeat("a", 64)
	plain := context.Background()
	if ScopedValidationKey(plain, base) != base {
		t.Fatal("normal proof binding changed")
	}
	first, err := WithFreshValidation(plain)
	if err != nil {
		t.Fatal(err)
	}
	second, err := WithFreshValidation(first)
	if err != nil {
		t.Fatal(err)
	}
	a, b := ScopedValidationKey(first, base), ScopedValidationKey(second, base)
	if len(a) != 64 || a == base || a == b || ScopedValidationKey(first, "") != "" {
		t.Fatal("fresh validation reused a scope or upgraded an empty binding")
	}
	if ScopedValidationKey(first, strings.Repeat("b", 64)) == a {
		t.Fatal("different source binding inherited a proof")
	}
	child, cancel := context.WithCancel(first)
	cancel()
	if ScopedValidationKey(context.WithoutCancel(child), base) != a {
		t.Fatal("local sync context lost the requested validation scope")
	}
	if ScopedValidationKey(plain, base) != base {
		t.Fatal("fresh request changed its parent context")
	}
}
