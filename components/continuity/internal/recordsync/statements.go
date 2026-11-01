package recordsync

import (
	"context"
	"database/sql"

	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

// statementSet is owned by one call inside one transaction. It retains no
// bodies or cross-transaction state; closing it leaves rollback to the caller.
type statementSet struct {
	tx         *sql.Tx
	statements map[string]*sql.Stmt
}

func (s *statementSet) prepare(ctx context.Context, query string) (*sql.Stmt, error) {
	if stmt := s.statements[query]; stmt != nil {
		return stmt, nil
	}
	stmt, err := s.tx.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	if s.statements == nil {
		s.statements = make(map[string]*sql.Stmt)
	}
	s.statements[query] = stmt
	syncwork.RecordWork(ctx, "observation_sql_prepares", 1)
	return stmt, nil
}

func (s *statementSet) exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	stmt, err := s.prepare(ctx, query)
	if err != nil {
		return nil, err
	}
	return stmt.ExecContext(ctx, args...)
}

func (s *statementSet) query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	stmt, err := s.prepare(ctx, query)
	if err != nil {
		return nil, err
	}
	return stmt.QueryContext(ctx, args...)
}

func (s *statementSet) close() {
	for _, stmt := range s.statements {
		_ = stmt.Close()
	}
}
