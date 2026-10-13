package usage

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// maxLineBytes bounds one session-file line; longer lines (huge tool
// outputs) are skipped whole.
const maxLineBytes = 32 << 20

// Ingester reads this project's session files into the usage tables.
type Ingester struct {
	DB        *sql.DB
	ProjectID string
	// Roots are the project's checkout roots; a session belongs to the
	// project when its working directory is inside one.
	Roots []string
	// ClaudeHome and CodexHome default to $CLAUDE_CONFIG_DIR or ~/.claude and
	// $CODEX_HOME or ~/.codex.
	ClaudeHome string
	CodexHome  string
	Now        func() time.Time
}

// Result counts what one ingestion added.
type Result struct {
	Files        int `json:"files_read"`
	UsageRecords int `json:"usage_records"`
	ParseIssues  int `json:"parse_issues"`
	Requests     int `json:"requests"`
	Calls        int `json:"tool_calls"`
	Results      int `json:"tool_results"`
}

func (r *Result) add(other Result) {
	r.Files += other.Files
	r.UsageRecords += other.UsageRecords
	r.ParseIssues += other.ParseIssues
	r.Requests += other.Requests
	r.Calls += other.Calls
	r.Results += other.Results
}

func (in Ingester) now() time.Time {
	if in.Now != nil {
		return in.Now().UTC()
	}
	return time.Now().UTC()
}

func home(env, dir string) string {
	if value := os.Getenv(env); value != "" {
		return value
	}
	base, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(base, dir)
}

func (in Ingester) claudeHome() string {
	if in.ClaudeHome != "" {
		return in.ClaudeHome
	}
	return home("CLAUDE_CONFIG_DIR", ".claude")
}

func (in Ingester) codexHome() string {
	if in.CodexHome != "" {
		return in.CodexHome
	}
	return home("CODEX_HOME", ".codex")
}

var nonAlnum = regexp.MustCompile(`[^A-Za-z0-9]`)

// claudeFiles lists transcripts in project directories that may hold this
// project's sessions: the directory named after each root and after any path
// below it (Claude Code names directories by replacing every other character
// with "-"). The cwd recorded inside each file decides membership.
func (in Ingester) claudeFiles(session string) []string {
	base := filepath.Join(in.claudeHome(), "projects")
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	var files []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		for _, root := range in.Roots {
			prefix := nonAlnum.ReplaceAllString(filepath.Clean(root), "-")
			if entry.Name() != prefix && !strings.HasPrefix(entry.Name(), prefix+"-") {
				continue
			}
			pattern := "*.jsonl"
			if session != "" {
				pattern = session + ".jsonl"
			}
			matches, _ := filepath.Glob(filepath.Join(base, entry.Name(), pattern))
			files = append(files, matches...)
			break
		}
	}
	return files
}

// codexFiles lists rollouts, all of them or one session's.
func (in Ingester) codexFiles(session string) []string {
	pattern := "rollout-*.jsonl"
	if session != "" {
		pattern = "rollout-*-" + session + ".jsonl"
	}
	matches, _ := filepath.Glob(filepath.Join(in.codexHome(), "sessions", "*", "*", "*", pattern))
	return matches
}

