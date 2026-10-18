### feat(db): opt-in retention for the `usageHistory` ledger

`usageHistory` held one row per request and was never pruned: the 24h/7d windows
fold it in memory, the analytics pages scan it, and `analyticsrange` documented
the assumption in a comment. A gateway that had been running for a year carried
every row it ever wrote, with no knob to drop the part an operator did not need.

The constraint that made this non-trivial is the per-key token budget:
`tokenQuota` is measured by `tokenUsageSince`, which sums
`promptTokens + completionTokens` from `usageHistory` after the key's reset mark,
so deleting old rows silently *refunds* spend and an exhausted key becomes
unexhausted.

Before a range is deleted, its tokens are banked per key in the new Go-only
`usageLedgerCarryover` table, and `tokenUsageSince` adds that term. The sum stays
derived from data rather than a drifting counter, and only one reader changes
instead of every ledger consumer. `ResetApiKeyQuota` clears the key's carryover
in the same transaction, so a reset after a prune still starts from zero.

New setting `usageHistoryRetentionDays`, default `0` = keep forever, so existing
installs and the shared upstream database are unaffected until an operator turns
it on. The retention loop deletes in 5000-row chunks and runs `PRAGMA optimize`
afterwards. A separate explicit action, `POST
/api/settings/database/prune-usage` (with a dry-run), is the only path that
touches the ledger; the existing "Prune Expired Records" button still owns
`requestDetails` alone. `usageDaily`, `requestDetails` and
`compressionAnalytics` are never touched, and `VACUUM` stays manual.

The window is floored at 24h. The raw usage cache ages rows out of a sliding
window by reading them back from the ledger and subtracting them, which is exact
only while those rows still exist; a shorter retention would delete rows it still
owes a subtraction for and the totals would read high until the cache rebuilt.

**Verified.** `go build ./...`, `go vet ./...` and the full `go test ./...` suite
pass. `internal/db/usage_history_retention_test.go` pins the issue's acceptance
criteria one test each: a key's usage reads identically before and after a prune
that deletes rows inside its budget range, rows at or before the reset mark carry
nothing over, two prunes accumulate while a reset between them clears,
`usageDaily`/`requestDetails`/rows at or after the cutoff survive, a dry run
reports the count without deleting, and `0`/negative/sub-24h windows are refused
rather than read as "delete everything".
`internal/handlers/dashboard/prune_usage_history_test.go` covers the endpoint's
dry-run-then-prune contract and its refusal when no window is configured.
