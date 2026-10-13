package audit

import (
	"context"
	"database/sql"
	"fmt"
)

type Summary struct {
	Events           int64 `json:"events"`
	Sessions         int64 `json:"sessions"`
	ToolCalls        int64 `json:"tool_calls"`
	FailedActions    int64 `json:"failed_actions"`
	Requests         int64 `json:"requests"`
	InputTokens      int64 `json:"input_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
}

type Service struct{ DB *sql.DB }

func (s Service) Summarize(ctx context.Context, projectID string) (Summary, error) {
	var summary Summary
	err := s.DB.QueryRowContext(ctx, `SELECT
      (SELECT COUNT(*) FROM events WHERE project_id = ?),
      (SELECT COUNT(*) FROM sessions WHERE project_id = ?),
      (SELECT COUNT(*) FROM events WHERE project_id = ? AND category = 'tool'),
      (SELECT COUNT(*) FROM events WHERE project_id = ? AND outcome = 'failure'),
      (SELECT COUNT(*) FROM usage_requests WHERE project_id = ?),
      COALESCE((SELECT SUM(input_tokens) FROM usage_requests WHERE project_id = ?), 0),
      COALESCE((SELECT SUM(cache_write_tokens) FROM usage_requests WHERE project_id = ?), 0),
      COALESCE((SELECT SUM(cache_read_tokens) FROM usage_requests WHERE project_id = ?), 0),
      COALESCE((SELECT SUM(output_tokens) FROM usage_requests WHERE project_id = ?), 0)`,
		projectID, projectID, projectID, projectID, projectID, projectID, projectID, projectID, projectID).Scan(
		&summary.Events, &summary.Sessions, &summary.ToolCalls, &summary.FailedActions,
		&summary.Requests, &summary.InputTokens, &summary.CacheWriteTokens, &summary.CacheReadTokens, &summary.OutputTokens)
	return summary, err
}

func Text(summary Summary) string {
	return fmt.Sprintf("Events: %d\nSessions: %d\nTool calls: %d\nFailed actions: %d\nModel requests: %d (exact, from session files; llewm usage ingest)\nInput tokens: %d\nCache write tokens: %d\nCache read tokens: %d\nOutput tokens: %d\n",
		summary.Events, summary.Sessions, summary.ToolCalls, summary.FailedActions,
		summary.Requests, summary.InputTokens, summary.CacheWriteTokens, summary.CacheReadTokens, summary.OutputTokens)
}
