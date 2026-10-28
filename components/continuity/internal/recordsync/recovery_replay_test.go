package recordsync

import (
	"strings"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/portable"
)

func TestRecoveryReplayEligibilityRejectsUnknownSchemas(t *testing.T) {
	object := func(kind string, schema int, hash string) portable.EncodedEnvelope {
		return portable.EncodedEnvelope{Envelope: portable.Envelope{RecordType: kind, RecordSchemaVersion: schema, EnvelopeSHA256: hash}}
	}
	recovery := object("redaction_recovery", 1, strings.Repeat("1", 64))
	retired := object("contract", 1, strings.Repeat("2", 64))
	if key, ok := recoveryReplaySet([]portable.EncodedEnvelope{recovery, retired}); !ok || !strings.HasPrefix(key, "recovery:v2:") {
		t.Fatalf("retired observation prevented proof candidacy: %q %v", key, ok)
	}
	unknown := object("work_narrative", 999, strings.Repeat("3", 64))
	if _, ok := recoveryReplaySet([]portable.EncodedEnvelope{recovery, unknown}); ok {
		t.Fatal("unsupported schema accepted for replay")
	}
}
