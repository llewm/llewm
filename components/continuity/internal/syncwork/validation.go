package syncwork

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

type validationScopeKey struct{}

// WithFreshValidation gives one operation independent verification receipts.
// Real source/build binding remains part of every base key. Calls within this
// operation can reuse work they have just completed; ordinary runs cannot
// inherit its receipts. No existing proof or durable history is deleted.
func WithFreshValidation(ctx context.Context) (context.Context, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, fmt.Errorf("create fresh validation scope: %w", err)
	}
	return context.WithValue(ctx, validationScopeKey{}, hex.EncodeToString(nonce[:])), nil
}

// ScopedValidationKey preserves the ordinary key unless a caller explicitly
// requested fresh validation. It never turns a missing base key into a proof.
func ScopedValidationKey(ctx context.Context, key string) string {
	scope, _ := ctx.Value(validationScopeKey{}).(string)
	if scope == "" || key == "" {
		return key
	}
	sum := sha256.Sum256([]byte("fresh-validation-v1\x00" + key + "\x00" + scope))
	return hex.EncodeToString(sum[:])
}
