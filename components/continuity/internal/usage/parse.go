// Package usage records exact, provider-reported model usage and tool-result
// sizes read from client session files: Claude Code transcripts and Codex
// rollouts. Only usage fields, model names, tool names, and byte sizes are
// kept, never prompt, response, or tool content. Rows are device-local and
// insert-only; re-reading a file adds nothing it already added.
package usage

import (
	"bytes"
	"encoding/json"
	"time"
)

// Clients whose session files are read.
const (
	ClaudeCode = "claude-code"
	Codex      = "codex"
)

// Request is one model request with its exact token counts. InputTokens
// excludes cached tokens; OutputTokens includes ReasoningTokens.
type Request struct {
	SessionID        string    `json:"session_id"`
	RequestID        string    `json:"request_id"`
	Model            string    `json:"model"`
	OccurredAt       time.Time `json:"occurred_at"`
	InputTokens      int64     `json:"input_tokens"`
	CacheWriteTokens int64     `json:"cache_write_tokens"`
	// CacheWriteLongTokens is the part of CacheWriteTokens written with a
	// long (1 hour) lifetime, which is priced higher.
	CacheWriteLongTokens int64 `json:"cache_write_long_tokens"`
	CacheReadTokens      int64 `json:"cache_read_tokens"`
	OutputTokens         int64 `json:"output_tokens"`
	ReasoningTokens      int64 `json:"reasoning_tokens"`
	// Sidechain marks a request made by a subagent inside the session.
	Sidechain bool `json:"sidechain,omitempty"`
}

// ToolCall is one tool invocation by name.
type ToolCall struct {
	SessionID  string    `json:"session_id"`
	CallID     string    `json:"call_id"`
	Tool       string    `json:"tool"`
	OccurredAt time.Time `json:"occurred_at"`
}

// ToolResult is the size of what a tool returned into the context.
type ToolResult struct {
	SessionID   string    `json:"session_id"`
	CallID      string    `json:"call_id"`
	ResultBytes int64     `json:"result_bytes"`
	IsError     bool      `json:"is_error,omitempty"`
	OccurredAt  time.Time `json:"occurred_at"`
}

// Batch is what one read of a session file found.
type Batch struct {
	Requests []Request
	Calls    []ToolCall
	Results  []ToolResult
	// UsageRecords and ParseIssues make recognized usage-schema drift visible
	// without retaining any transcript content.
	UsageRecords int
	ParseIssues  int
	// CWD and SessionID are what the file records, when seen.
	CWD       string
	SessionID string
}

// parser reads one line at a time and keeps per-file state (the Codex
// session and model) across lines.
type parser interface {
	line(raw []byte, batch *Batch)
}

func parseTime(value string) time.Time {
	at, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return at.UTC()
}

// contentBytes measures a tool result as it enters the context: the string
// itself, or the text of its text blocks plus the raw size of other blocks.
func contentBytes(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return int64(len(text))
	}
	var blocks []json.RawMessage
	if json.Unmarshal(raw, &blocks) == nil {
		var total int64
		for _, block := range blocks {
			var typed struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if json.Unmarshal(block, &typed) == nil && typed.Type == "text" {
				total += int64(len(typed.Text))
			} else {
				total += int64(len(block))
			}
		}
		return total
	}
	return int64(len(raw))
}

type claudeParser struct{}

type claudeLine struct {
	Type        string `json:"type"`
	SessionID   string `json:"sessionId"`
	CWD         string `json:"cwd"`
	Timestamp   string `json:"timestamp"`
	IsSidechain bool   `json:"isSidechain"`
	Message     struct {
		ID      string          `json:"id"`
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
		Usage   *struct {
			InputTokens         int64 `json:"input_tokens"`
			CacheCreationTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadTokens     int64 `json:"cache_read_input_tokens"`
			OutputTokens        int64 `json:"output_tokens"`
			OutputDetails       struct {
				ThinkingTokens int64 `json:"thinking_tokens"`
			} `json:"output_tokens_details"`
			CacheCreation struct {
				LongTokens int64 `json:"ephemeral_1h_input_tokens"`
			} `json:"cache_creation"`
		} `json:"usage"`
	} `json:"message"`
}

type claudeBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

func (claudeParser) line(raw []byte, batch *Batch) {
	var value claudeLine
	if json.Unmarshal(raw, &value) != nil {
		return
	}
	if value.CWD != "" && batch.CWD == "" {
		batch.CWD = value.CWD
	}
	if value.SessionID != "" && batch.SessionID == "" {
		batch.SessionID = value.SessionID
	}
	at := parseTime(value.Timestamp)
	var blocks []claudeBlock
	_ = json.Unmarshal(value.Message.Content, &blocks)
	switch value.Type {
	case "assistant":
		if usage := value.Message.Usage; usage != nil && value.Message.Model != "<synthetic>" {
			batch.UsageRecords++
			if value.Message.ID == "" || value.Message.Model == "" || usage.InputTokens < 0 || usage.CacheCreationTokens < 0 || usage.CacheCreation.LongTokens < 0 || usage.CacheCreation.LongTokens > usage.CacheCreationTokens || usage.CacheReadTokens < 0 || usage.OutputTokens < 0 || usage.OutputDetails.ThinkingTokens < 0 || usage.OutputDetails.ThinkingTokens > usage.OutputTokens {
				batch.ParseIssues++
				break
			}
			batch.Requests = append(batch.Requests, Request{SessionID: value.SessionID, RequestID: value.Message.ID, Model: value.Message.Model, OccurredAt: at,
				InputTokens: usage.InputTokens, CacheWriteTokens: usage.CacheCreationTokens, CacheWriteLongTokens: usage.CacheCreation.LongTokens, CacheReadTokens: usage.CacheReadTokens,
				OutputTokens: usage.OutputTokens, ReasoningTokens: usage.OutputDetails.ThinkingTokens, Sidechain: value.IsSidechain})
		}
		for _, block := range blocks {
			if block.Type == "tool_use" && block.ID != "" && block.Name != "" {
				batch.Calls = append(batch.Calls, ToolCall{SessionID: value.SessionID, CallID: block.ID, Tool: block.Name, OccurredAt: at})
			}
		}
	case "user":
		for _, block := range blocks {
			if block.Type == "tool_result" && block.ToolUseID != "" {
				batch.Results = append(batch.Results, ToolResult{SessionID: value.SessionID, CallID: block.ToolUseID, ResultBytes: contentBytes(block.Content), IsError: block.IsError, OccurredAt: at})
			}
		}
	}
}

type codexParser struct {
	session   string
	model     string
	sidechain bool
}

type codexLine struct {
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Payload   struct {
		Type       string          `json:"type"`
		ID         string          `json:"id"`
		SessionID  string          `json:"session_id"`
		CWD        string          `json:"cwd"`
		Model      string          `json:"model"`
		Source     json.RawMessage `json:"source"`
		ResponseID string          `json:"response_id"`
		CallID     string          `json:"call_id"`
		Name       string          `json:"name"`
		Output     json.RawMessage `json:"output"`
		IsError    bool            `json:"is_error"`
		Usage      *struct {
			InputTokens      int64 `json:"input_tokens"`
			CachedTokens     int64 `json:"cached_input_tokens"`
			CacheWriteTokens int64 `json:"cache_write_input_tokens"`
			OutputTokens     int64 `json:"output_tokens"`
			ReasoningTokens  int64 `json:"reasoning_output_tokens"`
		} `json:"usage"`
	} `json:"payload"`
}

