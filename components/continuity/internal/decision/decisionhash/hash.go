// Package decisionhash computes a decision record's semantic hash. It has no
// dependencies so the database migrations can rehash decisions.
package decisionhash

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

type Link struct {
	Kind           string `json:"kind"`
	TargetKind     string `json:"target_kind"`
	TargetID       string `json:"target_id"`
	TargetRevision string `json:"target_revision,omitempty"`
	Authority      string `json:"authority,omitempty"`
}

// Fields are the hashed fields of a decision record.
type Fields struct {
	ID           string `json:"id"`
	ProjectID    string `json:"project_id"`
	Title        string `json:"title"`
	Rationale    string `json:"rationale"`
	Disposition  string `json:"disposition"`
	Actor        string `json:"actor"`
	ActorType    string `json:"actor_type"`
	Authority    string `json:"authority"`
	ReviewSource string `json:"review_source"`
	SupersedesID string `json:"supersedes_id"`
	Links        []Link `json:"links"`
}

// Hash is the project-scoped record_sha256 of a decision and its links.
func Hash(fields Fields) (string, error) {
	body, err := json.Marshal(fields)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}
