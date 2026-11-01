package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"time"
)

// CommitPinned commits a transaction begun on conn. The caller must keep that
// connection pinned until this returns. SQLite can leave its native transaction
// active after COMMIT fails even though database/sql has finalized the Tx.
func CommitPinned(ctx context.Context, conn *sql.Conn, tx *sql.Tx) error {
	if err := tx.Commit(); err != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, rollbackErr := conn.ExecContext(cleanup, `ROLLBACK`); rollbackErr != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
		return err
	}
	return nil
}