func (p *codexParser) line(raw []byte, batch *Batch) {
	var value codexLine
	if json.Unmarshal(raw, &value) != nil {
		return
	}
	at, payload := parseTime(value.Timestamp), value.Payload
	switch value.Type {
	case "session_meta":
		p.session = payload.ID
		if p.session == "" {
			p.session = payload.SessionID
		}
		p.sidechain = codexSubagent(payload.Source)
		if p.sidechain && payload.SessionID != "" {
			// Codex gives a subagent rollout its own thread ID and preserves the
			// parent client session separately. Group it with the parent, as
			// Claude Code does for isSidechain requests.
			p.session = payload.SessionID
		}
		batch.SessionID, batch.CWD = p.session, payload.CWD
		return
	case "turn_context":
		if payload.Model != "" {
			p.model = payload.Model
		}
		if batch.CWD == "" {
			batch.CWD = payload.CWD
		}
		return
	case "token_usage_record":
		batch.UsageRecords++
		usage := payload.Usage
		if usage == nil || payload.ResponseID == "" || p.session == "" || p.model == "" || usage.InputTokens < 0 || usage.CachedTokens < 0 || usage.CachedTokens > usage.InputTokens || usage.CacheWriteTokens < 0 || usage.OutputTokens < 0 || usage.ReasoningTokens < 0 || usage.ReasoningTokens > usage.OutputTokens {
			batch.ParseIssues++
			return
		}
		// Codex counts cached tokens inside input_tokens.
		batch.Requests = append(batch.Requests, Request{SessionID: p.session, RequestID: payload.ResponseID, Model: p.model, OccurredAt: at,
			InputTokens: usage.InputTokens - usage.CachedTokens, CacheWriteTokens: usage.CacheWriteTokens, CacheReadTokens: usage.CachedTokens,
			OutputTokens: usage.OutputTokens, ReasoningTokens: usage.ReasoningTokens, Sidechain: p.sidechain})
		return
	}
	switch payload.Type {
	case "function_call", "custom_tool_call":
		if payload.CallID != "" && payload.Name != "" {
			batch.Calls = append(batch.Calls, ToolCall{SessionID: p.session, CallID: payload.CallID, Tool: payload.Name, OccurredAt: at})
		}
	case "function_call_output", "custom_tool_call_output":
		if payload.CallID != "" {
			size, isError := codexOutput(payload.Output)
			batch.Results = append(batch.Results, ToolResult{SessionID: p.session, CallID: payload.CallID, ResultBytes: size, IsError: payload.IsError || isError, OccurredAt: at})
		}
	}
}

func codexSubagent(raw json.RawMessage) bool {
	var source struct {
		Subagent json.RawMessage `json:"subagent"`
	}
	return json.Unmarshal(raw, &source) == nil && jsonPresent(source.Subagent)
}

// codexOutput measures only the returned content when Codex wraps it, and
// recognizes the stable error indicators emitted by shell and tool adapters.
func codexOutput(raw json.RawMessage) (int64, bool) {
	size := contentBytes(raw)
	inner := raw
	var text string
	if json.Unmarshal(raw, &text) == nil {
		inner = []byte(text)
	}
	var wrapped struct {
		Output      json.RawMessage `json:"output"`
		Error       json.RawMessage `json:"error"`
		IsError     bool            `json:"is_error"`
		IsErrorJSON bool            `json:"isError"`
		Status      string          `json:"status"`
		Success     *bool           `json:"success"`
		Metadata    struct {
			ExitCode *int `json:"exit_code"`
		} `json:"metadata"`
	}
	if json.Unmarshal(inner, &wrapped) != nil {
		return size, false
	}
	if jsonPresent(wrapped.Output) {
		size = contentBytes(wrapped.Output)
	}
	isError := wrapped.IsError || wrapped.IsErrorJSON || jsonPresent(wrapped.Error) || wrapped.Metadata.ExitCode != nil && *wrapped.Metadata.ExitCode != 0 || wrapped.Success != nil && !*wrapped.Success
	switch wrapped.Status {
	case "error", "failed", "failure":
		isError = true
	}
	return size, isError
}

func jsonPresent(raw json.RawMessage) bool {
	value := bytes.TrimSpace(raw)
	return len(value) > 0 && !bytes.Equal(value, []byte("null")) && !bytes.Equal(value, []byte("false")) && !bytes.Equal(value, []byte(`""`))
}
