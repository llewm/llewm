package portable

import "github.com/llewm/llewm/components/continuity/internal/fork"

// ImportInvalidationGuards describes conservative links for observations that
// cannot participate in partial semantic application. A complete reference
// baseline may retain these guards; reaching one requires reference replay.
// The boolean distinguishes known immutable rules from unknown input. It does
// not authorize importing the observation or treating it as independent.
func ImportInvalidationGuards(object EncodedEnvelope) ([]ImportDependency, bool) {
	e := object.Envelope
	if RetiredRecordType(e.RecordType) {
		return nil, true
	} // Reference import ignores them.
	if !SupportedEnvelope(e) {
		return nil, false
	}
	var result []ImportDependency
	exact := func(kind, id string, revision int) {
		if id != "" {
			result = append(result, ImportDependency{RecordType: kind, RecordID: id, Mode: DependencyExact, Revision: revision})
		}
	}
	switch e.RecordType {
	case "redaction_recovery":
		value, err := decodeRedactionRecovery(object)
		if err != nil {
			return nil, false
		}
		for _, body := range [][]byte{value.Original, value.Derived} {
			target, err := recoveryEnvelope(body)
			if err != nil {
				return nil, false
			}
			exact(target.Envelope.RecordType, target.Envelope.RecordID, target.Envelope.RecordRevision)
			dependencies, known := ImportDependencies(target.Envelope)
			if !known {
				return nil, false
			}
			// Both interpretations matter: additional redaction may change a target
			// inside the payload while preserving the recovered record's own identity.
			result = append(result, dependencies...)
		}
	case "fork_resolution", "revision_fork_resolution", "supersession_fork_resolution":
		decoded, err := decodeOperationalBundle(Bundle{Records: []EncodedEnvelope{object}})
		if err != nil || len(decoded.resolutions) != 1 {
			return nil, false
		}
		for _, value := range decoded.resolutions {
			switch value.Family {
			case fork.FamilyRevision:
				kind, id, revision, err := fork.ParseRevisionSubject(value.Subject)
				if err != nil {
					return nil, false
				}
				exact(kind, id, revision)
			case fork.FamilySupersession:
				kind, id, err := fork.ParseSupersessionSubject(value.Subject)
				if err != nil {
					return nil, false
				}
				if kind == "narrative" {
					kind = "work_narrative"
				}
				exact(kind, id, 0)
				exact(kind, value.AcceptedHead, 0)
				for _, id := range value.RejectedHeads {
					exact(kind, id, 0)
				}
			case fork.FamilyCheckpoint:
				exact("checkpoint", value.AcceptedHead, 0)
				exact("checkpoint", value.Continuation, 0)
				for _, id := range value.RejectedHeads {
					exact("checkpoint", id, 0)
				}
			default:
				return nil, false
			}
		}
	default:
		return nil, false
	}
	return normalizeImportDependencies(result), true
}
