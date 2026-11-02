package redact

import (
	"reflect"
	"strings"
	"testing"
)

func TestValueRedactsNestedMetadataWithoutMutatingInput(t *testing.T) {
	for worker := 0; worker < 8; worker++ {
		t.Run("concurrent", func(t *testing.T) {
			t.Parallel()
			input := map[string]any{
				"nested": []any{map[string]any{
					"API_-KEY": "one", "AcCeSs_ToKeN": "two", "refresh-token": "three",
					"private_key": "four", "ENVIRONMENT": map[string]any{"nested": "five"},
					"stdin": "six", "credentials": "seven", "authorization": "eight",
					"password": "nine", "secret": "ten", "token": "eleven", "env": "twelve",
					"description": "token=synthetic", "token_count": float64(12), "ordinary": "kept",
				}},
			}
			wantFields := map[string]any{
				"API_-KEY": "[REDACTED]", "AcCeSs_ToKeN": "[REDACTED]", "refresh-token": "[REDACTED]",
				"private_key": "[REDACTED]", "ENVIRONMENT": "[REDACTED]", "stdin": "[REDACTED]",
				"credentials": "[REDACTED]", "authorization": "[REDACTED]", "password": "[REDACTED]",
				"secret": "[REDACTED]", "token": "[REDACTED]", "env": "[REDACTED]",
				"description": "token=[REDACTED]", "token_count": float64(12), "ordinary": "kept",
			}
			want := map[string]any{"nested": []any{wantFields}}
			for n := 0; n < 64; n++ {
				got := Value(input)
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("nested metadata redaction: %#v", got)
				}
				// Mutating a returned tree cannot alter subsequent redactions.
				got.(map[string]any)["nested"].([]any)[0].(map[string]any)["ordinary"] = "changed"
			}
		})
	}
}

func BenchmarkMetadataKeyNormalization(b *testing.B) {
	for _, reference := range []bool{false, true} {
		name := "shared"
		if reference {
			name = "per_key"
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for n := 0; n < b.N; n++ {
				replacer := metadataKeySeparators
				if reference {
					replacer = strings.NewReplacer("_", "", "-", "")
				}
				_ = strings.ToLower(replacer.Replace("Creator_Invocation-ID"))
			}
		})
	}
}
