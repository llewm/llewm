package portable

// envelopeIndex is the small metadata summary each envelope carries so a reader
// can list records without decoding their payloads.
type envelopeIndex struct {
	Title        string   `json:"title,omitempty"`
	Status       string   `json:"status,omitempty"`
	Tags         []string `json:"tags,omitempty"`
	SourceCommit string   `json:"source_commit,omitempty"`
	TaskID       string   `json:"task_id,omitempty"`
}
