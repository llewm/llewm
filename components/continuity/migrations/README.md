# Migrations

Executable migrations are embedded from `internal/database/migrations` so the released binary remains self-contained. Files there are append-only after release; never edit an applied migration.

Migration 011 adds append-only commit-narrative evidence records and normalized
affected paths. It is additive and preserves schema-010 records; application tests
exercise the 010 → 011 path explicitly.
