-- Drops the one-time commit-attribution repair requests that 097 and 098
-- queued for the next hook. Every known database ran them, and the code that
-- ran them is removed. commit_author_amendments keeps their results. An older
-- binary's hook treats the missing table as a failed best-effort repair.
DROP TABLE commit_author_repairs_pending;
