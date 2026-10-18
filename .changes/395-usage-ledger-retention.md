### Retention for the `usageHistory` ledger (opt-in)

`requestDetails` has had a retention loop and a dashboard control; the ledger has
had neither, so a gateway that has been running for a year carried every row it
ever wrote. This adds an opt-in window for it and the machinery that makes
deleting billing rows safe.

- **New setting `usageHistoryRetentionDays`, default `0` = keep forever.** It is a
  separate knob from the requestDetails `retentionDays`, because one trims
  diagnostics and the other deletes billing rows. Existing installs, and the
  database shared with upstream, are unaffected until an operator turns it on.
- **Per-key token budgets stay exact.** Spend is derived from this table
  (`tokenUsageSince` sums `promptTokens + completionTokens`), so deleting an old
  row used to silently refund an exhausted key. Before a range is deleted, the
  spend those rows contributed is summed into a new Go-only table
  `usageLedgerCarryover`, and the budget reader adds that term back. The reader is
  a single statement, so a prune landing between its two halves cannot report a
  value that is neither the before nor the after.
- **A reset clears the carryover** in the same transaction as the new mark, so a
  reset issued after a prune still starts from zero.
- **Chunked, transactional, bounded.** Each 5000-row chunk is folded and deleted
  in one transaction, so a crash cannot leave a deleted row whose spend was not
  carried (which would refund a key) or a carried row that still exists (which
  would charge it twice). The write lock is never held for a full-table delete.
- **Separate, explicit dashboard action.** `POST /api/settings/database/prune-usage`
  takes `{days}` or `{before}`, supports `?dryRun=1`, and is deliberately not wired
  into the existing *Prune Expired Records* button: deleting the ledger is an
  operator's decision with a visible row count attached.
- **`GET /api/settings/database/stats`** now reports the ledger's oldest and
  newest timestamps (covering-index seeks), so "did that prune do anything" is
  answerable from the dashboard. The Storage card also shows the ledger row count
  and offers the action behind a typed confirmation.

Verification: `go test ./internal/db/... ./internal/handlers/dashboard/...`,
`make test-integration` (a key at its budget is still refused with 403 after the
rows that pushed it over are pruned), `make vet-svelte`, `cd web && bun run build`.

Caveats, stated rather than implied: `all` and the analytics pages now mean "as
far back as rows are retained" once a window is configured; the carryover holds
tokens only, so a future USD budget measured from this table will need it to grow
a cost column; and a database shared with upstream 9router will show upstream the
shortened ledger.

Closes #392