// inside reports whether dir is one of the roots or below one.
func (in Ingester) inside(dir string) bool {
	if dir == "" {
		return false
	}
	dir = filepath.Clean(dir)
	for _, root := range in.Roots {
		root = filepath.Clean(root)
		if dir == root || strings.HasPrefix(dir, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// Session ingests one session's file, as hooks do at SessionEnd and
// PreCompact.
func (in Ingester) Session(ctx context.Context, client, session string) (Result, error) {
	if session == "" || strings.ContainsAny(session, `/\*?[`) {
		return Result{}, nil
	}
	var files []string
	switch client {
	case ClaudeCode:
		files = in.claudeFiles(session)
	case Codex:
		files = in.codexFiles(session)
	}
	return in.files(ctx, client, files)
}

// All ingests every session file of both clients that belongs to the
// project, resuming each where the last read stopped.
func (in Ingester) All(ctx context.Context) (Result, error) {
	total, err := in.files(ctx, ClaudeCode, in.claudeFiles(""))
	if err != nil {
		return total, err
	}
	codex, err := in.files(ctx, Codex, in.codexFiles(""))
	total.add(codex)
	return total, err
}

type source struct {
	matched bool
	size    int64
	offset  int64
	known   bool
}

func (in Ingester) source(ctx context.Context, path string) (source, error) {
	var value source
	var matched int
	err := in.DB.QueryRowContext(ctx, `SELECT matched,size,offset FROM usage_sources WHERE project_id=? AND path=?`, in.ProjectID, path).Scan(&matched, &value.size, &value.offset)
	if errors.Is(err, sql.ErrNoRows) {
		return source{}, nil
	}
	value.matched, value.known = matched == 1, true
	return value, err
}

func (in Ingester) files(ctx context.Context, client string, paths []string) (Result, error) {
	sort.Strings(paths)
	var total Result
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		result, err := in.file(ctx, client, path)
		if err != nil {
			return total, err
		}
		total.add(result)
	}
	return total, nil
}

// file reads a session file from where the last read stopped to its last
// complete line and records what it found, all in one transaction.
func (in Ingester) file(ctx context.Context, client, path string) (Result, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Result{}, nil // vanished since listing
	}
	previous, err := in.source(ctx, path)
	if err != nil {
		return Result{}, err
	}
	if previous.known && (!previous.matched || info.Size() == previous.offset) {
		return Result{}, nil
	}
	start := previous.offset
	if info.Size() < start {
		start = 0 // rewritten; inserts are idempotent
	}
	file, err := os.Open(path)
	if err != nil {
		return Result{}, nil
	}
	defer file.Close()
	var p parser = claudeParser{}
	if client == Codex {
		p = &codexParser{}
		if start > 0 {
			// The session and model come from earlier lines; reread them.
			if err := primeCodex(file, start, p.(*codexParser)); err != nil {
				return Result{}, err
			}
		}
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return Result{}, err
	}
	var batch Batch
	reader := bufio.NewReaderSize(file, 1<<20)
	offset := start
	for {
		line, err := readLine(reader)
		if err != nil {
			break // EOF or a partial final line: stop before it
		}
		offset += int64(len(line))
		if len(line) <= maxLineBytes {
			p.line(line, &batch)
		}
		if !previous.known && batch.CWD != "" && !in.inside(batch.CWD) {
			break // another project's session: no need to read the rest
		}
	}
	matched := previous.matched || (!previous.known && in.inside(batch.CWD))
	if !previous.known && batch.CWD == "" {
		// Nothing tells us where this session ran yet; look again later.
		return Result{}, nil
	}
	if matched && batch.ParseIssues > 0 {
		return Result{}, fmt.Errorf("%s has %d incompatible usage record(s); ingestion stopped before advancing its cursor", path, batch.ParseIssues)
	}
	session := batch.SessionID
	if session == "" {
		session = strings.TrimSuffix(filepath.Base(path), ".jsonl")
	}
	var result Result
	tx, err := in.DB.BeginTx(ctx, nil)
	if err != nil {
		return Result{}, err
	}
	defer tx.Rollback()
	if matched {
		if result, err = in.record(ctx, tx, client, session, batch); err != nil {
			return Result{}, err
		}
		result.UsageRecords = batch.UsageRecords
		result.ParseIssues = batch.ParseIssues
		if offset > start {
			result.Files = 1
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO usage_sources(project_id,client,path,session_id,matched,size,offset,scanned_at) VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(project_id,path) DO UPDATE SET size=excluded.size, offset=excluded.offset, scanned_at=excluded.scanned_at, session_id=excluded.session_id`,
		in.ProjectID, client, path, session, boolInt(matched), info.Size(), offset, in.now().Format(time.RFC3339Nano)); err != nil {
		return Result{}, err
	}
	return result, tx.Commit()
}

// readLine returns one line including its newline, or an error at EOF or
// before a final line without one.
func readLine(reader *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, err := reader.ReadSlice('\n')
		line = append(line, chunk...)
		if err == nil {
			return line, nil
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return nil, err
		}
	}
}

// primeCodex reads the lines before start for the session and model only.
func primeCodex(file *os.File, start int64, p *codexParser) error {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	reader := bufio.NewReaderSize(io.LimitReader(file, start), 1<<20)
	var ignored Batch
	for {
		line, err := readLine(reader)
		if err != nil {
			return nil
		}
		if len(line) <= maxLineBytes && (strings.Contains(string(line[:min(len(line), 200)]), `"session_meta"`) || strings.Contains(string(line[:min(len(line), 200)]), `"turn_context"`)) {
			p.line(line, &ignored)
		}
	}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (in Ingester) record(ctx context.Context, tx *sql.Tx, client, session string, batch Batch) (Result, error) {
	var result Result
	sessionOf := func(value string) string {
		if value == "" {
			return session
		}
		return value
	}
	for _, value := range batch.Requests {
		res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO usage_requests(project_id,client,session_id,request_id,model,occurred_at,input_tokens,cache_write_tokens,cache_write_long_tokens,cache_read_tokens,output_tokens,reasoning_tokens,sidechain) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			in.ProjectID, client, sessionOf(value.SessionID), value.RequestID, value.Model, value.OccurredAt.Format(time.RFC3339Nano), value.InputTokens, value.CacheWriteTokens, value.CacheWriteLongTokens, value.CacheReadTokens, value.OutputTokens, value.ReasoningTokens, boolInt(value.Sidechain))
		if err != nil {
			return Result{}, err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			result.Requests++
		}
	}
	for _, value := range batch.Calls {
		res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO usage_tool_calls(project_id,client,session_id,call_id,tool,occurred_at) VALUES(?,?,?,?,?,?)`,
			in.ProjectID, client, sessionOf(value.SessionID), value.CallID, value.Tool, value.OccurredAt.Format(time.RFC3339Nano))
		if err != nil {
			return Result{}, err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			result.Calls++
		}
	}
	for _, value := range batch.Results {
		res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO usage_tool_results(project_id,client,session_id,call_id,result_bytes,is_error,occurred_at) VALUES(?,?,?,?,?,?,?)`,
			in.ProjectID, client, sessionOf(value.SessionID), value.CallID, value.ResultBytes, boolInt(value.IsError), value.OccurredAt.Format(time.RFC3339Nano))
		if err != nil {
			return Result{}, err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			result.Results++
		}
	}
	return result, nil
}
