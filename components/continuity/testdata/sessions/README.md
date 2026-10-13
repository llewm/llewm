# Lifecycle fixtures

These are synthetic, sanitized examples for the scaffold's configured hook events,
not payloads captured from live client sessions. The CLI lifecycle test checks that
every event in both plugin hook configurations has a fixture, ingests all examples,
checks startup receipts, and compares audit output with a golden report.

Expected totals: 15 events, 3 sessions, 3 tool calls, 1 failure. These fixtures do
not establish compatibility with an installed client's full payload schema. Add
sanitized real examples after live startup and compaction checks.
